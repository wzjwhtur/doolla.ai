package evaluation_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/evaluation"
)

func TestDatasetFormatsAndMetadata(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "input.yaml")
	raw := "dataset:\n  test_cases:\n    - id: one\n      message: first\n      metadata: {category: sql, config: {enabled: true}}\n      expected_outcome: {tools_called: [query], min_components: 0}\n    - id: two\n      user_id: second\n      message: other\n"
	if err := os.WriteFile(src, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := evaluation.Load(src)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "Unnamed Dataset" || d.Cases[0].Username != "test_user" || d.Cases[1].Email != "second@example.com" {
		t.Fatalf("defaults: %#v", d)
	}
	f := d.FilterByMetadata(map[string]any{"category": "sql", "config": map[string]any{"enabled": true}})
	if len(f.Cases) != 1 || f.Cases[0].ID != "one" || len(d.Cases) != 2 {
		t.Fatal(f)
	}
	for _, ext := range []string{".json", ".yml"} {
		path := filepath.Join(dir, "copy"+ext)
		if err := d.Save(path); err != nil {
			t.Fatal(err)
		}
		copy, err := evaluation.Load(path)
		if err != nil || !reflect.DeepEqual(d, copy) {
			t.Fatalf("roundtrip %s: %v %v", ext, copy, err)
		}
	}
}

type lookupTool struct{}

func (lookupTool) Spec() doolla.ToolSpec {
	return doolla.ToolSpec{Name: "lookup", Parameters: map[string]any{"type": "object"}}
}
func (lookupTool) Run(ctx context.Context, u doolla.User, args json.RawMessage) (doolla.ToolResult, error) {
	return doolla.ToolResult{Content: "42"}, nil
}
func TestRunnerObservesRealAgent(t *testing.T) {
	m := &doolla.MockModel{Replies: []doolla.Message{
		{ToolCalls: []doolla.ToolCall{{ID: "call", Type: "function", Function: doolla.FunctionCall{Name: "lookup", Arguments: "{}"}}}, Usage: &doolla.TokenUsage{Input: 7, Output: 3}},
		{Content: "The answer is 42", Usage: &doolla.TokenUsage{Input: 5, Output: 2}},
	}}
	a, err := doolla.NewAgent(m, "", doolla.Registration{Tool: lookupTool{}})
	if err != nil {
		t.Fatal(err)
	}
	d := evaluation.Dataset{Cases: []evaluation.Case{{ID: "example", Message: "What is the answer?", Expected: evaluation.Expected{ToolsCalled: []string{"lookup"}, Contains: []string{"ANSWER IS 42"}}}}}
	r, err := (evaluation.Runner{}).Evaluate(context.Background(), evaluation.Variant{Name: "go", Agent: a, Metadata: map[string]any{"model": "scripted"}}, d)
	if err != nil {
		t.Fatal(err)
	}
	c := r.Results[0]
	if !c.Passed() || r.AverageScore() != 1 || r.PassRate() != 1 || r.TotalTokens() != 17 || c.Run.InputTokens != 12 || c.Run.OutputTokens != 5 || !reflect.DeepEqual(c.Run.Tools, []string{"lookup"}) || c.Run.Answer != "The answer is 42" || len(c.Run.Events) != 4 || r.AverageTime() <= 0 || r.Timestamp.IsZero() || r.Metadata["model"] != "scripted" {
		t.Fatalf("unexpected run %#v", r)
	}
}

type modelFunc func(context.Context, []doolla.Message, []doolla.ToolSpec) (doolla.Message, error)

func (f modelFunc) Reply(ctx context.Context, m []doolla.Message, s []doolla.ToolSpec) (doolla.Message, error) {
	return f(ctx, m, s)
}

