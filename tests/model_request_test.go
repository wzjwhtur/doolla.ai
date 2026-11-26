package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"doolla.ai/sdk"
)

func TestPerRequestModelOptionsReachHTTP(t *testing.T) {
	type query struct {
		Temperature *float64        `json:"temperature"`
		MaxTokens   int             `json:"max_completion_tokens"`
		Messages    []doolla.Message `json:"messages"`
		Metadata    map[string]any  `json:"metadata"`
	}
	requests := make(chan query, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q query
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		requests <- q
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	initial := 0.9
	model, err := doolla.NewModel(doolla.ModelConfig{Provider: "openai", Model: "test", BaseURL: server.URL, Temperature: &initial, MaxOutputTokens: 41})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	mw := doolla.LLMMiddleware{BeforeRequest: func(_ context.Context, r doolla.ModelRequest) (doolla.ModelRequest, error) {
		if r.User.ID != "u" || r.SystemPrompt == nil || *r.SystemPrompt != "base" || len(r.Messages) != 1 || r.Messages[0].Role != "user" {
			return r, fmt.Errorf("invalid request envelope: %+v", r)
		}
		count++
		r.Metadata["test_value"] = count
		if count == 1 {
			temp, limit, prompt := 0.0, 7, "first turn prompt"
			r.Temperature, r.MaxTokens, r.SystemPrompt = &temp, &limit, &prompt
		}
		return r, nil
	}, AfterResponse: func(_ context.Context, r doolla.ModelRequest, m doolla.Message) (doolla.Message, error) {
		if r.Metadata["test_value"] != count {
			return m, fmt.Errorf("request metadata lost")
		}
		return m, nil
	}}
	cfg := doolla.AgentConfig{LLMMiddlewares: []doolla.LLMMiddleware{mw}, PromptBuilder: func(context.Context, doolla.Turn, []doolla.ToolSpec) (string, error) { return "base", nil }, Middleware: []doolla.Middleware{func(ctx context.Context, r doolla.ModelRequest, next func(context.Context, doolla.ModelRequest) (doolla.Message, error)) (doolla.Message, error) {
		if len(r.Messages) != 2 || r.Messages[0].Role != "system" {
			return doolla.Message{}, fmt.Errorf("legacy middleware lost leading prompt")
		}
		r.Messages[0].Content += " around"
		return next(ctx, r)
	}}}
	agent, err := doolla.NewConfiguredAgent(model, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := agent.Chat(t.Context(), doolla.User{ID: "u"}, fmt.Sprint(i), "ask", func(doolla.Component) error { return nil }); err != nil {
			t.Fatal(err)
		}
		q := <-requests
		temp, max, prompt := 0.0, 7, "first turn prompt around"
		if i == 1 {
			temp, max, prompt = 0.9, 41, "base around"
		}
		if q.Temperature == nil || *q.Temperature != temp || q.MaxTokens != max || len(q.Messages) != 2 || q.Messages[0].Content != prompt || len(q.Metadata) != 0 {
			t.Fatalf("unexpected request %d: %+v", i, q)
		}
	}
	temp, invalid := 2.01, 0
	advanced := model.(doolla.RequestModel)
	for _, r := range []doolla.ModelRequest{{Temperature: &temp}, {MaxTokens: &invalid}} {
		if _, err := advanced.ReplyRequest(t.Context(), r); err == nil {
			t.Fatalf("accepted invalid request %+v", r)
		}
	}
}
