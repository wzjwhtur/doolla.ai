package doolla

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// roundTrip records actual serialized HTTP without requiring provider credentials.
type modelTestTransport func(*http.Request) (*http.Response, error)

func (f modelTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func modelSSE(events ...string) string                                         { return "data: " + strings.Join(events, "\n\ndata: ") + "\n\n" }

type providerFixture struct {
	provider, endpoint, header, key, reply, stream string
	followup                                       []string
}

func protocolFixtures() []providerFixture {
	chatJSON := `{"usage":{"prompt_tokens":9,"completion_tokens":4},"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"OK","tool_calls":[{"id":"c","type":"function","function":{"name":"lookup","arguments":"{\"id\":1}"}}]}}]}`
	chatStream := modelSSE(
		`{"choices":[{"index":0,"delta":{"content":"O","tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"look","arguments":"{\"id\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"K","tool_calls":[{"index":0,"function":{"name":"up","arguments":"1}"}}]}}]}`,
		`{"choices":[{"index":0,"finish_reason":"tool_calls","delta":{}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4}}`,
		"[DONE]")
	chatReplay := []string{`"tool_call_id":"c"`, `"role":"tool"`, `"content":"42"`, `"arguments":"{\"id\":1}"`}
	responsesJSON := `{"id":"resp-001","usage":{"input_tokens":9,"output_tokens":4},"status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]},{"type":"function_call","name":"lookup","arguments":"{\"id\":1}","call_id":"c"}]}`
	anthropicJSON := `{"stop_reason":"tool_use","usage":{"input_tokens":9,"output_tokens":4},"content":[{"type":"text","text":"OK"},{"type":"tool_use","id":"c","name":"lookup","input":{"id":1}}]}`
	geminiJSON := `{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"},{"functionCall":{"name":"lookup","args":{"id":1}},"thoughtSignature":"opaque"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":3,"thoughtsTokenCount":1}}`
	return []providerFixture{
		{"openai", "/chat/completions", "Authorization", "Bearer secret", chatJSON, chatStream, chatReplay},
		{"azure", "/openai/deployments/demo/chat/completions?api-version=2024-10-21", "api-key", "secret", chatJSON, chatStream, chatReplay},
		{"responses", "/responses", "Authorization", "Bearer secret", responsesJSON, modelSSE(
			`{"type":"response.output_text.delta","delta":"O"}`,
			`{"type":"response.output_text.delta","delta":"K"}`,
			`{"type":"response.completed","response":`+responsesJSON+"}"),
			[]string{`"encrypted_content":"opaque"`, `"type":"function_call_output"`, `"call_id":"c"`, `"output":"42"`}},
		{"anthropic", "/v1/messages", "x-api-key", "secret", anthropicJSON, modelSSE(
			`{"type":"message_start","message":{"usage":{"input_tokens":9,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"c","name":"lookup","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"id\":"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"1}"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`,
			`{"type":"message_stop"}`),
			[]string{`"type":"tool_result"`, `"tool_use_id":"c"`, `"input":{"id":1}`, `"content":"42"`}},
		{"gemini", "/models/demo:generateContent", "x-goog-api-key", "secret", geminiJSON, modelSSE(
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"O"}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"K"},{"functionCall":{"name":"lookup","args":{"id":1}},"thoughtSignature":"opaque"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":3,"thoughtsTokenCount":1}}`),
			[]string{`"functionResponse":{`, `"name":"lookup"`, `"response":{"result":"42"}`, `"thoughtSignature":"opaque"`}},
		{"ollama", "/api/chat", "Authorization", "Bearer secret",
			`{"message":{"role":"assistant","thinking":"reason","content":"OK","tool_calls":[{"function":{"name":"lookup","arguments":"{\"id\":1}"}}]},"done":true,"done_reason":"stop","prompt_eval_count":9,"eval_count":4}`,
			"{\"message\":{\"content\":\"O\",\"thinking\":\"reason\"},\"done\":false}\n" + `{"message":{"content":"K","tool_calls":[{"function":{"name":"lookup","arguments":{"id":1}}}]},"done":true,"done_reason":"stop","prompt_eval_count":9,"eval_count":4}`,
			[]string{`"tool_name":"lookup"`, `"thinking":"reason"`, `"arguments":{"id":1}`, `"content":"42"`}},
	}
}

func TestModelProviderProtocols(t *testing.T) {
	t.Setenv("AZURE_OPENAI_API_VERSION", "2024-10-21")
	for _, fixture := range protocolFixtures() {
		t.Run(fixture.provider, func(t *testing.T) {
			model, err := NewConfiguredModel(fixture.provider, "https://host.test", "demo", "secret")
			if err != nil {
				t.Fatal(err)
			}
			m := model.(*remoteModel)
			spec := ToolSpec{Name: "lookup", Description: "look up an ID", Parameters: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}}}
			for _, stream := range []bool{false, true} {
				var streamed strings.Builder
				var requests []string
				m.client = &http.Client{Transport: modelTestTransport(func(req *http.Request) (*http.Response, error) {
					data, err := io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					requests = append(requests, string(data))
					wantDefault := fixture.provider == "anthropic" || fixture.provider == "gemini" || fixture.provider == "ollama"
					if strings.Contains(string(data), `"temperature":0.7`) != wantDefault {
						t.Errorf("unexpected default temperature for %s: %s", fixture.provider, data)
					}
					endpoint := fixture.endpoint
					first := len(requests) == 1
					if stream && first && fixture.provider == "gemini" {
						endpoint = "/models/demo:streamGenerateContent?alt=sse"
					}
					if req.Method != "POST" || req.URL.RequestURI() != endpoint || req.Header.Get(fixture.header) != fixture.key || req.Header.Get("Content-Type") != "application/json" {
						t.Errorf("incorrect %s request to %s (%s)", req.Method, req.URL.RequestURI(), fixture.header)
					}
					result := fixture.reply
					if stream && first {
						result = fixture.stream
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(result)), Header: http.Header{}}, nil
				})}
				history := []Message{{Role: "system", Content: "Test system prompt"}, {Role: "user", Content: "Query"}}
				var reply Message
				if stream {
					reply, err = m.Stream(context.Background(), history, []ToolSpec{spec}, func(part string) error { streamed.WriteString(part); return nil })
				} else {
					reply, err = m.Reply(context.Background(), history, []ToolSpec{spec})
				}
				if err != nil {
					t.Fatal(err)
				}
				if stream && streamed.String() != "OK" {
					t.Fatalf("streamed %q", streamed.String())
				}
				if reply.Usage == nil || *reply.Usage != (TokenUsage{Input: 9, Output: 4}) {
					t.Fatalf("usage %#v", reply.Usage)
				}
				if fixture.provider == "responses" && string(reply.Metadata) != `{"request_id":"resp-001"}` {
					t.Fatal("missing Responses request metadata", string(reply.Metadata))
				}
				if reply.Content != "OK" || len(reply.ToolCalls) != 1 {
					t.Fatalf("invalid reply: %#v", reply)
				}
				call := reply.ToolCalls[0]
				if call.ID == "" || call.Function.Name != "lookup" || call.Function.Arguments != `{"id":1}` {
					t.Fatalf("invalid call: %#v", call)
				}
				if !strings.Contains(requests[0], "Test system prompt") || !strings.Contains(requests[0], "look up an ID") {
					t.Fatal("missing system prompt or tool description")
				}
				// Cross the persistence boundary before returning the tool result.
				raw, err := json.Marshal(reply)
				if err != nil {
					t.Fatal(err)
				}
				var restored Message
				if err = json.Unmarshal(raw, &restored); err != nil {
					t.Fatal(err)
				}
				expectedReason := map[string]string{"openai": "tool_calls", "azure": "tool_calls", "anthropic": "tool_use", "ollama": "stop", "responses": "completed", "gemini": "stop"}[fixture.provider]
				if restored.FinishReason != expectedReason {
					t.Fatalf("stream=%v finish reason %q, want %q", stream, restored.FinishReason, expectedReason)
				}
				history = append(history, restored, Message{Role: "tool", ToolCallID: call.ID, Content: "42"})
				if _, err = m.Reply(context.Background(), history, []ToolSpec{spec}); err != nil {
					t.Fatal(err)
				}
				for _, token := range fixture.followup {
					if !strings.Contains(requests[1], token) {
						t.Errorf("missing %s in %s", token, requests[1])
					}
				}
				if strings.Contains(requests[1], "model_state") || strings.Contains(requests[1], `"usage":`) || strings.Contains(requests[1], `"finish_reason":`) {
					t.Fatal("leaked internal state field to provider")
				}
			}
		})
	}
}