func TestRunnerStreamsBeforeDatasetCompletes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	block := make(chan struct{})
	started := make(chan struct{})
	var active, peak atomic.Int32
	m := modelFunc(func(ctx context.Context, messages []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for p := peak.Load(); n > p; p = peak.Load() {
			if peak.CompareAndSwap(p, n) {
				break
			}
		}
		q := messages[len(messages)-1].Content
		switch q {
		case "first":
			close(started)
			select {
			case <-block:
			case <-ctx.Done():
				return doolla.Message{}, ctx.Err()
			}
		case "independent":
			select {
			case <-started:
			case <-ctx.Done():
				return doolla.Message{}, ctx.Err()
			}
		case "followup":
			if len(messages) < 4 || messages[len(messages)-2].Content != "first" {
				return doolla.Message{}, errors.New("lost previous turn")
			}
		}
		return doolla.Message{Content: q}, nil
	})
	a, err := doolla.NewAgent(m, "")
	if err != nil {
		t.Fatal(err)
	}
	d := evaluation.Dataset{Cases: []evaluation.Case{{ID: "one", Message: "first", ConversationID: "repeat"}, {ID: "two", Message: "followup", ConversationID: "repeat"}, {ID: "other", Message: "independent"}}}
	var arrivals []string
	reports, err := (evaluation.Runner{MaxConcurrency: 2}).CompareStreaming(ctx, []evaluation.Variant{{Name: "m", Agent: a}}, d, func(p evaluation.Progress) error {
		arrivals = append(arrivals, p.Result.Case.ID)
		if p.Completed != len(arrivals) || p.Total != 3 {
			t.Errorf("progress %#v", p)
		}
		if p.Result.Case.ID == "other" {
			close(block)
		}
		return nil
	})
	if err != nil || !reflect.DeepEqual(arrivals, []string{"other", "one", "two"}) || peak.Load() != 2 {
		t.Fatalf("progress=%v peak=%d err=%v", arrivals, peak.Load(), err)
	}
	for i, c := range reports[0].Results {
		if !c.Passed() || c.Case.ID != d.Cases[i].ID {
			t.Fatalf("report order/result %#v", c)
		}
	}
}

func TestStreamingCancellationAndErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	entered := make(chan struct{})
	deferDone := make(chan struct{})
	model := modelFunc(func(ctx context.Context, msgs []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
		if msgs[len(msgs)-1].Content == "slow" {
			close(entered)
			defer close(deferDone)
			<-ctx.Done()
			return doolla.Message{}, ctx.Err()
		}
		select {
		case <-entered:
		case <-ctx.Done():
			return doolla.Message{}, ctx.Err()
		}
		return doolla.Message{}, errors.New("fixture model error")
	})
	a, err := doolla.NewAgent(model, "")
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop")
	d := evaluation.Dataset{Cases: []evaluation.Case{{ID: "slow", Message: "slow"}, {ID: "fast", Message: "fast"}}}
	reports, err := (evaluation.Runner{MaxConcurrency: 2}).CompareStreaming(ctx, []evaluation.Variant{{Name: "x", Agent: a}}, d, func(p evaluation.Progress) error {
		if p.Result.Passed() || p.Result.Score() != 0 || !strings.Contains(p.Result.Run.Error, "fixture") {
			t.Errorf("expected useful failure: %#v", p)
		}
		return stop
	})
	if !errors.Is(err, stop) || len(reports[0].Results) != 1 || len(reports[0].Failures()) != 1 {
		t.Fatalf("result %#v err=%v", reports, err)
	}
	select {
	case <-deferDone:
	default:
		t.Fatal("worker still running after cancellation")
	}
}

func TestChecksAndJudgeCompatibility(t *testing.T) {
	ctx := context.Background()
	c := evaluation.Case{Message: "sample", Expected: evaluation.Expected{ToolsCalled: []string{"missing"}, Contains: []string{"text"}, MaxTime: 1}}
	r := evaluation.Run{Duration: 2 * time.Millisecond, Answer: "TEXT", Tokens: 6}
	if x := evaluation.Trajectory(ctx, c, r); x.Passed || x.Score != .5 {
		t.Fatal(x)
	}
	if x := evaluation.Output(ctx, c, r); !x.Passed || x.Score != 1 {
		t.Fatal(x)
	}
	if x := evaluation.Efficiency(time.Millisecond, 5)(ctx, c, r); x.Passed || x.Score > 0.0001 {
		t.Fatal(x)
	}
	for _, reply := range []string{`{"score":0.8,"passed":true,"reasoning":"good"}`, "SCORE: 0.8\nPASSED: yes\nREASONING: good"} {
		check := evaluation.Judge(&doolla.MockModel{Replies: []doolla.Message{{Content: reply}}}, "correctness")(ctx, c, r)
		if !check.Passed || check.Score != .8 || check.Reason != "good" || check.Name != "llm_judge" {
			t.Fatalf("judge: %#v", check)
		}
	}
	x := evaluation.Judge(&doolla.MockModel{Replies: []doolla.Message{{Content: "incorrect format"}}}, "")(ctx, c, r)
	if x.Passed || x.Score != 0 {
		t.Fatal(x)
	}
}

