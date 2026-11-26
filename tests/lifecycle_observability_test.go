package tests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"doolla.ai/sdk"
)

type observedSpan struct {
	name  string
	attrs map[string]any
	ended bool
}

func (s *observedSpan) SetAttribute(k string, v any) { s.attrs[k] = v }

type traceContextKey struct{}

type operationRecorder struct {
	spans   []*observedSpan
	metrics map[string][]map[string]string
}

func (r *operationRecorder) CreateSpan(ctx context.Context, name string, attrs map[string]any) (context.Context, doolla.ObservabilitySpan) {
	span := &observedSpan{name: name, attrs: attrs}
	r.spans = append(r.spans, span)
	return context.WithValue(ctx, traceContextKey{}, span), span
}
func (r *operationRecorder) EndSpan(_ context.Context, span doolla.ObservabilitySpan) {
	span.(*observedSpan).ended = true
}
func (r *operationRecorder) RecordMetric(_ context.Context, name string, _ float64, _ string, tags map[string]string) {
	if r.metrics == nil {
		r.metrics = make(map[string][]map[string]string)
	}
	r.metrics[name] = append(r.metrics[name], tags)
}
func (r *operationRecorder) assertEnded(t *testing.T, expected ...string) {
	t.Helper()
	var names []string
	for _, s := range r.spans {
		if !s.ended {
			t.Errorf("unfinished span %q", s.name)
		}
		names = append(names, s.name)
	}
	for _, name := range expected {
		if !slices.Contains(names, name) {
			t.Errorf("missing %s in %v", name, names)
		}
	}
}

