package tests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"doolla.ai/sdk"
)

func TestUnconfirmedToolAnswers(t *testing.T) {
	for _, tc := range []struct{ question, want string }{
		{"请查询 dbo.Pro_Mo 的工单总行数", "未取得查询结果"},
		{"Call run_sql to count orders", "未取得查询结果"},
		{"请记住：质检代码是 AZ991", "权限"},
	} {
		t.Run(tc.question, func(t *testing.T) {
			store := doolla.NewLocalMemory(10)
			conv := doolla.NewMemoryConversations()
			cfg := doolla.AgentConfig{VerifyToolAnswers: true, Conversations: conv}
			model := testModel(func(context.Context, []doolla.Message, []doolla.ToolSpec) (doolla.Message, error) {
				return doolla.Message{Content: "已保存，查询结果为 9999"}, nil
			})
			regs := append(doolla.MemoryTools(store), doolla.Registration{Tool: doolla.SQLTool{}})
			a, err := doolla.NewConfiguredAgent(model, cfg, regs...)
			if err != nil {
				t.Fatal(err)
			}
			user := doolla.User{ID: "guest", Groups: []string{"user"}}
			var visible strings.Builder
			if err := a.Chat(doolla.WithLocale(t.Context(), "zh-CN"), user, "turn", tc.question, func(c doolla.Component) error { fmt.Fprint(&visible, c.Data); return nil }); err != nil {
				t.Fatal(err)
			}
			history, err := conv.Load(t.Context(), user, "turn")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(visible.String(), "9999") || !strings.Contains(visible.String(), tc.want) || len(history) == 0 || !strings.Contains(history[len(history)-1].Content, tc.want) {
				t.Fatalf("unverified reply: %s; history %v", visible.String(), history)
			}
		})
	}
}

type evidenceRunner struct{ err error }

func (r evidenceRunner) RunSQL(context.Context, doolla.User, string) (doolla.SQLResult, error) {
	return doolla.SQLResult{Columns: []string{"total"}, Rows: []map[string]any{{"total": 0}}}, r.err
}

func TestSQLReceiptIsCurrentTurnOnly(t *testing.T) {
	call := doolla.ToolCall{ID: "sql", Type: "function", Function: doolla.FunctionCall{Name: "run_sql", Arguments: `{"sql":"SELECT COUNT(*) AS total FROM orders"}`}}
	for _, test := range []struct {
		name string
		err  error
		want string
	}{{"true zero", nil, "为 0。"}, {"database error", fmt.Errorf("offline"), "未取得查询结果"}} {
		t.Run(test.name, func(t *testing.T) {
			n := 0
			model := testModel(func(_ context.Context, _ []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
				n++
				if n == 1 {
					return doolla.Message{Content: "premature 9999", ToolCalls: []doolla.ToolCall{call}}, nil
				}
				return doolla.Message{Content: "数据库返回 0"}, nil
			})
			a, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{VerifyToolAnswers: true}, doolla.Registration{Tool: doolla.SQLTool{Runner: evidenceRunner{test.err}, DisableArtifacts: true}})
			if err != nil {
				t.Fatal(err)
			}
			for i, want := range []string{test.want, "未取得查询结果"} {
				var text strings.Builder
				q := "请查询工单数"
				if i == 1 {
					q = "那上个月呢？"
				}
				err := a.Chat(doolla.WithLocale(t.Context(), "zh-CN"), doolla.User{ID: "user"}, "same", q, func(c doolla.Component) error { fmt.Fprint(&text, c.Data); return nil })
				if err != nil || !strings.Contains(text.String(), want) || strings.Contains(text.String(), "9999") || (i == 1 && strings.Contains(text.String(), "数据库返回 0")) {
					t.Fatalf("turn %d: %v %s", i, err, text.String())
				}
			}
		})
	}
}

type queryActivityRunner struct {
	query string
	err   error
}

type batchQueryRunner struct{ retried bool }

