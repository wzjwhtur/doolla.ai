package doolla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type geminiCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type geminiPart struct {
	Text         string
	Thought      bool
	FunctionCall *geminiCall
}

func geminiTurns(messages []Message) ([]string, []wireObject, error) {
	var systems []string
	var turns []wireObject
	refs := map[string]geminiCall{}
	var results []wireObject
	flushResults := func() {
		if len(results) > 0 {
			turns = append(turns, wireObject{"role": "user", "parts": results})
			results = nil
		}
	}
	for _, msg := range messages {
		if msg.Role == "tool" {
			call, ok := refs[msg.ToolCallID]
			if !ok {
				return nil, nil, fmt.Errorf("Gemini tool result has unknown call ID %q", msg.ToolCallID)
			}
			// Older datasets often store tool output as a serialized object.
			// Keep its structure and preserve exact integers on the wire.
			var output any = wireObject{"result": msg.Content}
			if raw, err := jsonArgs(msg.Content); err == nil && strings.TrimSpace(msg.Content) != "" {
				output = raw
			}
			response := wireObject{"name": call.Name, "response": output}
			if call.ID != "" {
				response["id"] = call.ID
			}
			results = append(results, wireObject{"functionResponse": response})
			continue
		}
		flushResults()
		switch msg.Role {
		case "system":
			systems = append(systems, msg.Content)
		case "assistant":
			parts := savedParts(msg, "gemini")
			if len(parts) > 0 {
				i := 0
				for _, raw := range parts {
					var part geminiPart
					if err := json.Unmarshal(raw, &part); err != nil {
						return nil, nil, err
					}
					if part.FunctionCall != nil && i < len(msg.ToolCalls) {
						refs[msg.ToolCalls[i].ID] = *part.FunctionCall
						i++
					}
				}
				turns = append(turns, wireObject{"role": "model", "parts": parts})
				continue
			}
			var fallback []wireObject
			if msg.Content != "" {
				fallback = append(fallback, wireObject{"text": msg.Content})
			}
			for _, call := range msg.ToolCalls {
				args, err := jsonArgs(call.Function.Arguments)
				if err != nil {
					return nil, nil, err
				}
				// A history without provider state may contain Python-generated
				// call_<name> IDs. Associate results locally; only replay IDs
				// recorded in Gemini's original parts on the wire.
				fc := geminiCall{Name: call.Function.Name, Args: args}
				refs[call.ID] = fc
				fallback = append(fallback, wireObject{"functionCall": fc})
			}
			turns = append(turns, wireObject{"role": "model", "parts": fallback})
		default:
			turns = append(turns, wireObject{"role": "user", "parts": []wireObject{{"text": msg.Content}}})
		}
	}
	flushResults()
	return systems, turns, nil
}
func (m *remoteModel) gemini(ctx context.Context, messages []Message, tools []ToolSpec, stream bool, onText func(string) error) (Message, error) {
	systems, turns, err := geminiTurns(messages)
	if err != nil {
		return Message{}, err
	}
	payload := wireObject{"contents": turns}
	gen := wireObject{}
	if v := m.options.Temperature; v != nil {
		gen["temperature"] = *v
	}
	for key, value := range m.options.GeminiGenerationConfig {
		gen[key] = value
	}
	if v := m.options.MaxOutputTokens; v > 0 {
		gen["maxOutputTokens"] = v
	}
	if len(gen) > 0 {
		payload["generationConfig"] = gen
	}
	if text := strings.Join(systems, "\n\n"); text != "" {
		payload["systemInstruction"] = wireObject{"parts": []wireObject{{"text": text}}}
	}
	var defs []wireObject
	for _, tool := range tools {
		defs = append(defs, wireObject{"name": tool.Name, "description": tool.Description, "parametersJsonSchema": tool.Parameters})
	}
	if len(defs) > 0 {
		payload["tools"] = []wireObject{{"functionDeclarations": defs}}
	}
	endpoint := m.baseURL + "/models/" + url.PathEscape(m.model)
	if stream {
		endpoint += ":streamGenerateContent?alt=sse"
	} else {
		endpoint += ":generateContent"
	}
	headers := http.Header{}
	headers.Set("x-goog-api-key", m.key)
	body, err := m.post(ctx, endpoint, headers, payload)
	if err != nil {
		return Message{}, err
	}
	defer body.Close()
	var parts []json.RawMessage
	var text strings.Builder
	reply := Message{Role: "assistant"}
	consume := func(data []byte) (bool, error) {
		var result struct {
			UsageMetadata *struct{ PromptTokenCount, CandidatesTokenCount, ThoughtsTokenCount int }
			Candidates    []struct {
				Index        int
				Content      struct{ Parts []json.RawMessage }
				FinishReason string
			}
			PromptFeedback struct{ BlockReason string }
			Error          json.RawMessage
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return false, err
		}
		if u := result.UsageMetadata; u != nil {
			reply.Usage = &TokenUsage{Input: u.PromptTokenCount, Output: u.CandidatesTokenCount + u.ThoughtsTokenCount}
		}
		if hasModelError(result.Error) {
			return false, errors.New(errorDetail(result.Error))
		}
		if result.PromptFeedback.BlockReason != "" {
			return false, fmt.Errorf("Gemini blocked prompt: %s", result.PromptFeedback.BlockReason)
		}
		for _, candidate := range result.Candidates {
			if candidate.Index != 0 {
				continue
			}
			switch candidate.FinishReason {
			case "", "FINISH_REASON_UNSPECIFIED":
			case "STOP", "MAX_TOKENS":
				reply.FinishReason = strings.ToLower(candidate.FinishReason)
			default:
				return false, fmt.Errorf("Gemini stopped: %s", candidate.FinishReason)
			}
			for _, raw := range candidate.Content.Parts {
				parts = append(parts, raw)
				var part geminiPart
				if err := json.Unmarshal(raw, &part); err != nil {
					return false, err
				}
				if fc := part.FunctionCall; fc != nil {
					id := fc.ID
					if id == "" {
						id = newID()
					}
					args, err := jsonArgs(string(fc.Args))
					if err != nil {
						return false, err
					}
					reply.ToolCalls = append(reply.ToolCalls, wireCall(id, fc.Name, string(args)))
				}
				if !part.Thought {
					text.WriteString(part.Text)
					if err := notifyText(onText, part.Text); err != nil {
						return false, err
					}
				}
			}
			if reply.FinishReason != "" {
				return true, nil
			}
		}
		return false, nil
	}
	if stream {
		err = readSSE(body, consume)
	} else {
		var raw json.RawMessage
		if err = json.NewDecoder(body).Decode(&raw); err == nil {
			_, err = consume(raw)
		}
	}
	if err != nil {
		return Message{}, err
	}
	reply.Content = text.String()
	if reply.FinishReason == "max_tokens" {
		// A truncated response can include the beginning of a function call.
		// Keep the generated text, without dispatching or replaying that call.
		reply.ToolCalls = nil
		return reply, nil
	}
	err = saveParts(&reply, "gemini", parts)
	return reply, err
}
