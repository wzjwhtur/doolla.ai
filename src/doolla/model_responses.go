package doolla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

type apiTokenUsage struct {
	Input  int `json:"input_tokens"`
	Output int `json:"output_tokens"`
}

func (u *apiTokenUsage) counts() *TokenUsage {
	if u == nil {
		return nil
	}
	return &TokenUsage{Input: u.Input, Output: u.Output}
}

type responsesReply struct {
	ID                string
	IncompleteDetails struct{ Reason string } `json:"incomplete_details"`
	Usage             *apiTokenUsage
	Status            string
	Output            []json.RawMessage
	Error             json.RawMessage
}

func (r responsesReply) message() (Message, error) {
	partial := r.Status == "incomplete" && (r.IncompleteDetails.Reason == "" || r.IncompleteDetails.Reason == "max_output_tokens")
	if hasModelError(r.Error) || (r.Status != "" && r.Status != "completed" && !partial) {
		return Message{}, fmt.Errorf("Responses API %s: %s", r.Status, errorDetail(r.Error))
	}
	reply := Message{Role: "assistant", Usage: r.Usage.counts(), FinishReason: r.Status}
	if r.ID != "" {
		reply.Metadata, _ = json.Marshal(map[string]string{"request_id": r.ID})
	}
	var text strings.Builder
	replay := make([]json.RawMessage, 0, len(r.Output))
	for _, raw := range r.Output {
		var item struct {
			Type, Name, Arguments string
			CallID                string `json:"call_id"`
			Content               []struct {
				Type, Text, Refusal string
				Call                *struct {
					ID, Type string
					Function struct {
						Name      string
						Arguments json.RawMessage
					}
				} `json:"tool_call"`
			}
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return Message{}, err
		}
		start := len(reply.ToolCalls)
		var messageText strings.Builder
		switch item.Type {
		case "function_call":
			if !partial && item.Name != "" {
				reply.ToolCalls = append(reply.ToolCalls, wireCall(item.CallID, item.Name, string(modelArguments(item.Arguments))))
			}
		case "message":
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					messageText.WriteString(part.Text)
				case "refusal":
					messageText.WriteString(part.Refusal)
				case "tool_call":
					call := part.Call
					if partial || call == nil || call.Type != "function" || call.Function.Name == "" {
						continue
					}
					args, err := encodedModelArguments(call.Function.Arguments)
					if err != nil {
						return Message{}, err
					}
					id := call.ID
					if id == "" {
						id = newID()
					}
					reply.ToolCalls = append(reply.ToolCalls, wireCall(id, call.Function.Name, string(args)))
				}
			}
			text.WriteString(messageText.String())
		}
		if item.Type != "message" || len(reply.ToolCalls) == start {
			replay = append(replay, raw)
			continue
		}
		// Compatible gateways can nest a tool_call inside message content.
		// Replay it with the same call ID as its function_call_output.
		if messageText.Len() != 0 {
			msg, _ := json.Marshal(wireObject{"role": "assistant", "content": messageText.String()})
			replay = append(replay, msg)
		}
		for _, call := range reply.ToolCalls[start:] {
			part, _ := json.Marshal(wireObject{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
			replay = append(replay, part)
		}
	}
	reply.Content = text.String()
	if partial {
		// Only the text is safe to replay. The provider's output items may
		// include a function call with truncated arguments.
		return reply, nil
	}
	err := saveParts(&reply, "responses", replay)
	return reply, err
}
func (m *remoteModel) responses(ctx context.Context, messages []Message, tools []ToolSpec, stream bool, onText func(string) error) (Message, error) {
	var system []string
	input := make([]any, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			system = append(system, msg.Content)
		case "tool":
			input = append(input, wireObject{"type": "function_call_output", "call_id": msg.ToolCallID, "output": msg.Content})
		case "assistant":
			if parts := savedParts(msg, "responses"); len(parts) > 0 {
				for _, part := range parts {
					input = append(input, part)
				}
				continue
			}
			if msg.Content != "" {
				input = append(input, wireObject{"role": "assistant", "content": msg.Content})
			}
			for _, call := range msg.ToolCalls {
				input = append(input, wireObject{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
			}
		default:
			input = append(input, wireObject{"role": msg.Role, "content": msg.Content})
		}
	}
	payload := wireObject{"model": m.model, "input": input, "stream": stream, "store": false, "include": []string{"reasoning.encrypted_content"}}
	if m.options.MaxOutputTokens > 0 {
		payload["max_output_tokens"] = m.options.MaxOutputTokens
	}
	if m.options.Temperature != nil {
		payload["temperature"] = *m.options.Temperature
	}
	if len(system) > 0 {
		payload["instructions"] = strings.Join(system, "\n\n")
	}
	var defs []wireObject
	for _, tool := range tools {
		defs = append(defs, wireObject{"type": "function", "name": tool.Name, "description": tool.Description, "parameters": tool.Parameters, "strict": false})
	}
	if len(defs) > 0 {
		payload["tools"] = defs
	}
	headers := bearerHeaders(m.key)
	if org := firstValue(m.options.OpenAIOrganization, os.Getenv("OPENAI_ORG")); org != "" {
		headers.Set("OpenAI-Organization", org)
	}
	body, err := m.post(ctx, m.baseURL+"/responses", headers, payload)
	if err != nil {
		return Message{}, err
	}
	defer body.Close()
	var result responsesReply
	if !stream {
		if err := json.NewDecoder(body).Decode(&result); err != nil {
			return Message{}, err
		}
		return result.message()
	}
	err = readSSE(body, func(data []byte) (bool, error) {
		var event struct {
			Type, Delta, Message string
			Response             responsesReply
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return false, err
		}
		switch event.Type {
		case "response.output_text.delta", "response.refusal.delta":
			return false, notifyText(onText, event.Delta)
		case "response.completed", "response.incomplete":
			result = event.Response
			if event.Type == "response.incomplete" {
				result.Status = "incomplete"
			}
			return true, nil
		case "response.failed":
			return false, fmt.Errorf("%s: %s", event.Type, errorDetail(event.Response.Error))
		case "error":
			return false, errors.New(firstValue(event.Message, "model stream failed"))
		}
		return false, nil
	})
	if err != nil {
		return Message{}, err
	}
	return result.message()
}