func (r *batchQueryRunner) RunSQL(_ context.Context, _ doolla.User, statement string) (doolla.SQLResult, error) {
	if statement == "SELECT failed" || (statement == "SELECT retry" && !r.retried) {
		r.retried = true
		return doolla.SQLResult{}, errors.New("database detail")
	}
	result := doolla.SQLResult{Columns: []string{"count"}}
	if statement != "SELECT empty" {
		result.Rows = []map[string]any{{"count": int64(41)}}
	}
	return result, nil
}

func TestVerifiedBatchQueryOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, first, second, status string
		redact                      bool
		want                        []string
	}{
		{"success then failure", "SELECT valid", "SELECT failed", "partial", false, []string{"41", "部分查询"}},
		{"failure then success", "SELECT failed", "SELECT valid", "partial", false, []string{"41", "部分查询"}},
		{"successful empty result", "SELECT empty", "SELECT empty", "succeeded", false, []string{"未返回记录"}},
		{"failed batch", "SELECT failed", "SELECT failed", "failed", false, []string{"未取得查询结果"}},
		{"retry recovers the same statement", "SELECT retry", "SELECT retry", "succeeded", false, []string{"41"}},
		{"completion hook withholds results", "SELECT valid", "SELECT valid", "failed", true, []string{"未取得查询结果"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := testModel(func(_ context.Context, messages []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
				if messages[len(messages)-1].Role == "tool" {
					return doolla.Message{Content: "所有查询都成功，结果为 999。"}, nil
				}
				return doolla.Message{ToolCalls: []doolla.ToolCall{
					{ID: "first", Function: doolla.FunctionCall{Name: "run_sql", Arguments: fmt.Sprintf(`{"sql":%q}`, tc.first)}},
					{ID: "second", Function: doolla.FunctionCall{Name: "run_sql", Arguments: fmt.Sprintf(`{"sql":%q}`, tc.second)}},
				}}, nil
			})
			conf := doolla.AgentConfig{VerifyToolAnswers: true, Hooks: []doolla.Hooks{{
				AfterToolResult: func(_ context.Context, _ doolla.Turn, _ doolla.ToolCall, result doolla.ToolResult) (*doolla.ToolResult, error) {
					if tc.redact {
						return &doolla.ToolResult{Content: "Withheld", Metadata: map[string]any{"row_count": 1}}, nil
					}
					return nil, nil
				},
			}}}
			agent, err := doolla.NewConfiguredAgent(model, conf, doolla.Registration{Tool: doolla.SQLTool{Runner: &batchQueryRunner{}, DisableArtifacts: true}})
			if err != nil {
				t.Fatal(err)
			}
			server := doolla.NewServer(agent, func(http.ResponseWriter, *http.Request) (doolla.User, error) {
				return doolla.User{ID: "viewer"}, nil
			})
			body := strings.NewReader(`{"message":"查询工单数量","locale":"zh-CN","conversation_id":"batch"}`)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/doolla/v2/chat_poll", body))
			var response struct{ Chunks []doolla.Chunk }
			if err := json.Unmarshal(w.Body.Bytes(), &response); w.Code != http.StatusOK || err != nil || len(response.Chunks) < 2 {
				t.Fatalf("poll status %d, error %v: %s", w.Code, err, w.Body)
			}
			last := response.Chunks[len(response.Chunks)-1].Rich
			data, _ := last["data"].(map[string]any)
			if last["type"] != "turn_outcome" || data["status"] != tc.status {
				t.Fatalf("expected %s, got %v", tc.status, last)
			}
			answer, _ := response.Chunks[len(response.Chunks)-2].Simple["text"].(string)
			if strings.Contains(answer, "999") || strings.Contains(answer, "database detail") || (tc.redact && strings.Contains(answer, "41")) {
				t.Fatalf("unexpected answer %q", answer)
			}
			if tc.first == "SELECT retry" && strings.Contains(answer, "部分查询") {
				t.Errorf("successful retry should resolve the earlier failure: %s", answer)
			}
			for _, want := range tc.want {
				if !strings.Contains(answer, want) {
					t.Errorf("answer %q missing %q", answer, want)
				}
			}
		})
	}
}