func TestModelStreamFailure(t *testing.T) {
	stop := errors.New("client stopped")
	for _, f := range protocolFixtures() {
		t.Run(f.provider, func(t *testing.T) {
			model, err := NewConfiguredModel(f.provider, "https://host.test", "demo", "secret")
			if err != nil {
				t.Fatal(err)
			}
			m := model.(*remoteModel)
			stream := f.stream
			m.client = &http.Client{Transport: modelTestTransport(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream))}, nil
			})}
			_, err = m.Stream(context.Background(), nil, nil, func(string) error { return stop })
			if !errors.Is(err, stop) {
				t.Fatalf("callback error %v", err)
			}
			stream = ""
			_, err = m.Stream(context.Background(), nil, nil, nil)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("truncated stream %v", err)
			}
		})
	}
}

func TestModelMockAndConfiguration(t *testing.T) {
	t.Setenv("AZURE_OPENAI_ENDPOINT", "")
	t.Setenv("AZURE_OPENAI_MODEL", "")
	t.Setenv("OLLAMA_MODEL", "")
	for _, p := range []string{"other", "azure", "ollama"} {
		if _, err := NewConfiguredModel(p, "", "", ""); err == nil {
			t.Errorf("accepted %s without configuration", p)
		}
	}
	model, err := NewConfiguredModel("mock", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	m := model.(*MockModel)
	_, err = m.Reply(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var chunks strings.Builder
	response, err := m.Stream(context.Background(), nil, nil, func(s string) error { chunks.WriteString(s); return nil })
	if err != nil || response.Content != chunks.String() || !strings.Contains(response.Content, "Streamed #2") {
		t.Fatalf("mock response %v %v", response, err)
	}
	m.ResetCallCount()
	m.Replies = []Message{{ToolCalls: []ToolCall{wireCall("c", "lookup", "{}")}}, {Content: "result"}}
	call, _ := m.Reply(context.Background(), nil, nil)
	final, _ := m.Reply(context.Background(), nil, nil)
	if len(call.ToolCalls) != 1 || final.Content != "result" || m.CallCount() != 2 {
		t.Fatal("scripted tool workflow")
	}
}

func TestModelGenerationOptions(t *testing.T) {
	temp := 0.4
	for _, f := range protocolFixtures() {
		t.Run(f.provider, func(t *testing.T) {
			options := ModelConfig{Provider: f.provider, BaseURL: "https://example.test", Model: "deployment", APIKey: "key", MaxOutputTokens: 123, Temperature: &temp, AzureAPIVersion: "custom", OllamaContextSize: 9999}
			expected := map[string]string{"openai": `"max_completion_tokens":123`, "responses": `"max_output_tokens":123`, "azure": `"max_completion_tokens":123`, "gemini": `"maxOutputTokens":123`, "anthropic": `"max_tokens":123`, "ollama": `"num_predict":123`}[f.provider]
			options.AzureToken = func(context.Context) (string, error) { return "test-bearer", nil }
			options.HTTPClient = &http.Client{Transport: modelTestTransport(func(r *http.Request) (*http.Response, error) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				body := string(raw)
				if !strings.Contains(body, expected) || !strings.Contains(body, `"temperature":0.4`) {
					t.Errorf("generation settings missing in %s", body)
				}
				if f.provider == "azure" && (r.Header.Get("Authorization") != "Bearer test-bearer" || r.Header.Get("api-key") != "" || r.URL.Query().Get("api-version") != "custom") {
					t.Error("Azure AD credentials or API version")
				}
				if f.provider == "ollama" && !strings.Contains(body, `"num_ctx":9999`) {
					t.Error("missing context size")
				}
				return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`))}, nil
			})}
			m, err := NewModel(options)
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.Reply(context.Background(), nil, nil)
			if err == nil || !strings.Contains(err.Error(), "429: rate limited") {
				t.Errorf("HTTP error %v", err)
			}
			if f.provider == "openai" || f.provider == "azure" {
				options.OpenAIUseLegacyMaxTokens = true
				expected = `"max_tokens":123`
				m, err = NewModel(options)
				if err != nil {
					t.Fatal(err)
				}
				_, err = m.Reply(context.Background(), nil, nil)
				if err == nil || !strings.Contains(err.Error(), "429: rate limited") {
					t.Errorf("HTTP error %v", err)
				}
			}
		})
	}
}

func TestModelParallelToolCalls(t *testing.T) {
	t.Run("interleaved chat deltas", func(t *testing.T) {
		wire := modelSSE(
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"second","function":{"name":"lookup","arguments":"{\"id\":"}},{"index":0,"id":"first","function":{"name":"lookup","arguments":"{\"id\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}},{"index":1,"function":{"arguments":"2}"}}]}}]}`,
			"[DONE]")
		m := OpenAIModel{Model: "demo", BaseURL: "https://example.test", Client: &http.Client{Transport: modelTestTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(wire))}, nil
		})}}
		msg, err := m.Stream(context.Background(), nil, nil, nil)
		if err != nil || len(msg.ToolCalls) != 2 {
			t.Fatalf("calls %v, %v", msg.ToolCalls, err)
		}
		if a, b := msg.ToolCalls[0], msg.ToolCalls[1]; a.ID != "first" || a.Function.Arguments != `{"id":1}` || b.ID != "second" || b.Function.Arguments != `{"id":2}` {
			t.Fatalf("mixed calls %v", msg.ToolCalls)
		}
	})
	t.Run("Gemini mixed provider IDs", func(t *testing.T) {
		raw := `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"functionCall":{"id":"server-id","name":"lookup","args":{"id":1}},"thoughtSignature":"abc"},{"functionCall":{"name":"lookup","args":{"id":2}}}]}}]}`
		m := remoteModel{provider: "gemini", model: "demo", baseURL: "https://example.test", client: &http.Client{Transport: modelTestTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw))}, nil
		})}}
		reply, err := m.Reply(context.Background(), nil, nil)
		if err != nil || len(reply.ToolCalls) != 2 {
			t.Fatalf("%v %v", reply, err)
		}
		if reply.ToolCalls[0].ID != "server-id" || reply.ToolCalls[1].ID == "" || reply.ToolCalls[0].ID == reply.ToolCalls[1].ID {
			t.Fatalf("IDs: %v", reply.ToolCalls)
		}
		_, turns, err := geminiTurns([]Message{reply, {Role: "tool", ToolCallID: reply.ToolCalls[0].ID, Content: "one"}, {Role: "tool", ToolCallID: reply.ToolCalls[1].ID, Content: "two"}})
		if err != nil || len(turns) != 2 {
			t.Fatalf("%v %v", turns, err)
		}
		data, err := json.Marshal(turns[1])
		if err != nil {
			t.Fatal(err)
		}
		expected := `{"parts":[{"functionResponse":{"id":"server-id","name":"lookup","response":{"result":"one"}}},{"functionResponse":{"name":"lookup","response":{"result":"two"}}}],"role":"user"}`
		if string(data) != expected {
			t.Fatalf("unexpected response grouping %s", data)
		}
	})
}

