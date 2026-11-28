package tests

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"doolla.ai/sdk"
)

func TestCompatibleToolArguments(t *testing.T) {
	r := new(doolla.ToolRegistry)
	tool := schemaEcho{}
	if err := r.Register(doolla.Registration{Tool: tool}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args, want string
		conflict   bool
	}{
		{`{"sql=":"x","nested":{"sql=":9223372036854775807}}`, `{"nested":{"sql=":9223372036854775807},"sql":"x"}`, false},
		{`{"sql":"x","sql=":"y"}`, "", true},
		{`{"sql":"x","extra=":1}`, `{"sql":"x","extra=":1}`, false},
	} {
		result, err := r.Execute(t.Context(), doolla.User{ID: "test"}, doolla.ToolCall{Function: doolla.FunctionCall{Name: "echo", Arguments: tc.args}})
		if tc.conflict {
			if err == nil || !strings.Contains(err.Error(), "conflicting") {
				t.Fatalf("conflict: %v", err)
			}
			continue
		}
		if err != nil || result.Content != tc.want {
			t.Fatalf("result %q, err %v", result.Content, err)
		}
	}
}

type schemaEcho struct{}

func (schemaEcho) Spec() doolla.ToolSpec {
	return doolla.ToolSpec{Name: "echo", Parameters: map[string]any{"type": "object", "properties": map[string]any{"sql": map[string]string{"type": "string"}}}}
}
func (schemaEcho) Run(_ context.Context, _ doolla.User, args json.RawMessage) (doolla.ToolResult, error) {
	return doolla.ToolResult{Content: string(args)}, nil
}

func TestAzureDeploymentTemperature(t *testing.T) {
	zero := 0.0
	for _, temp := range []*float64{nil, &zero} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			val, ok := body["temperature"]
			if (temp != nil) != ok || (ok && val != 0.0) {
				t.Errorf("temperature=%v, present=%v", val, ok)
			}
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		}))
		model, err := doolla.NewModel(doolla.ModelConfig{Provider: "azure", BaseURL: srv.URL, Model: "mes-prod", APIKey: "test", Temperature: temp})
		if err == nil {
			_, err = model.Reply(t.Context(), []doolla.Message{{Role: "user", Content: "hello"}}, nil)
		}
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}

// The upstream server holds the final fragment until the downstream SSE client
// observes the preview. Passing proves delivery before model completion.
func TestNegotiatedAnswerDeltaOverHTTP(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(fmt.Sprint("verified=", verified), func(t *testing.T) {
			release := make(chan struct{})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
				w.(http.Flusher).Flush()
				if !verified {
					select {
					case <-release:
					case <-ctx.Done():
						return
					}
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer upstream.Close()
			agent, err := doolla.NewConfiguredAgent(doolla.OpenAIModel{Model: "test", BaseURL: upstream.URL}, doolla.AgentConfig{StreamResponses: true, VerifyToolAnswers: verified})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(doolla.NewServer(agent, nil))
			defer server.Close()
			req, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/doolla/v2/chat_sse", strings.NewReader(`{"message":"Say hello","metadata":{"answer_deltas":true}}`))
			if err != nil {
				t.Fatal(err)
			}
			res, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			var preview strings.Builder
			finalCount := 0
			scan := bufio.NewScanner(res.Body)
			for scan.Scan() {
				line, ok := strings.CutPrefix(scan.Text(), "data: ")
				if !ok || !strings.HasPrefix(line, "{") {
					continue
				}
				var chunk struct {
					Rich struct {
						Type string
						Data struct{ Text, Content string }
					}
				}
				if err := json.Unmarshal([]byte(line), &chunk); err != nil {
					t.Fatal(err)
				}
				switch chunk.Rich.Type {
				case "answer_delta":
					if preview.Len() == 0 {
						close(release)
					}
					preview.WriteString(chunk.Rich.Data.Text)
				case "text":
					if chunk.Rich.Data.Content != "hello world" {
						t.Fatal(chunk.Rich.Data.Content)
					}
					finalCount++
				}
			}
			if err := scan.Err(); err != nil && err != io.EOF {
				t.Fatal(err)
			}
			want := "hello world"
			if verified {
				want = ""
			}
			if preview.String() != want || finalCount != 1 {
				t.Fatalf("preview %q, %d final text events", preview.String(), finalCount)
			}
		})
	}
}

// Exercise the complete compatible-provider -> tool registry -> policy -> tool
// path, including a key split across two streamed function argument fragments.
func TestProviderToolAliasAtDispatch(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Stream   bool
					Messages []doolla.Message
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				last := request.Messages[len(request.Messages)-1]
				if last.Role == "tool" && last.Content != `{"sql":"SELECT 1"}` {
					t.Errorf("unexpected tool receipt: %s", last.Content)
				}
				if request.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					if last.Role == "tool" {
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"accepted\"},\"finish_reason\":\"stop\"}]}\n\n")
					} else {
						for i, fragment := range []string{`{"sql`, `=":"SELECT 1"}`} {
							fn := map[string]any{"arguments": fragment}
							if i == 0 {
								fn["name"] = "echo"
							}
							b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": fn}}}}}})
							fmt.Fprintf(w, "data: %s\n\n", b)
						}
					}
					fmt.Fprint(w, "data: [DONE]\n\n")
				} else {
					message := doolla.Message{Role: "assistant", Content: "accepted"}
					if last.Role != "tool" {
						message.Content = ""
						message.ToolCalls = []doolla.ToolCall{{ID: "c", Type: "function", Function: doolla.FunctionCall{Name: "echo", Arguments: `{"sql=":"SELECT 1"}`}}}
					}
					json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": "stop"}}})
				}
			}))
			defer upstream.Close()
			model := doolla.OpenAIModel{Model: "test", BaseURL: upstream.URL}
			agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{StreamResponses: stream}, doolla.Registration{Tool: schemaEcho{}})
			if err != nil {
				t.Fatal(err)
			}
			var answer string
			err = agent.Chat(t.Context(), doolla.User{ID: "caller"}, "alias", "Run the example", func(c doolla.Component) error {
				if c.Type == "text" {
					answer, _ = c.Data["content"].(string)
				}
				return nil
			})
			if err != nil || answer != "accepted" {
				t.Fatalf("answer=%q err=%v", answer, err)
			}
		})
	}
}
