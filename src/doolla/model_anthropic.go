package doolla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type anthropicBlock struct {
	Type, Text, ID, Name string
	Input                json.RawMessage
}

func (m *remoteModel) anthropic(ctx context.Context, messages []Message, tools []ToolSpec, stream bool, onText func(string) error) (Message, error) {
	var system []string
	var turns []wireObject
	var results []wireObject
	flushResults := func() {
		if len(results) > 0 {
			turns = append(turns, wireObject{"role": "user", "content": results})
			results = nil
		}
	}
	for _, msg := range messages {
		if msg.Role == "tool" {
			results = append(results, wireObject{"type": "tool_result", "tool_use_id": msg.ToolCallID, "content": msg.Content})
			continue
		}
		flushResults()
		if msg.Role == "system" {
			system = append(system, msg.Content)
			continue
		}
		var blocks []wireObject
		if msg.Content != "" {
			blocks = append(blocks, wireObject{"type": "text", "text": msg.Content})
		}
		for _, call := range msg.ToolCalls {
			args, err := jsonArgs(call.Function.Arguments)
			if err != nil {
				return Message{}, err
			}
			blocks = append(blocks, wireObject{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": args})
		}
		turns = append(turns, wireObject{"role": msg.Role, "content": blocks})
	}
	flushResults()
	maxTokens := m.options.MaxOutputTokens
	if maxTokens == 0 {
		maxTokens = 512
	}
	payload := wireObject{"model": m.model, "max_tokens": maxTokens, "messages": turns, "stream": stream}
	if m.options.Temperature != nil {
		payload["temperature"] = *m.options.Temperature
	}
	if text := strings.Join(system, "\n\n"); text != "" {
		payload["system"] = text
	}
	var defs []wireObject
	for _, tool := range tools {
		defs = append(defs, wireObject{"name": tool.Name, "description": tool.Description, "input_schema": tool.Parameters})
	}
	if len(defs) > 0 {
		payload["tools"] = defs
	}
	headers := http.Header{}
	headers.Set("x-api-key", m.key)
	headers.Set("anthropic-version", "2023-06-01")
	base := m.baseURL
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	body, err := m.post(ctx, base+"/messages", headers, payload)
	if err != nil {
		return Message{}, err
	}
	defer body.Close()
	var usage *TokenUsage
	var finishReason string
	var blocks []anthropicBlock
	if !stream {
		var result struct {
			StopReason string `json:"stop_reason"`
			Usage      *apiTokenUsage
			Content    []anthropicBlock
			Error      json.RawMessage
		}
		if err := json.NewDecoder(body).Decode(&result); err != nil {
			return Message{}, err
		}
		if hasModelError(result.Error) {
			return Message{}, errors.New(errorDetail(result.Error))
		}
		blocks = result.Content
		finishReason = result.StopReason
		usage = result.Usage.counts()
	} else {
		partial := map[int]*strings.Builder{}
		err = readSSE(body, func(data []byte) (bool, error) {
			var event struct {
				Message struct{ Usage *apiTokenUsage }
				Usage   *apiTokenUsage
				Type    string
				Index   int
				Block   anthropicBlock `json:"content_block"`
				Delta   struct {
					StopReason  string `json:"stop_reason"`
					Type, Text  string
					PartialJSON string `json:"partial_json"`
				}
				Error json.RawMessage
			}
			if err := json.Unmarshal(data, &event); err != nil {
				return false, err
			}
			switch event.Type {
			case "message_start":
				usage = event.Message.Usage.counts()
			case "message_delta":
				if event.Delta.StopReason != "" {
					finishReason = event.Delta.StopReason
				}
				if event.Usage != nil {
					if usage == nil {
						usage = &TokenUsage{}
					}
					usage.Output = event.Usage.Output
				}
			case "message_stop":
				return true, nil
			case "error":
				return false, errors.New(errorDetail(event.Error))
			case "content_block_start":
				if event.Index != len(blocks) {
					return false, fmt.Errorf("unexpected Anthropic block index %d", event.Index)
				}
				blocks = append(blocks, event.Block)
				if event.Block.Type == "text" {
					return false, notifyText(onText, event.Block.Text)
				}
			case "content_block_delta":
				if event.Index < 0 || event.Index >= len(blocks) {
					return false, errors.New("delta for unknown Anthropic block")
				}
				switch event.Delta.Type {
				case "text_delta":
					blocks[event.Index].Text += event.Delta.Text
					return false, notifyText(onText, event.Delta.Text)
				case "input_json_delta":
					if partial[event.Index] == nil {
						partial[event.Index] = &strings.Builder{}
					}
					partial[event.Index].WriteString(event.Delta.PartialJSON)
				}
			}
			return false, nil
		})
		if err != nil {
			return Message{}, err
		}
		for index, args := range partial {
			blocks[index].Input = json.RawMessage(args.String())
		}
	}
	reply := Message{Role: "assistant", Usage: usage, FinishReason: finishReason}
	var text strings.Builder
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			args, err := jsonArgs(string(block.Input))
			if err != nil {
				return Message{}, err
			}
			reply.ToolCalls = append(reply.ToolCalls, wireCall(block.ID, block.Name, string(args)))
		}
	}
	reply.Content = text.String()
	return reply, nil
}
