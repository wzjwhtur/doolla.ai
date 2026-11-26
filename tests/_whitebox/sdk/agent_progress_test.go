package doolla

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestConfiguredFeaturesReachToolsAndAudit(t *testing.T) {
	for _, group := range []string{"analyst", "guest"} {
		t.Run(group, func(t *testing.T) {
			const feature = "export_report"
			wantAccess := group == "analyst"
			var checks, invocations, calls int
			cfg := AgentConfig{
				UIFeatures: map[string][]string{feature: {"analyst"}, "public_feature": {}, "tool_names": {"analyst"}},
				Audit: &AuditConfig{LogUIFeatureChecks: true, Sink: func(_ context.Context, e AuditEvent) error {
					switch {
					case e.Type == AuditUIFeatureAccess && e.FeatureName == feature:
						checks++
						if *e.AccessGranted != wantAccess || !slices.Equal(*e.RequiredGroups, []string{"analyst"}) {
							t.Fatalf("feature audit: %+v", e)
						}
					case e.Type == AuditToolInvocation:
						invocations++
						if slices.Contains(*e.UIFeaturesAvailable, feature) != wantAccess {
							t.Fatalf("invocation features: %v", *e.UIFeaturesAvailable)
						}
					}
					return nil
				}},
			}
			tool := functionTool{ToolSpec{Name: "report"}, func(ctx context.Context, _ User, _ json.RawMessage) (ToolResult, error) {
				calls++
				turn, ok := CurrentTurn(ctx)
				if !ok || slices.Contains(turn.UIFeatures, feature) != wantAccess || !slices.Contains(turn.UIFeatures, "public_feature") {
					t.Fatalf("turn: %+v", turn)
				}
				return ToolResult{Content: "ok"}, nil
			}}
			a, err := NewConfiguredAgent(singleToolModel("report"), cfg, Registration{Tool: tool})
			if err != nil {
				t.Fatal(err)
			}
			added := map[string]string{}
			completed := map[string]string{}
			err = a.Chat(t.Context(), User{ID: group, Groups: []string{group}}, "c", "please report", func(c Component) error {
				if c.Type == "task_tracker_update" {
					if c.Data["operation"] == "add_task" {
						task := c.Data["task"].(map[string]any)
						added[task["id"].(string)] = task["title"].(string)
					} else {
						completed[c.Data["task_id"].(string)] = c.Data["status"].(string)
					}
				}
				return nil
			})
			if err != nil || calls != 1 || checks != 1 || invocations != 1 {
				t.Fatalf("chat %v calls %d checks %d invocations %d", err, calls, checks, invocations)
			}
			names := []string{}
			for id, title := range added {
				if completed[id] != "completed" {
					t.Fatalf("unfinished task %s", title)
				}
				names = append(names, title)
			}
			if !slices.Contains(names, "Load conversation context") || slices.Contains(names, "Execute report") != wantAccess {
				t.Fatalf("tasks %v", names)
			}
		})
	}
}

func singleToolModel(name string) Model {
	return replyFunc(func(_ context.Context, messages []Message, _ []ToolSpec) (Message, error) {
		if messages[len(messages)-1].Role == "tool" {
			return Message{Content: "complete"}, nil
		}
		return Message{ToolCalls: []ToolCall{{ID: "call", Type: "function", Function: FunctionCall{Name: name, Arguments: "{}"}}}}, nil
	})
}

func TestToolFailureVisibleInChatByPermission(t *testing.T) {
	const detail = "private SQL Server detail"
	for _, tc := range []struct {
		group, notice string
	}{
		{"user", "run_sql 执行失败。"},
		{"admin", "Tool error: " + detail},
		{"guest", ""},
	} {
		t.Run(tc.group, func(t *testing.T) {
			tool := functionTool{ToolSpec{Name: "run_sql"}, func(context.Context, User, json.RawMessage) (ToolResult, error) {
				return ToolResult{}, errors.New(detail)
			}}
			a, err := NewConfiguredAgent(singleToolModel("run_sql"), AgentConfig{}, Registration{Tool: tool})
			if err != nil {
				t.Fatal(err)
			}
			var notices []string
			var final string
			err = a.Chat(t.Context(), User{ID: tc.group, Groups: []string{tc.group}}, "c", "run query", func(c Component) error {
				if c.Type == "notification" && c.Data["level"] == "error" {
					notices = append(notices, c.Data["message"].(string))
				}
				if c.Type == "text" {
					final, _ = c.Data["content"].(string)
				}
				if payload, _ := json.Marshal(c); tc.group != "admin" && strings.Contains(string(payload), detail) {
					t.Fatal("private tool error was sent to the browser")
				}
				return nil
			})
			var want []string
			if tc.notice != "" {
				want = []string{tc.notice}
			}
			if err != nil || !slices.Equal(notices, want) || final != "complete" {
				t.Fatalf("notices %v, final %q, err %v", notices, final, err)
			}
		})
	}
}