type trackedModelBody struct {
	io.Reader
	closed bool
}

func (r *trackedModelBody) Close() error { r.closed = true; return nil }

func TestModelReleasesBodies(t *testing.T) {
	stop := errors.New("stop")
	for _, f := range protocolFixtures() {
		t.Run(f.provider, func(t *testing.T) {
			for _, scenario := range []string{"callback", "truncated", "http-error", "bad-json"} {
				data := f.stream
				status := 200
				switch scenario {
				case "truncated":
					data = data[:len(data)/2]
				case "http-error":
					data = `{"error":{"message":"rejected"}}`
					status = 400
				case "bad-json":
					data = "{"
				}
				body := &trackedModelBody{Reader: strings.NewReader(data)}
				model, err := NewModel(ModelConfig{Provider: f.provider, Model: "demo", APIKey: "test", BaseURL: "https://test.invalid", HTTPClient: &http.Client{Transport: modelTestTransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: status, Body: body}, nil
				})}})
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "bad-json" {
					_, err = model.Reply(context.Background(), nil, nil)
				} else {
					var cb func(string) error
					if scenario == "callback" {
						cb = func(string) error { return stop }
					}
					_, err = model.(StreamingModel).Stream(context.Background(), nil, nil, cb)
				}
				if err == nil || !body.closed {
					t.Fatalf("%s: err=%v closed=%v", scenario, err, body.closed)
				}
			}
		})
	}
}