func TestAgentLifecycleFullConversationAndTelemetry(t *testing.T) {
	user := doolla.User{ID: "operator"}
	store := doolla.NewMemoryConversations()
	if err := store.Save(t.Context(), user, "thread", []doolla.Message{{Role: "user", Content: "old question"}, {Role: "assistant", Content: "old answer"}}); err != nil {
		t.Fatal(err)
	}
	var rec operationRecorder
	var stages []string
	var completions, toolCalls, modelCalls int
	model := testModel(func(ctx context.Context, msgs []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
		modelCalls++
		span := ctx.Value(traceContextKey{}).(*observedSpan)
		if span.name != "llm.request" || msgs[1].Content != "model-only old question" {
			t.Fatalf("bad model context: %s, %+v", span.name, msgs)
		}
		stages = append(stages, "model")
		if modelCalls == 1 {
			return doolla.Message{ToolCalls: []doolla.ToolCall{{ID: "call", Type: "function", Function: doolla.FunctionCall{Name: "echo", Arguments: "{}"}}}}, nil
		}
		if last := msgs[len(msgs)-1]; last.ToolCallID != "call" || last.Content != "second replacement" {
			t.Fatalf("model saw stale result %+v", last)
		}
		return doolla.Message{Content: "answer"}, nil
	})
	mw := func(name string) doolla.LLMMiddleware {
		return doolla.LLMMiddleware{Name: name,
			BeforeRequest: func(ctx context.Context, req doolla.ModelRequest) (doolla.ModelRequest, error) {
				stages = append(stages, name+" before")
				return req, nil
			},
			AfterResponse: func(ctx context.Context, req doolla.ModelRequest, m doolla.Message) (doolla.Message, error) {
				stages = append(stages, name+" after")
				return m, nil
			},
		}
	}
	hooks := []doolla.Hooks{
		{Name: "first",
			BeforeMessage: func(_ context.Context, _ doolla.Turn, q string) (string, error) { return q + "?", nil },
			BeforeTool: func(_ context.Context, _ doolla.Turn, _ doolla.Tool, args json.RawMessage) (json.RawMessage, error) {
				return args, nil
			},
			AfterTool: func(_ context.Context, _ doolla.Turn, _ doolla.Tool, result doolla.ToolResult) (doolla.ToolResult, error) {
				if result.Content != "{}" {
					t.Fatalf("unexpected original result: %+v", result)
				}
				toolCalls++
				return doolla.ToolResult{Content: "first replacement"}, nil
			},
			AfterMessage: func(_ context.Context, _ doolla.Turn, msg doolla.Message) error {
				if msg.Content != "answer" {
					t.Fatalf("unexpected legacy message: %+v", msg)
				}
				completions++
				return nil
			},
		},
		{Name: "second",
			AfterToolResult: func(_ context.Context, _ doolla.Turn, call doolla.ToolCall, result doolla.ToolResult) (*doolla.ToolResult, error) {
				if call.ID != "call" || result.Content != "first replacement" {
					t.Fatalf("broken hook chain: %+v", result)
				}
				return &doolla.ToolResult{Content: "second replacement", Component: &doolla.Component{Type: "card", Data: map[string]any{"title": "from hook"}}}, nil
			},
			AfterConversation: func(_ context.Context, turn doolla.Turn, c doolla.Conversation) error {
				completions++
				if turn.ConversationID != c.ID || c.User.ID != user.ID || len(c.Messages) != 6 || c.Messages[2].Content != "new question?" || c.Messages[4].ToolCallID != "call" || c.Messages[4].Content != "second replacement" || c.Messages[5].Content != "answer" {
					t.Fatalf("bad completion snapshot: %+v", c)
				}
				stored, err := store.LoadConversation(t.Context(), user, "thread")
				if err != nil || stored == nil || c.Created.IsZero() || c.Updated != stored.Updated || c.Created != stored.Created {
					t.Fatalf("bad timestamps or store data: %+v, %v", stored, err)
				}
				c.Messages[3].ToolCalls[0].ID = "mutated"
				return nil
			},
		},
	}
	agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{Observability: &rec, Conversations: store, Hooks: hooks, LLMMiddlewares: []doolla.LLMMiddleware{mw("one"), mw("two")},
		EnhanceUserMessages: func(_ context.Context, _ doolla.Turn, msgs []doolla.Message) ([]doolla.Message, error) {
			if msgs[0].Role != "user" || msgs[0].Content != "old question" {
				t.Fatalf("unexpected input to enhancer %+v", msgs)
			}
			msgs[0].Content = "model-only old question"
			return msgs, nil
		},
	}, doolla.Registration{Tool: echoTool("echo")})
	if err != nil {
		t.Fatal(err)
	}
	var sawCard bool
	if err := agent.Chat(t.Context(), user, "thread", "new question", func(c doolla.Component) error {
		if c.Type == "card" && c.Data["title"] == "from hook" {
			sawCard = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Load(t.Context(), user, "thread")
	if err != nil || stored[3].ToolCalls[0].ID != "call" || stored[0].Content != "old question" || toolCalls != 1 || completions != 2 || !sawCard {
		t.Fatalf("persisted history or callback mismatch: %v, %v, %d, %d, %t", stored, err, toolCalls, completions, sawCard)
	}
	round := []string{"one before", "two before", "model", "one after", "two after"}
	if !slices.Equal(stages, append(slices.Clone(round), round...)) {
		t.Fatalf("wrong middleware order %v", stages)
	}
	rec.assertEnded(t, "agent.send_message", "agent.conversation.save", "agent.conversation.load", "agent.tool_schemas.fetch", "agent.workflow_handler.try_handle", "agent.system_prompt.build", "agent.llm_context.enhance_user_messages", "agent.tool.execute", "agent.hook.before_tool", "agent.hook.after_tool", "agent.hook.before_message", "agent.hook.after_message", "agent.middleware.before_llm", "agent.middleware.after_llm", "llm.request")
	if len(rec.metrics["agent.hook.duration"]) != 6 || len(rec.metrics["llm.request.duration"]) != 2 || len(rec.metrics["agent.middleware.duration"]) != 8 {
		t.Fatalf("unexpected metrics %+v", rec.metrics)
	}
}

func TestToolFailureCompletionCanReplaceUnavailableResult(t *testing.T) {
	store := doolla.NewMemoryConversations()
	var observed bool
	model := testModel(func(_ context.Context, msgs []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
		if msgs[len(msgs)-1].Role == "user" {
			return doolla.Message{ToolCalls: []doolla.ToolCall{{ID: "missing", Function: doolla.FunctionCall{Name: "missing", Arguments: "{}"}}}}, nil
		}
		if msgs[len(msgs)-1].Content != "fallback" {
			t.Fatalf("model saw %+v", msgs[len(msgs)-1])
		}
		return doolla.Message{Content: "done"}, nil
	})
	user := doolla.User{ID: "u"}
	agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{Conversations: store, DisableAutoSave: true, Hooks: []doolla.Hooks{{AfterToolResult: func(_ context.Context, _ doolla.Turn, _ doolla.ToolCall, r doolla.ToolResult) (*doolla.ToolResult, error) {
		if r.Error == "" {
			t.Fatalf("hook missed failure: %+v", r)
		}
		return &doolla.ToolResult{Content: "fallback"}, nil
	}, AfterConversation: func(_ context.Context, _ doolla.Turn, c doolla.Conversation) error {
		observed = len(c.Messages) == 4 && c.Messages[2].Content == "fallback"
		return nil
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Chat(t.Context(), user, "c", "q", func(doolla.Component) error { return nil }); err != nil {
		t.Fatal(err)
	}
	msgs, err := store.Load(t.Context(), user, "c")
	if err != nil || !observed || len(msgs) != 0 {
		t.Fatalf("autosave or hook mismatch: %v %t %v", msgs, observed, err)
	}
}

func TestTelemetryResolverAndFailedTurn(t *testing.T) {
	var rec operationRecorder
	failure := errors.New("model offline")
	agent, err := doolla.NewConfiguredAgent(testModel(func(context.Context, []doolla.Message, []doolla.ToolSpec) (doolla.Message, error) {
		return doolla.Message{}, failure
	}), doolla.AgentConfig{Observability: &rec})
	if err != nil {
		t.Fatal(err)
	}
	server := doolla.NewServer(agent, func(_ http.ResponseWriter, r *http.Request) (doolla.User, error) {
		if s := r.Context().Value(traceContextKey{}).(*observedSpan); s.name != "agent.user_resolution" {
			t.Errorf("wrong resolver context: %v", s)
		}
		return doolla.User{ID: "caller"}, nil
	})
	req := httptest.NewRequest("POST", "/api/doolla/v2/chat_poll", strings.NewReader(`{"message":"query","conversation_id":"c"}`))
	server.ServeHTTP(httptest.NewRecorder(), req)
	rec.assertEnded(t, "agent.user_resolution", "agent.send_message.error", "llm.request", "agent.send_message")
	if len(rec.metrics["agent.user_resolution.duration"]) != 1 || len(rec.metrics["agent.error.count"]) != 1 {
		t.Fatalf("missing resolver/error metrics %v", rec.metrics)
	}
	for _, s := range rec.spans {
		if s.name == "llm.request" && s.attrs["error"] != failure.Error() {
			t.Fatalf("missing span failure %v", s.attrs)
		}
	}
}

func TestLiveLifecycleWithOllamaAndSQLite(t *testing.T) {
	name, endpoint := os.Getenv("DOOLLA_TEST_AGENT_MODEL"), os.Getenv("DOOLLA_TEST_AGENT_URL")
	if name == "" || endpoint == "" {
		t.Skip("set DOOLLA_TEST_AGENT_MODEL and DOOLLA_TEST_AGENT_URL to a tool-capable Ollama endpoint")
	}
	const tokenLimit = 128
	captured := 0
	clientTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := r.GetBody()
		if err != nil {
			return nil, err
		}
		var payload struct {
			Options struct {
				Temperature *float64
				Predict     int `json:"num_predict"`
			}
		}
		err = json.NewDecoder(body).Decode(&payload)
		body.Close()
		if err != nil {
			return nil, err
		}
		if payload.Options.Temperature == nil || *payload.Options.Temperature != 0 || payload.Options.Predict != tokenLimit {
			return nil, fmt.Errorf("incorrect Ollama request options: %+v", payload.Options)
		}
		captured++
		return http.DefaultTransport.RoundTrip(r)
	})
	temp := 0.9
	model, err := doolla.NewModel(doolla.ModelConfig{Provider: "ollama", BaseURL: endpoint, Model: name, Temperature: &temp, HTTPClient: &http.Client{Transport: clientTransport}})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "test.db")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := doolla.OpenSQLite(file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var rec operationRecorder
	var hookCalls int
	var forwarded, completed bool
	const receipt = "Verified by the lifecycle hook"
	agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{
		PromptBuilder: func(context.Context, doolla.Turn, []doolla.ToolSpec) (string, error) {
			return "Use run_sql for every calculation. Report the number returned by run_sql.", nil
		},
		StreamResponses: true,
		Observability:   &rec,
		LLMMiddlewares: []doolla.LLMMiddleware{{BeforeRequest: func(_ context.Context, req doolla.ModelRequest) (doolla.ModelRequest, error) {
			temp, limit := 0.0, tokenLimit
			req.Temperature, req.MaxTokens = &temp, &limit
			last := req.Messages[len(req.Messages)-1]
			if last.Role == "tool" {
				forwarded = strings.HasPrefix(last.Content, receipt)
			}
			return req, nil
		}}},
		Hooks: []doolla.Hooks{{
			AfterToolResult: func(_ context.Context, _ doolla.Turn, call doolla.ToolCall, result doolla.ToolResult) (*doolla.ToolResult, error) {
				if call.Function.Name != "run_sql" || result.Error != "" || !strings.Contains(result.Content, "30") {
					return nil, fmt.Errorf("SQL returned %q, error %q", result.Content, result.Error)
				}
				hookCalls++
				result.Content = receipt + " " + result.Content
				return &result, nil
			},
			AfterConversation: func(_ context.Context, _ doolla.Turn, c doolla.Conversation) error {
				completed = len(c.Messages) >= 4 && c.Messages[0].Role == "user" && c.Messages[len(c.Messages)-1].Role == "assistant" && strings.Contains(c.Messages[len(c.Messages)-1].Content, "30")
				return nil
			},
		}},
	}, doolla.Registration{Tool: doolla.SQLTool{DB: db, DisableArtifacts: true}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(doolla.NewServer(agent, func(http.ResponseWriter, *http.Request) (doolla.User, error) { return doolla.User{ID: "operator"}, nil }))
	defer server.Close()
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Post(server.URL+"/api/doolla/v2/chat_poll", "application/json", strings.NewReader(`{"message":"Call run_sql to run SELECT 11 + 19 AS total in SQLite, then tell me the total."}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var wire struct{ Chunks []doolla.Chunk }
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		t.Fatal(err)
	}
	var table, answer bool
	for _, c := range wire.Chunks {
		data, _ := c.Rich["data"].(map[string]any)
		switch c.Rich["type"] {
		case "dataframe":
			rows, _ := data["data"].([]any)
			if len(rows) == 1 {
				row, _ := rows[0].(map[string]any)
				table = fmt.Sprint(row["total"]) == "30"
			}
		case "text":
			content, _ := data["content"].(string)
			answer = answer || strings.Contains(content, "30")
		}
	}
	if resp.StatusCode != 200 || captured < 2 || hookCalls < 1 || !table || !forwarded || !completed || !answer {
		t.Fatalf("status=%d hook_calls=%d table=%t forwarded=%t conversation=%t answer=%t", resp.StatusCode, hookCalls, table, forwarded, completed, answer)
	}
	rec.assertEnded(t, "agent.user_resolution", "agent.hook.after_tool", "agent.hook.after_message", "agent.tool.execute", "llm.stream")
	t.Logf("model=%s, SQLite total=30, tool hooks=%d, streaming LLM requests=%d", name, hookCalls, len(rec.metrics["llm.stream.duration"]))
}

func TestToolRoundLimitCompletesAtLastExecutedCall(t *testing.T) {
	var calls int
	model := testModel(func(context.Context, []doolla.Message, []doolla.ToolSpec) (doolla.Message, error) {
		calls++
		return doolla.Message{ToolCalls: []doolla.ToolCall{{ID: "last", Function: doolla.FunctionCall{Name: "echo", Arguments: "{}"}}}}, nil
	})
	var snapshot doolla.Conversation
	rec := &operationRecorder{}
	agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{MaxToolIterations: 1, Observability: rec, Hooks: []doolla.Hooks{{AfterConversation: func(_ context.Context, _ doolla.Turn, c doolla.Conversation) error { snapshot = c; return nil }}}}, doolla.Registration{Tool: echoTool("echo")})
	if err != nil {
		t.Fatal(err)
	}
	err = agent.Chat(t.Context(), doolla.User{ID: "u"}, "c", "go", func(doolla.Component) error { return nil })
	var limit *doolla.ToolIterationLimitError
	if !errors.As(err, &limit) || calls != 1 || len(snapshot.Messages) != 3 || snapshot.Messages[2].ToolCallID != "last" || snapshot.Messages[2].Content != "{}" {
		t.Fatalf("calls=%d, snapshot=%+v, error=%v", calls, snapshot, err)
	}
	rec.assertEnded(t, "agent.send_message", "agent.hook.after_message", "agent.conversation.save")
	if len(rec.metrics["agent.error.count"]) != 0 {
		t.Fatal("normal tool-budget stop reported as runtime failure")
	}
}

func TestStarterRequestDoesNotConsumeMessageQuota(t *testing.T) {
	model := testModel(func(context.Context, []doolla.Message, []doolla.ToolSpec) (doolla.Message, error) {
		return doolla.Message{}, errors.New("starter request reached model")
	})
	var requests int
	agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{Hooks: []doolla.Hooks{{BeforeMessage: func(context.Context, doolla.Turn, string) (string, error) {
		requests++
		return "", errors.New("quota exhausted")
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Chat(t.Context(), doolla.User{ID: "u"}, "c", "  ", func(doolla.Component) error { return nil }); err != nil || requests != 0 {
		t.Fatalf("starter consumed quota: %d %v", requests, err)
	}
}

type failingTool struct{ calls int }

func (*failingTool) Spec() doolla.ToolSpec { return doolla.ToolSpec{Name: "unavailable_source"} }
func (f *failingTool) Run(context.Context, doolla.User, json.RawMessage) (doolla.ToolResult, error) {
	f.calls++
	return doolla.ToolResult{}, errors.New("source down")
}

func TestRecoveryAttemptsAndToolFailure(t *testing.T) {
	for _, useRecovery := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovery=%t", useRecovery), func(t *testing.T) {
			tool := &failingTool{}
			var attempts []int
			cfg := doolla.AgentConfig{}
			if useRecovery {
				cfg.Recover = func(_ context.Context, e doolla.Event, attempt int) (time.Duration, bool) {
					if e.Operation != "tool" || e.Name != tool.Spec().Name || e.Err.Error() != "source down" {
						t.Fatalf("wrong error event: %+v", e)
					}
					attempts = append(attempts, attempt)
					return 0, attempt < 3
				}
			}
			var replies int
			model := testModel(func(_ context.Context, msgs []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
				replies++
				if replies == 1 {
					return doolla.Message{ToolCalls: []doolla.ToolCall{{ID: "call", Function: doolla.FunctionCall{Name: tool.Spec().Name, Arguments: "{}"}}}}, nil
				}
				last := msgs[len(msgs)-1]
				if last.Role != "tool" || !strings.Contains(last.Content, "source down") {
					t.Fatalf("tool failure not relayed %v", last)
				}
				return doolla.Message{Content: "failed tool received"}, nil
			})
			agent, err := doolla.NewConfiguredAgent(model, cfg, doolla.Registration{Tool: tool})
			if err != nil {
				t.Fatal(err)
			}
			if err := agent.Chat(t.Context(), doolla.User{ID: "u"}, "c", "question", func(doolla.Component) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if replies != 2 || (!useRecovery && tool.calls != 1) || (useRecovery && (tool.calls != 3 || !slices.Equal(attempts, []int{1, 2, 3}))) {
				t.Fatalf("model calls=%d tool calls=%d attempts=%v", replies, tool.calls, attempts)
			}
		})
	}
}

func TestModelRetryDelayAndCancellation(t *testing.T) {
	for _, cancelDuringDelay := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelDuringDelay), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls int
			var scheduled time.Time
			const delay = 15 * time.Millisecond
			model := testModel(func(context.Context, []doolla.Message, []doolla.ToolSpec) (doolla.Message, error) {
				calls++
				if calls == 1 {
					return doolla.Message{}, errors.New("temporary provider failure")
				}
				if time.Since(scheduled) < delay {
					t.Fatal("retry ran before the configured delay")
				}
				return doolla.Message{Content: "ok"}, nil
			})
			agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{Recover: func(_ context.Context, e doolla.Event, attempt int) (time.Duration, bool) {
				if e.Operation != "model" || e.Err == nil || attempt != 1 {
					t.Fatalf("invalid recovery event: %+v, %d", e, attempt)
				}
				scheduled = time.Now()
				if cancelDuringDelay {
					cancel()
					return time.Hour, true
				}
				return delay, true
			}})
			if err != nil {
				t.Fatal(err)
			}
			err = agent.Chat(ctx, doolla.User{ID: "u"}, "c", "ask", func(doolla.Component) error { return nil })
			if cancelDuringDelay && (!errors.Is(err, context.Canceled) || calls != 1) || !cancelDuringDelay && (err != nil || calls != 2) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