func TestExplicitVerifiedSQLSkipsOnlyRedundantSummary(t *testing.T) {
	const query = "SELECT COUNT(*) AS total FROM orders"
	for _, tc := range []struct {
		name, question   string
		enabled, rewrite bool
		calls            int
	}{
		{"explicit Chinese", "请只调用一次 run_sql 执行这条只读 SQL：" + query + "; 请直接按实际结果回答。", true, false, 1},
		{"explicit English", "Please only run this read-only SQL: " + query + "; Please return the actual result.", true, false, 1},
		{"SDK default", "Run SQL: " + query, false, false, 2},
		{"generated SQL", "Count the orders", true, false, 2},
		{"additional task", "Run SQL: " + query + "; and explain the result.", true, false, 2},
		{"prepared SQL differs", "Run SQL: " + query, true, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var modelCalls int
			model := testModel(func(context.Context, []doolla.Message, []doolla.ToolSpec) (doolla.Message, error) {
				modelCalls++
				if modelCalls == 1 {
					return doolla.Message{ToolCalls: []doolla.ToolCall{{ID: "read", Function: doolla.FunctionCall{
						Name: "run_sql", Arguments: fmt.Sprintf(`{"sql":%q}`, query),
					}}}}, nil
				}
				return doolla.Message{Content: "The result is 999."}, nil
			})
			cfg := doolla.AgentConfig{VerifyToolAnswers: true, DirectSQLResult: tc.enabled}
			if tc.rewrite {
				cfg.Hooks = []doolla.Hooks{{BeforeTool: func(context.Context, doolla.Turn, doolla.Tool, json.RawMessage) (json.RawMessage, error) {
					return json.RawMessage(`{"sql":"SELECT total FROM visible_orders"}`), nil
				}}}
			}
			runner := &queryActivityRunner{}
			agent, err := doolla.NewConfiguredAgent(model, cfg, doolla.Registration{Tool: doolla.SQLTool{Runner: runner, DisableArtifacts: true}})
			if err != nil {
				t.Fatal(err)
			}
			var answer string
			err = agent.Chat(t.Context(), doolla.User{ID: "viewer"}, "direct", tc.question, func(c doolla.Component) error {
				if c.Type == "text" {
					answer, _ = c.Data["content"].(string)
				}
				return nil
			})
			if err != nil || modelCalls != tc.calls || !strings.Contains(answer, "8") || strings.Contains(answer, "999") {
				t.Fatalf("model calls=%d, SQL=%q, answer=%q, error=%v", modelCalls, runner.query, answer, err)
			}
		})
	}
}

func (r *queryActivityRunner) RunSQL(_ context.Context, _ doolla.User, query string) (doolla.SQLResult, error) {
	r.query = query
	return doolla.SQLResult{Columns: []string{"total"}, Rows: []map[string]any{{"total": 8}}}, r.err
}

