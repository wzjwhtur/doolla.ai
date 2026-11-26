package doolla

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Select a Responses endpoint that reports incomplete turns at its output limit.
func TestLiveIncompleteResponses(t *testing.T) {
	name := os.Getenv("DOOLLA_TEST_RESPONSES_PARTIAL_MODEL")
	url := os.Getenv("DOOLLA_TEST_RESPONSES_PARTIAL_URL")
	if name == "" || url == "" {
		t.Skip("set DOOLLA_TEST_RESPONSES_PARTIAL_MODEL and DOOLLA_TEST_RESPONSES_PARTIAL_URL")
	}
	temperature := 0.0
	model, err := NewModel(ModelConfig{Provider: "responses", Model: name, BaseURL: url, APIKey: os.Getenv("DOOLLA_TEST_RESPONSES_PARTIAL_KEY"), MaxOutputTokens: 2, Temperature: &temperature})
	if err != nil {
		t.Fatal(err)
	}
	req := []Message{{Role: "user", Content: "Count to one hundred, spelling out every number."}}
	for _, mode := range []string{"reply", "stream"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			var msg Message
			var err error
			if mode == "stream" {
				var seen strings.Builder
				msg, err = model.(StreamingModel).Stream(ctx, req, nil, func(text string) error { seen.WriteString(text); return nil })
				if err == nil && msg.Content != seen.String() {
					t.Fatalf("stream=%q response=%q", seen.String(), msg.Content)
				}
			} else {
				msg, err = model.Reply(ctx, req, nil)
			}
			if err != nil || msg.Content == "" || msg.FinishReason != "incomplete" || len(msg.ToolCalls) != 0 || len(msg.ModelState) != 0 {
				t.Fatalf("partial=%+v err=%v", msg, err)
			}
			t.Logf("partial=%q finish=%s usage=%+v", msg.Content, msg.FinishReason, msg.Usage)
		})
	}
}

// Set DOOLLA_TEST_MODEL_<PROVIDER> to the model (Azure: deployment). Its
// _BASE_URL and _API_KEY variables can override the normal provider settings.
// Unset providers do not make network requests. Each enabled provider must
// support tool calling for the Doolla Agent, in addition to text generation.
func TestLiveModelProviders(t *testing.T) {
	for _, provider := range []string{"openai", "azureopenai", "responses", "anthropic", "gemini", "ollama"} {
		t.Run(provider, func(t *testing.T) {
			prefix := "DOOLLA_TEST_MODEL_" + strings.ToUpper(provider)
			name := os.Getenv(prefix)
			if name == "" {
				t.Skip("set " + prefix + " to run live inference")
			}
			temperature := 0.0
			cfg := ModelConfig{Provider: provider, Model: name, BaseURL: os.Getenv(prefix + "_BASE_URL"), APIKey: os.Getenv(prefix + "_API_KEY"), Temperature: &temperature}
			cfg.OpenAIUseLegacyMaxTokens = os.Getenv(prefix+"_LEGACY_MAX_TOKENS") == "1"
			m, err := NewModel(cfg)
			if err != nil {
				t.Fatal(err)
			}
			model := m.(StreamingModel)
			for _, mode := range []string{"reply", "stream"} {
				t.Run(mode, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
					defer cancel()
					request := []Message{{Role: "user", Content: "What is 8 + 9? Reply using just the numeral."}}
					var msg Message
					var err error
					var seen strings.Builder
					if mode == "stream" {
						msg, err = model.Stream(ctx, request, nil, func(s string) error { seen.WriteString(s); return nil })
						if err == nil && seen.String() != msg.Content {
							t.Fatalf("stream deltas %q disagree with message %q", seen.String(), msg.Content)
						}
					} else {
						msg, err = model.Reply(ctx, request, nil)
					}
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(msg.Content, "17") || msg.FinishReason == "" {
						t.Fatalf("content=%q reason=%q", msg.Content, msg.FinishReason)
					}
					t.Logf("model=%s finish=%s usage=%+v", name, msg.FinishReason, msg.Usage)
				})
			}
			if provider == "openai" || provider == "azureopenai" || provider == "gemini" {
				t.Run("length_limit", func(t *testing.T) {
					cfg.MaxOutputTokens = 2
					limited, err := NewModel(cfg)
					if err != nil {
						t.Fatal(err)
					}
					want := "length"
					if provider == "gemini" {
						want = "max_tokens"
					}
					for _, stream := range []bool{false, true} {
						ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
						req := []Message{{Role: "user", Content: "Count from 1 to 100 in order, spelling out every number."}}
						var reply Message
						if stream {
							var seen strings.Builder
							reply, err = limited.(StreamingModel).Stream(ctx, req, nil, func(s string) error { seen.WriteString(s); return nil })
							if err == nil && seen.String() != reply.Content {
								t.Errorf("deltas %q, reply %q", seen.String(), reply.Content)
							}
						} else {
							reply, err = limited.Reply(ctx, req, nil)
						}
						cancel()
						if err != nil || reply.FinishReason != want || reply.Content == "" {
							t.Fatalf("stream=%v text=%q finish=%q error=%v", stream, reply.Content, reply.FinishReason, err)
						}
					}
					t.Logf("model=%s preserved truncated answers, finish=%s", name, want)
				})
			}
			for _, mode := range []string{"tool_round_trip", "stream_tool_round_trip"} {
				t.Run(mode, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
					defer cancel()
					spec := ToolSpec{Name: "lookup_order", Description: "Return the status code for an order.", Parameters: objectSchema(map[string]any{"id": strField("The order ID")}, "id")}
					req := []Message{{Role: "user", Content: "Call the lookup_order tool with id A17, then answer with its returned status code."}}
					var call Message
					var err error
					if mode == "stream_tool_round_trip" {
						call, err = model.Stream(ctx, req, []ToolSpec{spec}, nil)
					} else {
						call, err = model.Reply(ctx, req, []ToolSpec{spec})
					}
					if err != nil {
						t.Fatal(err)
					}
					if len(call.ToolCalls) != 1 {
						t.Fatalf("wanted one tool call, got %d", len(call.ToolCalls))
					}
					tool := call.ToolCalls[0]
					var args struct{ ID string }
					if tool.ID == "" || tool.Function.Name != spec.Name || json.Unmarshal([]byte(tool.Function.Arguments), &args) != nil || args.ID != "A17" {
						t.Fatalf("tool=%+v", tool)
					}
					req = append(req, call, Message{Role: "tool", ToolCallID: tool.ID, Content: `{"status":"COBALT42"}`})
					reply, err := model.Reply(ctx, req, nil)
					if err != nil {
						t.Fatal(err)
					}
					if len(reply.ToolCalls) != 0 || !strings.Contains(strings.ToUpper(reply.Content), "COBALT42") {
						t.Fatalf("final answer=%q", reply.Content)
					}
					t.Logf("model=%s tool=%s recovered actual tool result", name, tool.Function.Name)
				})
			}
		})
	}
}