func TestModelLegacyGeminiHistory(t *testing.T) {
	msgs := []Message{{Role: "assistant", ToolCalls: []ToolCall{wireCall("call_lookup", "lookup", "{}")}},
		{Role: "tool", ToolCallID: "call_lookup", Content: `{"rows":[{"large_id":989898989898989899}]}`}}
	_, turns, err := geminiTurns(msgs)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(turns)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "call_lookup") || !strings.Contains(string(raw), `"response":{"rows":[{"large_id":989898989898989899}]}`) {
		t.Fatal(string(raw))
	}
}
func TestModelAzureRefreshAndEmptyToolContent(t *testing.T) {
	const reply = `{"error":null,"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"x","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`
	tokenCount := 0
	wireCount := 0
	temp := 0.7
	dynamicErr := errors.New("token refresh failed")
	model, err := NewModel(ModelConfig{Provider: "azure", Model: "my-gpt-5-deployment", BaseURL: "https://example.test", Temperature: &temp, AzureToken: func(ctx context.Context) (string, error) {
		tokenCount++
		if tokenCount == 3 {
			return "", dynamicErr
		}
		return fmt.Sprintf("token-%d", tokenCount), nil
	}, HTTPClient: &http.Client{Transport: modelTestTransport(func(req *http.Request) (*http.Response, error) {
		wireCount++
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if req.Header.Get("Authorization") != fmt.Sprintf("Bearer token-%d", wireCount) || req.Header.Get("api-key") != "" || strings.Contains(string(body), `"temperature"`) {
			t.Error("Azure refresh or reasoning-model parameters")
		}
		if wireCount == 2 && !strings.Contains(string(body), `"content":null`) {
			t.Errorf("missing nullable tool-only assistant turn: %s", body)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := model.Reply(context.Background(), nil, nil)
	if err != nil || len(msg.ToolCalls) != 1 || msg.Content != "" {
		t.Fatal(msg, err)
	}
	_, err = model.Reply(context.Background(), []Message{msg, {Role: "tool", ToolCallID: "x", Content: "result"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = model.Reply(context.Background(), nil, nil); !errors.Is(err, dynamicErr) || wireCount != 2 {
		t.Fatalf("token failure contacted endpoint %d err=%v", wireCount, err)
	}
}

func TestModelInterruptedHTTPStream(t *testing.T) {
	client, server := net.Pipe()
	deadline := time.Now().Add(5 * time.Second)
	if err := client.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := server.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		req, err := http.ReadRequest(bufio.NewReader(server))
		if err == nil {
			_, err = io.Copy(io.Discard, req.Body)
			req.Body.Close()
		}
		if err == nil {
			// A complete tool-call fragment, followed by transport failure with
			// no protocol completion marker. The caller must not run this tool.
			chunk := modelSSE(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"lookup","arguments":"{}"}}]}}]}`)
			_, err = fmt.Fprintf(server, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n%s", len(chunk)+100, chunk)
		}
		serverErr <- err
	}()
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	defer transport.CloseIdleConnections()
	m := OpenAIModel{BaseURL: "http://local.test", Model: "fixture", Client: &http.Client{Transport: transport}}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	msg, err := m.Stream(ctx, nil, nil, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(msg.ToolCalls) != 0 {
		t.Fatalf("interrupted tool stream leaked %v (%v)", msg.ToolCalls, err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestModelToolDiagnostics(t *testing.T) {
	long := strings.Repeat("x", 65)
	tools := []ToolSpec{{Name: long}, {}}
	for name, want := range map[string]int{"openai": 2, "azure": 2, "gemini": 3, "anthropic": 1, "ollama": 3, "mock": 0, "responses": 0} {
		model, err := NewConfiguredModel(name, "https://test.invalid", "instance", "test")
		if err != nil {
			t.Fatal(err)
		}
		if got := ValidateModelTools(model, tools); len(got) != want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestTruncatedModelReplies(t *testing.T) {
	const chat = `{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}]}`
	chatStream := modelSSE(`{"choices":[{"delta":{"content":"partial"}}]}`, `{"choices":[{"delta":{},"finish_reason":"length"}]}`, "[DONE]")
	const gemini = `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[{"text":"partial"},{"functionCall":{"name":"lookup","args":{}}}]}}]}`
	const responses = `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]},{"type":"function_call","name":"lookup","arguments":"{","call_id":"c"}]}`
	withoutReason := strings.Replace(responses, `"incomplete_details":{"reason":"max_output_tokens"},`, "", 1)
	for _, tc := range []struct {
		provider, reply, events, reason string
	}{
		{"openai", chat, chatStream, "length"},
		{"azure", chat, chatStream, "length"},
		{"gemini", gemini, modelSSE(gemini), "max_tokens"},
		{"responses", responses, modelSSE(`{"type":"response.output_text.delta","delta":"partial"}`, `{"type":"response.incomplete","response":`+responses+"}"), "incomplete"},
		{"responses", withoutReason, modelSSE(`{"type":"response.output_text.delta","delta":"partial"}`, `{"type":"response.incomplete","response":`+withoutReason+"}"), "incomplete"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			m, err := NewConfiguredModel(tc.provider, "https://test.invalid", "demo", "unused")
			if err != nil {
				t.Fatal(err)
			}
			model := m.(*remoteModel)
			body := tc.reply
			model.client = &http.Client{Transport: modelTestTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			msg, err := model.Reply(t.Context(), nil, nil)
			if err != nil || msg.Content != "partial" || msg.FinishReason != tc.reason || len(msg.ToolCalls) != 0 || len(msg.ModelState) != 0 {
				t.Fatalf("reply=%+v, err=%v", msg, err)
			}
			body = tc.events
			var seen strings.Builder
			msg, err = model.Stream(t.Context(), nil, nil, func(text string) error { seen.WriteString(text); return nil })
			if err != nil || msg.Content != "partial" || seen.String() != msg.Content || msg.FinishReason != tc.reason || len(msg.ToolCalls) != 0 || len(msg.ModelState) != 0 {
				t.Fatalf("stream=%+v, text=%q, err=%v", msg, seen.String(), err)
			}
		})
	}
}

func TestResponsesFilteredPartialReply(t *testing.T) {
	var r responsesReply
	if err := json.Unmarshal([]byte(`{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[{"type":"message","content":[{"type":"output_text","text":"discarded"}]}]}`), &r); err != nil {
		t.Fatal(err)
	}
	if msg, err := r.message(); err == nil || msg.Content != "" || len(msg.ToolCalls) > 0 {
		t.Fatalf("filtered reply: %+v, err=%v", msg, err)
	}
}

func TestGeminiRejectedCandidate(t *testing.T) {
	const rejected = `{"candidates":[{"finishReason":"SAFETY","content":{"parts":[{"text":"discarded"}]}}]}`
	m, err := NewModel(ModelConfig{Provider: "gemini", BaseURL: "https://test.invalid", Model: "demo", HTTPClient: &http.Client{Transport: modelTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(modelSSE(rejected)))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	var seen strings.Builder
	msg, err := m.(StreamingModel).Stream(t.Context(), nil, nil, func(s string) error { seen.WriteString(s); return nil })
	if err == nil || !strings.Contains(err.Error(), "SAFETY") || seen.Len() != 0 || msg.Content != "" {
		t.Fatalf("msg=%+v, text=%q, err=%v", msg, seen.String(), err)
	}
}

func TestOllamaFlexibleArguments(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`"{\"id\":9007199254740993}"`, `{"id":9007199254740993}`},
		{`"[1,2]"`, `{"args":[1,2]}`},
		{`"formatted incorrectly"`, `{"_raw":"formatted incorrectly"}`},
		{`null`, `{"args":null}`},
		{"", "{}"},
	} {
		got, err := encodedModelArguments(json.RawMessage(tc.raw))
		if err != nil || string(got) != tc.want {
			t.Errorf("arguments %q returned %s, %v (want %s)", tc.raw, got, err, tc.want)
		}
	}
}

func TestChatCompletionsNonObjectToolArguments(t *testing.T) {
	for _, provider := range []string{"openai", "azure"} {
		m, err := NewConfiguredModel(provider, "https://example.test", "demo", "local")
		if err != nil {
			t.Fatal(err)
		}
		for _, stream := range []bool{false, true} {
			var body string
			message := `{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"list","arguments":"[9007199254740993]"}},{"index":1,"function":{"name":"invalid","arguments":"bad"}}]}`
			if stream {
				body = modelSSE(`{"choices":[{"index":0,"delta":`+message+`}]}`, "[DONE]")
			} else {
				body = `{"choices":[{"message":` + message + `}]}`
			}
			model := m.(*remoteModel)
			model.client = &http.Client{Transport: modelTestTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			response, err := model.generate(t.Context(), nil, nil, stream, nil)
			if err != nil || len(response.ToolCalls) != 2 {
				t.Fatal(response, err)
			}
			if response.ToolCalls[0].Function.Arguments != `{"args":[9007199254740993]}` || response.ToolCalls[1].Function.Arguments != `{"_raw":"bad"}` || response.ToolCalls[0].ID == response.ToolCalls[1].ID {
				t.Fatal(response.ToolCalls)
			}
		}
	}
}

func TestResponsesNestedPythonToolCall(t *testing.T) {
	var r responsesReply
	err := json.Unmarshal([]byte(`{"status":"completed","output":[{"type":"reasoning","id":"rs_1"},{"type":"message","content":[{"type":"output_text","text":"working"},{"type":"tool_call","tool_call":{"type":"function","function":{"name":"read","arguments":{"id":9007199254740993}}}}]}]}`), &r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.message()
	if err != nil || got.Content != "working" || len(got.ToolCalls) != 1 {
		t.Fatal(got, err)
	}
	call := got.ToolCalls[0]
	if call.ID == "" || call.Function.Arguments != `{"id":9007199254740993}` {
		t.Fatal(call)
	}
	parts := savedParts(got, "responses")
	if len(parts) != 3 || !strings.Contains(string(parts[0]), "rs_1") || !strings.Contains(string(parts[2]), call.ID) {
		t.Fatal("incorrect replay items", string(got.ModelState))
	}
	r.Status = "incomplete"
	got, err = r.message()
	if err != nil || got.Content != "working" || len(got.ToolCalls) != 0 || len(got.ModelState) != 0 {
		t.Fatal(got, err)
	}
}