func TestSQLActivityShowsPreparedStatementAndResult(t *testing.T) {
	const prepared = "SELECT COUNT(*) AS total FROM authorized_orders"
	for _, tc := range []struct {
		name        string
		groups      []string
		rejected    bool
		offline     bool
		wantSQL     bool
		wantResults bool
	}{
		{name: "allowed", groups: []string{"user"}, wantSQL: true, wantResults: true},
		{name: "restricted", groups: []string{"admin"}, wantResults: true},
		{name: "database error", groups: []string{"user"}, offline: true, wantSQL: true},
		{name: "blocked before execution", groups: []string{"user"}, rejected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &queryActivityRunner{}
			if tc.offline {
				runner.err = errors.New("database unavailable")
			}
			calls := 0
			model := testModel(func(context.Context, []doolla.Message, []doolla.ToolSpec) (doolla.Message, error) {
				calls++
				if calls == 1 {
					return doolla.Message{ToolCalls: []doolla.ToolCall{{ID: "sql", Function: doolla.FunctionCall{
						Name: "run_sql", Arguments: `{"sql":"SELECT COUNT(*) AS total FROM raw_orders"}`,
					}}}}, nil
				}
				return doolla.Message{Content: "查询已结束。"}, nil
			})
			config := doolla.AgentConfig{
				UIFeatures: map[string][]string{"sql_query": tc.groups},
				Hooks: []doolla.Hooks{{BeforeTool: func(context.Context, doolla.Turn, doolla.Tool, json.RawMessage) (json.RawMessage, error) {
					if tc.rejected {
						return nil, errors.New("statement denied")
					}
					return json.Marshal(map[string]string{"sql": prepared})
				}}},
			}
			tool := &doolla.SQLTool{Runner: runner, DisableArtifacts: true}
			agent, err := doolla.NewConfiguredAgent(model, config, doolla.Registration{Tool: tool})
			if err != nil {
				t.Fatal(err)
			}
			var statement, result bool
			err = agent.Chat(doolla.WithLocale(t.Context(), "zh-CN"), doolla.User{ID: "viewer", Groups: []string{"user"}}, "sql-activity", "查询工单数量", func(c doolla.Component) error {
				switch c.Type {
				case "sql_statement":
					statement = true
					if c.Data["content"] != prepared || c.Data["chat_section"] != "activity" || runner.query != "" {
						t.Fatalf("statement must be published before execution with prepared SQL: %+v", c.Data)
					}
				case "dataframe":
					result = true
					if c.Data["chat_section"] != "activity" || runner.query != prepared {
						t.Fatalf("unexpected query result: %+v", c.Data)
					}
				}
				return nil
			})
			if err != nil || statement != tc.wantSQL || result != tc.wantResults || (runner.query == "") != tc.rejected {
				t.Fatalf("SQL shown=%t, result shown=%t, executed=%t, error=%v", statement, result, runner.query != "", err)
			}
		})
	}
}

type failingSave struct{ doolla.MemoryStore }

func (f failingSave) Save(context.Context, doolla.MemoryEntry) error {
	return fmt.Errorf("write unavailable")
}

func TestMemoryReceiptRequiresSuccessfulWrite(t *testing.T) {
	for _, fail := range []bool{false, true} {
		store := doolla.NewLocalMemory(8)
		var backend doolla.MemoryStore = store
		if fail {
			backend = failingSave{store}
		}
		model := testModel(func(context.Context, []doolla.Message, []doolla.ToolSpec) (doolla.Message, error) {
			t.Fatal("verbatim save should not need model-generated arguments")
			return doolla.Message{}, nil
		})
		a, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{VerifyToolAnswers: true}, doolla.MemoryTools(backend)...)
		if err != nil {
			t.Fatal(err)
		}
		var answer string
		if err = a.Chat(doolla.WithLocale(t.Context(), "zh-CN"), doolla.User{ID: "owner", Groups: []string{"admin"}}, "save", "请记住：质检代码是 AZ991", func(c doolla.Component) error {
			if c.Type == "text" {
				answer = fmt.Sprint(c.Data["content"])
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		items, err := store.Recent(t.Context(), "text", 10)
		if err != nil {
			t.Fatal(err)
		}
		if fail {
			if len(items) != 0 || !strings.Contains(answer, "未确认") {
				t.Fatalf("failed write: %v %q", items, answer)
			}
		} else if len(items) != 1 || items[0].Content != "质检代码是 AZ991" || answer != "记忆已保存。" {
			t.Fatalf("successful write: %v %q", items, answer)
		}
	}
}

func TestEvidencePolicyAllowsSchemaAndSQLHelp(t *testing.T) {
	for _, q := range []string{"请查询这张表有哪些外键", "Explain how to count the rows in SQL"} {
		model := testModel(func(context.Context, []doolla.Message, []doolla.ToolSpec) (doolla.Message, error) {
			return doolla.Message{Content: "Use the catalog."}, nil
		})
		a, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{VerifyToolAnswers: true}, doolla.Registration{Tool: doolla.SQLTool{}})
		if err != nil {
			t.Fatal(err)
		}
		var answer string
		if err = a.Chat(doolla.WithLocale(t.Context(), "zh-CN"), doolla.User{ID: "user"}, "schema", q, func(c doolla.Component) error {
			if c.Type == "text" {
				answer = fmt.Sprint(c.Data["content"])
			}
			return nil
		}); err != nil || answer != "Use the catalog." {
			t.Fatalf("%s: %q, %v", q, answer, err)
		}
	}
}
