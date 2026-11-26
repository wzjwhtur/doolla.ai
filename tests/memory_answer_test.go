package tests

import (
	"context"
	"fmt"
	"strings"
	"testing"

	sdk "doolla.ai/sdk"
)

func TestMemoryAnswerRequiresReceipt(t *testing.T) {
	for _, tc := range []struct {
		q, modelReply, want string
		admin               bool
	}{
		{"把质检代码存进长期记忆", "已保存。", "权限", false},
		{"把质检代码存进长期记忆", "已保存。", "未确认", true},
		{"记住的质检代码是什么？", "质检代码是 A。", "质检代码是 A。", false},
		{"记住的质检代码是什么？", "已记住的质检代码是 A。", "已记住的质检代码是 A。", false},
		{"Do you remember the inspection code?", "The code was A.", "The code was A.", false},
		{"处理一下这种口径", "已保存。", "未确认", true},
		{"处理一下这种口径", "我已把口径保存至长期记忆。", "未确认", true},
		{"Handle this convention.", "I've saved it to memory.", "未确认", false},
		{"请导出 CSV 文件", "已保存。", "已保存。", false},
		{"请导出 CSV 文件", "CSV 文件已保存。", "CSV 文件已保存。", false},
	} {
		t.Run(tc.q+"/"+tc.modelReply, func(t *testing.T) {
			store := sdk.NewLocalMemory(10)
			conversations := sdk.NewMemoryConversations()
			a, err := sdk.NewConfiguredAgent(testModel(func(context.Context, []sdk.Message, []sdk.ToolSpec) (sdk.Message, error) {
				return sdk.Message{Content: tc.modelReply}, nil
			}), sdk.AgentConfig{Memory: store, Conversations: conversations, VerifyToolAnswers: true, SystemPrompt: "Use the supplied glossary to answer."}, sdk.MemoryTools(store)...)
			if err != nil {
				t.Fatal(err)
			}
			user := sdk.User{ID: "user", Groups: []string{"user"}}
			if tc.admin {
				user.Groups = []string{"admin"}
			}
			var answer string
			if err := a.Chat(sdk.WithLocale(t.Context(), "zh-CN"), user, "review", tc.q, func(c sdk.Component) error {
				if c.Type == "text" {
					answer = fmt.Sprint(c.Data["content"])
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			history, err := conversations.Load(t.Context(), user, "review")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(answer, tc.want) || len(history) < 2 || history[len(history)-1].Content != answer {
				t.Errorf("visible answer %q, want %q, history %v", answer, tc.want, history)
			}
			items, err := store.Recent(t.Context(), "text", 10)
			if err != nil || len(items) != 0 {
				t.Fatalf("unexpected write: %v, %v", items, err)
			}
		})
	}
}

func TestMemoryReadDoesNotWrite(t *testing.T) {
	store := sdk.NewLocalMemory(10)
	model := testModel(func(context.Context, []sdk.Message, []sdk.ToolSpec) (sdk.Message, error) {
		return sdk.Message{Content: "已保存。", ToolCalls: []sdk.ToolCall{{
			ID: "unexpected", Type: "function", Function: sdk.FunctionCall{Name: "save_text_memory", Arguments: `{"content":"fiction"}`},
		}}}, nil
	})
	a, err := sdk.NewConfiguredAgent(model, sdk.AgentConfig{Memory: store, VerifyToolAnswers: true}, sdk.MemoryTools(store)...)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	err = a.Chat(sdk.WithLocale(t.Context(), "zh-CN"), sdk.User{ID: "admin", Groups: []string{"admin"}}, "lookup", "记住的质检代码是什么？", func(c sdk.Component) error { fmt.Fprint(&text, c.Data); return nil })
	rows, readErr := store.Recent(t.Context(), "text", 10)
	if err != nil || readErr != nil || len(rows) != 0 || strings.Contains(text.String(), "已保存") {
		t.Fatalf("answer=%q rows=%v err=%v/%v", text.String(), rows, err, readErr)
	}
}

func TestMemoryConfirmationAfterIncidentalWrite(t *testing.T) {
	store := sdk.NewLocalMemory(10)
	calls := 0
	model := testModel(func(context.Context, []sdk.Message, []sdk.ToolSpec) (sdk.Message, error) {
		calls++
		if calls == 1 {
			return sdk.Message{Content: "已经写入长期记忆。", ToolCalls: []sdk.ToolCall{{
				ID: "glossary", Type: "function", Function: sdk.FunctionCall{Name: "save_text_memory", Arguments: `{"content":"line uses kilograms"}`},
			}}}, nil
		}
		return sdk.Message{Content: "已保存。"}, nil
	})
	a, err := sdk.NewConfiguredAgent(model, sdk.AgentConfig{Memory: store, VerifyToolAnswers: true}, sdk.MemoryTools(store)...)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	err = a.Chat(sdk.WithLocale(t.Context(), "zh-CN"), sdk.User{ID: "admin", Groups: []string{"admin"}}, "new", "处理这条可复用的术语", func(c sdk.Component) error { fmt.Fprint(&text, c.Data); return nil })
	rows, readErr := store.Recent(t.Context(), "text", 10)
	if err != nil || readErr != nil || len(rows) != 1 || !strings.Contains(text.String(), "已保存。") || strings.Contains(text.String(), "已经写入长期记忆。") {
		t.Fatalf("answer=%q rows=%v err=%v/%v", text.String(), rows, err, readErr)
	}
}

type recordedTextSearch struct {
	sdk.MemoryStore
	matches []sdk.MemoryMatch
	err     error
	queries []sdk.MemoryQuery
}

func (s *recordedTextSearch) Search(_ context.Context, q sdk.MemoryQuery) ([]sdk.MemoryMatch, error) {
	s.queries = append(s.queries, q)
	return s.matches, s.err
}

func TestReadRememberedFactUsesStore(t *testing.T) {
	const text = "原始记录 **质检代码** 是 QZ00123"
	for _, tc := range []struct {
		name       string
		q          string
		subject    string
		matches    []sdk.MemoryMatch
		backendErr error
		want       string
	}{
		{"found", "记住的质检代码是什么？", "质检代码", []sdk.MemoryMatch{{Memory: sdk.MemoryEntry{Kind: "text", Content: text}, Score: .72}}, nil, text},
		{"absent", "记住的质检代码是什么？", "质检代码", nil, nil, "未找到"},
		{"outage", "记住的质检代码是什么？", "质检代码", nil, fmt.Errorf("private backend details"), "检索失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &recordedTextSearch{MemoryStore: sdk.NewLocalMemory(10), matches: tc.matches, err: tc.backendErr}
			model := testModel(func(context.Context, []sdk.Message, []sdk.ToolSpec) (sdk.Message, error) {
				return sdk.Message{Content: "incorrect model guess"}, nil
			})
			cfg := sdk.AgentConfig{Memory: store, VerifyToolAnswers: true, TextMemoryRecall: sdk.MemoryRecall{Limit: 150, Threshold: .70}}
			agent, err := sdk.NewConfiguredAgent(model, cfg, sdk.MemoryTools(store)...)
			if err != nil {
				t.Fatal(err)
			}
			var data map[string]any
			err = agent.Chat(sdk.WithLocale(t.Context(), "zh-CN"), sdk.User{ID: "reader", Groups: []string{"user"}}, "read", tc.q, func(c sdk.Component) error {
				if c.Type == "text" {
					data = c.Data
				}
				return nil
			})
			if err != nil || !strings.Contains(fmt.Sprint(data["content"]), tc.want) || strings.Contains(fmt.Sprint(data), "private backend details") {
				t.Fatalf("unexpected result %v %v", data, err)
			}
			if tc.name == "found" && data["markdown"] != false {
				t.Errorf("raw text as Markdown: %v", data)
			}
			if len(store.queries) != 1 || store.queries[0] != (sdk.MemoryQuery{Kind: "text", Text: tc.subject, Limit: 1, Threshold: .70}) {
				t.Errorf("search %v", store.queries)
			}
		})
	}
}

func TestDirectRecallPreservesHostPolicy(t *testing.T) {
	q := "记住的质检代码是什么？"
	for _, tc := range []string{"prompt override", "message filter", "hidden lookup tool", "custom system prompt", "verification disabled"} {
		t.Run(tc, func(t *testing.T) {
			store := &recordedTextSearch{MemoryStore: sdk.NewLocalMemory(2), matches: []sdk.MemoryMatch{{Memory: sdk.MemoryEntry{Kind: "text", Content: "private stored code"}, Score: 1}}}
			cfg := sdk.AgentConfig{VerifyToolAnswers: true, Memory: store}
			regs := sdk.MemoryTools(store)
			switch tc {
			case "prompt override":
				cfg.DirectTextRecall = true
				cfg.EnhancePrompt = func(context.Context, sdk.Turn, string, string) (string, error) { return "public information only", nil }
			case "message filter":
				cfg.DirectTextRecall = true
				cfg.Filters = []func(context.Context, sdk.Turn, []sdk.Message) ([]sdk.Message, error){func(_ context.Context, _ sdk.Turn, msgs []sdk.Message) ([]sdk.Message, error) { return msgs, nil }}
			case "hidden lookup tool":
				regs = nil
			case "custom system prompt":
				cfg.SystemPrompt = "Use the host redaction policy."
			case "verification disabled":
				cfg.VerifyToolAnswers = false
			}
			calls := 0
			model := testModel(func(context.Context, []sdk.Message, []sdk.ToolSpec) (sdk.Message, error) {
				calls++
				return sdk.Message{Content: "host answer"}, nil
			})
			a, err := sdk.NewConfiguredAgent(model, cfg, regs...)
			if err != nil {
				t.Fatal(err)
			}
			var text string
			err = a.Chat(sdk.WithLocale(t.Context(), "zh-CN"), sdk.User{ID: "reader", Groups: []string{"user"}}, "read", q, func(c sdk.Component) error {
				if c.Type == "text" {
					text = fmt.Sprint(c.Data["content"])
				}
				return nil
			})
			if err != nil || calls != 1 || text != "host answer" {
				t.Fatalf("model calls=%d text=%q err=%v", calls, text, err)
			}
		})
	}
}
