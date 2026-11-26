package evaluation_test

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/evaluation"
)

type spanKey struct{}
type spanName string

func (spanName) SetAttribute(string, any) {}

type comparisonTracer struct {
	started []string
	ended   []string
	attrs   []map[string]any
}

func (p *comparisonTracer) CreateSpan(ctx context.Context, name string, attrs map[string]any) (context.Context, doolla.ObservabilitySpan) {
	parent, _ := ctx.Value(spanKey{}).(string)
	p.started = append(p.started, parent+"/"+name)
	p.attrs = append(p.attrs, attrs)
	return context.WithValue(ctx, spanKey{}, name), spanName(name)
}

func (p *comparisonTracer) EndSpan(_ context.Context, s doolla.ObservabilitySpan) {
	p.ended = append(p.ended, string(s.(spanName)))
}

func (*comparisonTracer) RecordMetric(context.Context, string, float64, string, map[string]string) {}

func TestEvaluationTraceEnclosesCasesAndReports(t *testing.T) {
	p := &comparisonTracer{}
	ctx := evaluation.WithObservability(t.Context(), p)
	meta := map[string]any{"model": "probe"}
	agent, err := doolla.NewAgent(modelFunc(func(ctx context.Context, _ []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
		if ctx.Value(spanKey{}) != "variant_fast" {
			return doolla.Message{}, fmt.Errorf("model outside variant span")
		}
		return doolla.Message{Content: "答复"}, nil
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	d := evaluation.Dataset{Cases: []evaluation.Case{{ID: "001", Message: "测试", Expected: evaluation.Expected{Contains: []string{"答复"}}}}}
	report, err := (evaluation.Runner{}).Evaluate(ctx, evaluation.Variant{Name: "fast", Agent: agent, Metadata: meta}, d)
	if err != nil || report.PassRate() != 1 {
		t.Fatalf("unexpected evaluation %v %v", report.Results, err)
	}
	if !reflect.DeepEqual(p.started, []string{"/agent_comparison", "agent_comparison/variant_fast"}) || !reflect.DeepEqual(p.ended, []string{"variant_fast", "agent_comparison"}) {
		t.Fatal(p.started, p.ended)
	}
	if p.attrs[0]["num_variants"] != 1 || p.attrs[1]["num_test_cases"] != 1 || p.attrs[1]["model"] != "probe" {
		t.Fatal(p.attrs)
	}
	var rendered, summary strings.Builder
	if err := evaluation.WriteHTML(&rendered, []evaluation.Report{report}); err != nil {
		t.Fatal(err)
	}
	if err := evaluation.WriteSummary(&summary, []evaluation.Report{report}); err != nil {
		t.Fatal(err)
	}
	if ts := report.Timestamp.Format(time.RFC3339Nano); !strings.Contains(html.UnescapeString(rendered.String()), ts) || !strings.Contains(summary.String(), ts) || !strings.Contains(rendered.String(), "Test cases: 1") {
		t.Fatal("comparison metadata missing")
	}
}

func TestEvaluationJSONSyntaxAndPartialJudgeResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dataset.json")
	if err := os.WriteFile(path, []byte("dataset: {name: test}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := evaluation.Load(path); err == nil {
		t.Fatal("accepted YAML-only syntax in a JSON dataset")
	}
	for _, tc := range []struct {
		response string
		score    float64
		passed   bool
	}{
		{"SCORE: 0.2", 0.2, false},
		{"SCORE: invalid\nPASSED: no", 0.5, false},
		{"PASSED: yes", 0.5, true},
	} {
		check := evaluation.Judge(&doolla.MockModel{Replies: []doolla.Message{{Content: tc.response}}}, "correct")(t.Context(), evaluation.Case{}, evaluation.Run{})
		if check.Score != tc.score || check.Passed != tc.passed || check.Reason != tc.response {
			t.Fatalf("%q: %+v", tc.response, check)
		}
	}
}