func TestTaskCompletionAfterCheckpoint(t *testing.T) {
	store := NewMemoryConversations()
	tool := functionTool{ToolSpec{Name: "fail"}, func(context.Context, User, json.RawMessage) (ToolResult, error) {
		return ToolResult{}, errors.New("internal tool failure")
	}}
	a, err := NewConfiguredAgent(singleToolModel("fail"), AgentConfig{Conversations: store}, Registration{Tool: tool})
	if err != nil {
		t.Fatal(err)
	}
	u := User{ID: "u", Groups: []string{"user"}}
	err = a.Chat(t.Context(), u, "c", "run tool", func(c Component) error {
		if c.Type == "task_tracker_update" && c.Data["status"] == "error" {
			payload, _ := json.Marshal(c.Data)
			if strings.Contains(string(payload), "internal tool failure") {
				t.Fatal("private error in sidebar")
			}
			return io.ErrClosedPipe
		}
		return nil
	})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("chat: %v", err)
	}
	history, err := store.Load(t.Context(), u, "c")
	if err != nil || len(history) != 3 || history[2].ToolCallID != "call" || !strings.Contains(history[2].Content, "internal tool failure") {
		t.Fatalf("checkpoint: %+v %v", history, err)
	}
}

func TestHTTPToolLimitFeedback(t *testing.T) {
	m := replyFunc(func(context.Context, []Message, []ToolSpec) (Message, error) {
		return Message{ToolCalls: []ToolCall{{ID: "call", Function: FunctionCall{Name: "absent", Arguments: "{}"}}}}, nil
	})
	a, err := NewConfiguredAgent(m, AgentConfig{MaxToolIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	q := "go"
	var sawWarning, sawExplanation, sawInput bool
	err = (&Server{agent: a}).stream(t.Context(), User{ID: "u"}, ChatRequest{ConversationID: "c", RequestID: "r", Message: &q}, func(c Chunk) error {
		d := c.Rich["data"].(map[string]any)
		switch c.Rich["type"] {
		case "status_bar_update":
			sawWarning = sawWarning || d["status"] == "warning"
		case "text":
			sawExplanation = strings.Contains(d["content"].(string), "configured limit (1)")
		case "chat_input_update":
			sawInput = d["disabled"] == false
		}
		return nil
	})
	if err != nil || !sawWarning || !sawInput || !sawExplanation {
		t.Fatalf("warning %v explanation %v input %v err %v", sawWarning, sawExplanation, sawInput, err)
	}
}

func TestConversationWorkflowReplacesAndClearsHistory(t *testing.T) {
	store := FileConversations{Directory: t.TempDir()}
	u := User{ID: "analyst"}
	seed := []Message{{Role: "user", Content: "seed"}}
	if err := store.Save(t.Context(), u, "c", seed); err != nil {
		t.Fatal(err)
	}
	cfg := AgentConfig{Conversations: store, ConversationWorkflow: func(ctx context.Context, turn Turn, history []Message, q string, emit func(Component) error) (WorkflowDecision, error) {
		if len(history) != 1 {
			t.Fatalf("workflow history: %+v", history)
		}
		switch q {
		case "/replace":
			if history[0].Content != "seed" {
				t.Fatalf("seed: %+v", history)
			}
			history[0].Content = "replaced"
		case "/reset":
			if history[0].Content != "replaced" {
				t.Fatalf("replacement: %+v", history)
			}
			history = []Message{}
		}
		return WorkflowDecision{Handled: true, Replacement: history}, emit(textComponent("done"))
	}}
	a, err := NewConfiguredAgent(replyFunc(func(context.Context, []Message, []ToolSpec) (Message, error) {
		t.Fatal("unexpected call to model")
		return Message{}, nil
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"/replace", "/reset"} {
		if err := a.Chat(t.Context(), u, "c", q, func(c Component) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.Load(t.Context(), u, "c")
	if err != nil || len(history) != 0 {
		t.Fatalf("cleared history: %+v %v", history, err)
	}
}

func TestTruncationKeepsPartialTextAndSuppressesToolCall(t *testing.T) {
	db := NewMemoryConversations()
	tool := functionTool{ToolSpec{Name: "report"}, func(context.Context, User, json.RawMessage) (ToolResult, error) {
		t.Fatal("truncated tool call was executed")
		return ToolResult{}, nil
	}}
	model := replyFunc(func(context.Context, []Message, []ToolSpec) (Message, error) {
		return Message{Content: "partial", FinishReason: "length", ModelState: json.RawMessage(`{"partial":true}`), ToolCalls: []ToolCall{{ID: "unsafe", Function: FunctionCall{Name: "report", Arguments: "{}"}}}}, nil
	})
	a, err := NewConfiguredAgent(model, AgentConfig{Conversations: db}, Registration{Tool: tool})
	if err != nil {
		t.Fatal(err)
	}
	u := User{ID: "u"}
	var partial, warning bool
	if err := a.Chat(t.Context(), u, "c", "a report", func(c Component) error {
		switch c.Type {
		case "text":
			partial = c.Data["content"] == "partial"
		case "notification":
			warning = c.Data["finish_reason"] == "length"
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := db.Load(t.Context(), u, "c")
	if !partial || !warning || err != nil || len(saved) != 2 || saved[1].FinishReason != "length" || len(saved[1].ToolCalls) > 0 || len(saved[1].ModelState) > 0 {
		t.Fatalf("partial=%v warning=%v saved=%v err=%v", partial, warning, saved, err)
	}
}
