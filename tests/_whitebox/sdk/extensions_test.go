package doolla

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recallRecorder struct {
	MemoryStore
	query MemoryQuery
}

func (r *recallRecorder) Search(_ context.Context, q MemoryQuery) ([]MemoryMatch, error) {
	r.query = q
	return nil, nil
}

func TestTextAndToolMemoryRecall(t *testing.T) {
	store := &recallRecorder{}
	agent, err := NewConfiguredAgent(&MockModel{}, AgentConfig{Memory: store, TextMemoryRecall: MemoryRecall{Limit: 3, Threshold: 0}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.systemPrompt(t.Context(), Turn{}, "shift", nil); err != nil {
		t.Fatal(err)
	}
	if store.query != (MemoryQuery{Kind: "text", Text: "shift", Limit: 3, Threshold: 0}) {
		t.Fatal(store.query)
	}
	for _, tool := range MemoryToolsWithRecall(store, MemoryRecall{Limit: 7, Threshold: 0.86}) {
		if tool.Tool.Spec().Name != "search_saved_correct_tool_uses" {
			continue
		}
		for _, tc := range []struct {
			args string
			want MemoryQuery
		}{
			{`{"question":"shift"}`, MemoryQuery{Kind: "tool", Text: "shift", Limit: 7, Threshold: 0.86}},
			{`{"question":"shift","tool_name_filter":"run_sql","limit":2,"similarity_threshold":0}`, MemoryQuery{Kind: "tool", Text: "shift", ToolName: "run_sql", Limit: 2, Threshold: 0}},
		} {
			if _, err := tool.Tool.Run(t.Context(), User{}, json.RawMessage(tc.args)); err != nil {
				t.Fatal(err)
			}
			if store.query != tc.want {
				t.Fatal(store.query)
			}
		}
		return
	}
	t.Fatal("missing memory search tool")
}

func TestRegistryAndFileTools(t *testing.T) {
	ctx := t.Context()
	u := User{ID: "u", Groups: []string{"editors"}}
	files := Artifacts{Directory: t.TempDir()}
	reg := ToolRegistry{}
	all := FileTools(files)
	for i := range all {
		all[i].Groups = []string{"editors"}
	}
	if err := reg.Register(all...); err != nil {
		t.Fatal(err)
	}
	run := func(name, args string) (ToolResult, error) {
		return reg.Execute(ctx, u, ToolCall{Function: FunctionCall{Name: name, Arguments: args}})
	}
	if _, err := run("write_file", `{"filename":"n.txt"}`); err == nil {
		t.Fatal("missing content accepted")
	}
	if _, err := run("write_file", `{"filename":"sub/a.txt","content":"one\ntwo\n"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := run("edit_file", `{"filename":"sub/a.txt","edits":[{"start_line":1,"end_line":0,"new_content":"zero\n"},{"start_line":2,"new_content":"three\n"}]}`); err != nil {
		t.Fatal(err)
	}
	res, err := run("read_file", `{"filename":"sub/a.txt"}`)
	if err != nil || res.Content != "zero\none\nthree\n" {
		t.Fatal(res, err)
	}
	if _, err := run("write_file", `{"filename":"../outside.txt","content":"hello"}`); err == nil {
		t.Fatal("invalid path accepted")
	}
	res, err = run("search_files", `{"query":"three"}`)
	if err != nil || !strings.Contains(res.Content, "sub/a.txt") {
		t.Fatal(res, err)
	}
	if _, err := reg.Execute(ctx, User{ID: "other"}, ToolCall{Function: FunctionCall{Name: "read_file", Arguments: `{"filename":"sub/a.txt"}`}}); err == nil {
		t.Fatal("access not checked")
	}
	reg.TransformArgs = func(context.Context, User, Tool, json.RawMessage) (json.RawMessage, error) { return []byte(`{}`), nil }
	if _, err := run("read_file", `{"filename":"sub/a.txt"}`); err == nil {
		t.Fatal("invalid transformed arguments")
	}
	if err := reg.Register(all...); err == nil {
		t.Fatal("duplicate tool accepted")
	}
}
func TestTurnConcurrency(t *testing.T) {
	entered := make(chan bool, 1)
	finish := make(chan struct{})
	history := NewMemoryConversations()
	a, err := NewConfiguredAgent(replyFunc(func(ctx context.Context, m []Message, s []ToolSpec) (Message, error) {
		if len(m) == 2 {
			entered <- true
			select {
			case <-finish:
			case <-ctx.Done():
				return Message{}, ctx.Err()
			}
		}
		return Message{Content: "ok"}, nil
	}), AgentConfig{Conversations: history})
	if err != nil {
		t.Fatal(err)
	}
	emit := func(Component) error { return nil }
	u := User{ID: "u"}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := a.Chat(t.Context(), u, "c", "first", emit); err != nil {
			t.Error(err)
		}
	}()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := a.Chat(ctx, u, "c", "cancelled", emit); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := a.Chat(t.Context(), u, "c", "second", emit); err != nil {
			t.Error(err)
		}
	}()
	close(finish)
	wg.Wait()
	m, err := history.Load(t.Context(), u, "c")
	if err != nil || len(m) != 4 || m[0].Content != "first" || m[2].Content != "second" {
		t.Fatal(m, err)
	}
}
func TestHooksRecoveryAndPythonProcess(t *testing.T) {
	var runs, observed int
	a, err := NewConfiguredAgent(replyFunc(func(ctx context.Context, m []Message, s []ToolSpec) (Message, error) {
		runs++
		if runs == 1 {
			return Message{}, errors.New("temporary")
		}
		if m[len(m)-1].Content != "rewritten" {
			t.Fatal(m)
		}
		return Message{Content: "answer"}, nil
	}), AgentConfig{Hooks: []Hooks{{BeforeMessage: func(ctx context.Context, t Turn, q string) (string, error) { return "rewritten", nil }}},
		Recover: func(ctx context.Context, e Event, n int) (time.Duration, bool) { return 0, n == 1 },
		Observe: func(ctx context.Context, e Event) {
			if e.Operation == "model" {
				observed++
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Chat(t.Context(), User{ID: "u"}, "conv", "input", func(Component) error { return nil }); err != nil || runs != 2 || observed != 2 {
		t.Fatal(err, runs, observed)
	}
	var got []string
	tools := PythonTools(func(ctx context.Context, u User, program string, args []string) (string, error) {
		got = args
		return "ok", nil
	})
	res, err := tools[0].Tool.Run(t.Context(), User{ID: "trusted"}, []byte(`{"filename":"sample.py","arguments":["a b"]}`))
	if err != nil || res.Content != "ok" || len(got) != 3 || got[2] != "a b" {
		t.Fatal(res, err, got)
	}
}
func TestArtifactPythonWorkspaceLayout(t *testing.T) {
	dir := t.TempDir()
	// sha256("abc")[:16], matching LocalFileSystem in Python.
	p := filepath.Join(dir, "ba7816bf8f01cfea")
	if err := os.MkdirAll(p, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "existing.csv"), []byte("total\n7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := (Artifacts{dir}).Read(User{ID: "abc"}, "existing.csv")
	if err != nil || string(v) != "total\n7\n" {
		t.Fatal(string(v), err)
	}
}

func TestWorkflowMemoryActionsAndNullChartValues(t *testing.T) {
	mem := NewLocalMemory(10)
	if err := mem.Save(t.Context(), MemoryEntry{Kind: "text", ID: "fact", Content: "fiscal year"}); err != nil {
		t.Fatal(err)
	}
	agent, err := NewConfiguredAgent(replyFunc(func(context.Context, []Message, []ToolSpec) (Message, error) {
		return Message{}, errors.New("unexpected model call")
	}), AgentConfig{Memory: mem})
	if err != nil {
		t.Fatal(err)
	}
	var action string
	emit := func(c Component) error {
		if c.Type == "card" {
			action = c.Data["actions"].([]map[string]string)[0]["action"]
		}
		return nil
	}
	user := User{ID: "a", Groups: []string{"admin"}}
	if err := agent.Chat(t.Context(), user, "c", "/memories", emit); err != nil || action != "/delete fact" {
		t.Fatal(action, err)
	}
	if err := agent.Chat(t.Context(), user, "c", action, emit); err != nil {
		t.Fatal(err)
	}
	recent, err := mem.Recent(t.Context(), "text", 10)
	if err != nil || len(recent) != 0 {
		t.Fatal(recent, err)
	}
	figure, err := PlotlyFigure([][]string{{"team", "points"}, {"A", "4"}, {"A", ""}, {"B", "3"}}, "By team")
	if err != nil {
		t.Fatal(err)
	}
	trace := figure["data"].([]map[string]any)[0]
	if trace["type"] != "bar" || trace["y"].([]float64)[0] != 4 {
		t.Fatal(trace)
	}
}