func TestReportFormatsAndWinner(t *testing.T) {
	c := evaluation.Case{ID: "t001", Message: "question, quoted?"}
	reports := []evaluation.Report{
		{Variant: "quick", Results: []evaluation.Result{{Case: c, Run: evaluation.Run{Duration: time.Millisecond, Answer: "<script>unsafe</script>"}, Checks: []evaluation.Check{{Name: "output", Score: .5, Reason: "incorrect"}}}}},
		{Variant: "accurate", Results: []evaluation.Result{{Case: c, Run: evaluation.Run{Duration: time.Second, Tokens: 20}, Checks: []evaluation.Check{{Name: "output", Passed: true, Score: 1}}}}},
	}
	for metric, want := range map[string]string{"score": "accurate", "pass_rate": "accurate", "speed": "quick"} {
		got, err := evaluation.BestVariant(reports, metric)
		if err != nil || got != want {
			t.Fatal(metric, got, err)
		}
	}
	if _, err := evaluation.BestVariant(nil, "score"); err == nil {
		t.Fatal("empty winner")
	}
	if _, err := evaluation.BestVariant(reports, "unknown"); err == nil {
		t.Fatal("unknown metric")
	}
	var buf bytes.Buffer
	if err := evaluation.WriteCSV(&buf, reports); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil || len(rows) != 3 || rows[0][8] != "evaluator_scores" || rows[1][2] != c.Message || rows[2][8] != `{"output":1}` {
		t.Fatal(rows, err)
	}
	if err := evaluation.WriteHTML(&buf, reports); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if strings.Contains(html, "<script>") || !strings.Contains(html, "&lt;script&gt;") || strings.Count(html, "<h2>t001</h2>") != 1 || !strings.Contains(html, `class="best"`) {
		t.Fatal(html)
	}
	buf.Reset()
	if err := evaluation.WriteSummary(&buf, reports); err != nil || !strings.Contains(buf.String(), "incorrect") {
		t.Fatal(err, buf.String())
	}
	if err := evaluation.WriteHTML(io.Discard, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCompareVariantsShareWorkerLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var active, peak atomic.Int32
	ready := make(chan struct{})
	m := modelFunc(func(ctx context.Context, m []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for max := peak.Load(); n > max; max = peak.Load() {
			if peak.CompareAndSwap(max, n) {
				if n == 2 {
					close(ready)
				}
				break
			}
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return doolla.Message{}, ctx.Err()
		}
		return doolla.Message{Content: "done"}, nil
	})
	var variants []evaluation.Variant
	for _, name := range []string{"b", "a", "c"} {
		a, err := doolla.NewAgent(m, "")
		if err != nil {
			t.Fatal(err)
		}
		variants = append(variants, evaluation.Variant{Name: name, Agent: a})
	}
	d := evaluation.Dataset{Cases: []evaluation.Case{{ID: "1", Message: "One"}, {ID: "2", Message: "Two"}}}
	reports, err := (evaluation.Runner{MaxConcurrency: 2}).Compare(ctx, variants, d)
	if err != nil || peak.Load() != 2 || len(reports) != 3 {
		t.Fatal(reports, err)
	}
	for i, r := range reports {
		if r.Variant != variants[i].Name || len(r.Results) != 2 || r.PassRate() != 1 {
			t.Fatal(r)
		}
	}
	got, err := evaluation.BestVariant(reports, "")
	if err != nil || got != "b" {
		t.Fatal(got, err)
	}
}

func TestEfficiencyUsesProvidedPrice(t *testing.T) {
	b := evaluation.EfficiencyBudget{MaxCostUSD: .02, CostUSD: func(r evaluation.Run) (float64, bool) { return float64(r.InputTokens+2*r.OutputTokens) / 1000, true }}
	got := b.Evaluate(context.Background(), evaluation.Case{}, evaluation.Run{InputTokens: 20, OutputTokens: 5})
	if got.Passed || got.Metrics["cost_usd"] != .03 {
		t.Fatal(got)
	}
}
