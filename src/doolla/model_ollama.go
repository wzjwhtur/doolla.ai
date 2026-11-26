package doolla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

func (m *remoteModel) ollama(ctx context.Context, messages []Message, tools []ToolSpec, stream bool, onText func(string) error) (Message, error) {
	names := map[string]string{}
	wire := make([]wireObject, 0, len(messages))
	for _, msg := range messages {
		item := wireObject{"role": msg.Role, "content": msg.Content}
		if p := savedParts(msg, "ollama"); len(p) > 0 {
			item["thinking"] = p[0]
		}
		var calls []wireObject
		for index, call := range msg.ToolCalls {
			args, err := jsonArgs(call.Function.Arguments)
			if err != nil {
				return Message{}, err
			}
			calls = append(calls, wireObject{"type": "function", "function": wireObject{"name": call.Function.Name, "index": index, "arguments": args}})
			names[call.ID] = call.Function.Name
		}
		if len(calls) > 0 {
			item["tool_calls"] = calls
		}
		if msg.Role == "tool" {
			name, ok := names[msg.ToolCallID]
			if !ok {
				return Message{}, fmt.Errorf("Ollama tool result has unknown call ID %q", msg.ToolCallID)
			}
			item["tool_name"] = name
		}
		wire = append(wire, item)
	}
	options := wireObject{"num_ctx": 8192, "temperature": 0.7}
	if v := m.options.OllamaContextSize; v > 0 {
		options["num_ctx"] = v
	}
	if v := m.options.Temperature; v != nil {
		options["temperature"] = *v
	}
	if v := m.options.MaxOutputTokens; v > 0 {
		options["num_predict"] = v
	}
	for key, value := range m.options.OllamaOptions {
		options[key] = value
	}
	payload := wireObject{"model": m.model, "messages": wire, "stream": stream, "options": options}
	if len(tools) > 0 {
		payload["tools"] = chatToolSchema(tools)
	}
	body, err := m.post(ctx, m.baseURL+"/api/chat", bearerHeaders(m.key), payload)
	if err != nil {
		return Message{}, err
	}
	defer body.Close()
	reply := Message{Role: "assistant"}
	var text, thinking strings.Builder
	decoder := json.NewDecoder(body)
	for {
		var chunk struct {
			DoneReason string `json:"done_reason"`
			Input      *int   `json:"prompt_eval_count"`
			Output     *int   `json:"eval_count"`
			Message    struct {
				Content, Thinking string
				ToolCalls         []struct {
					ID       string
					Function struct {
						Name      string
						Arguments json.RawMessage
					}
				} `json:"tool_calls"`
			}
			Error string
			Done  bool
		}
		if err := decoder.Decode(&chunk); err != nil {
			if errors.Is(err, io.EOF) {
				return Message{}, io.ErrUnexpectedEOF
			}
			return Message{}, err
		}
		if chunk.Error != "" {
			return Message{}, errors.New(chunk.Error)
		}
		text.WriteString(chunk.Message.Content)
		thinking.WriteString(chunk.Message.Thinking)
		if err := notifyText(onText, chunk.Message.Content); err != nil {
			return Message{}, err
		}
		for _, call := range chunk.Message.ToolCalls {
			if call.Function.Name == "" {
				continue
			}
			id := call.ID
			if id == "" {
				id = newID()
			}
			args, err := encodedModelArguments(call.Function.Arguments)
			if err != nil {
				return Message{}, err
			}
			reply.ToolCalls = append(reply.ToolCalls, wireCall(id, call.Function.Name, string(args)))
		}
		if chunk.Input != nil || chunk.Output != nil {
			if reply.Usage == nil {
				reply.Usage = &TokenUsage{}
			}
			if chunk.Input != nil {
				reply.Usage.Input = *chunk.Input
			}
			if chunk.Output != nil {
				reply.Usage.Output = *chunk.Output
			}
		}
		if chunk.Done || !stream {
			reply.FinishReason = chunk.DoneReason
			if chunk.Done && reply.FinishReason == "" {
				reply.FinishReason = "stop"
			}
			break
		}
	}
	reply.Content = text.String()
	if thinking.Len() > 0 {
		raw, err := json.Marshal(thinking.String())
		if err != nil {
			return Message{}, err
		}
		err = saveParts(&reply, "ollama", []json.RawMessage{raw})
		if err != nil {
			return Message{}, err
		}
	}
	return reply, nil
}
