package doolla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
)

// OpenAIModel uses Chat Completions, including compatible local model servers.
type OpenAIModel struct {
	BaseURL, APIKey, Model string
	Client                 *http.Client
}

func (m OpenAIModel) remote() *remoteModel {
	return &remoteModel{provider: "openai", baseURL: strings.TrimRight(firstValue(m.BaseURL, "https://api.openai.com/v1"), "/"), model: m.Model, key: m.APIKey, client: m.Client}
}
func (m OpenAIModel) Reply(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
	return m.remote().Reply(ctx, messages, tools)
}
func (m OpenAIModel) Stream(ctx context.Context, messages []Message, tools []ToolSpec, onText func(string) error) (Message, error) {
	return m.remote().Stream(ctx, messages, tools, onText)
}

func chatToolSchema(tools []ToolSpec) []wireObject {
	result := make([]wireObject, 0, len(tools))
	for _, tool := range tools {
		result = append(result, wireObject{"type": "function", "function": tool})
	}
	return result
}

func azureReasoningDeployment(model string) bool {
	model = strings.ToLower(model)
	for _, family := range []string{"gpt-5", "o1", "o3-mini"} {
		if strings.Contains(model, family) {
			return true
		}
	}
	return false
}

type chatUsage struct {
	Input  int `json:"prompt_tokens"`
	Output int `json:"completion_tokens"`
}

func (u *chatUsage) counts() *TokenUsage {
	if u == nil {
		return nil
	}
	return &TokenUsage{Input: u.Input, Output: u.Output}
}

func (m *remoteModel) chatCompletion(ctx context.Context, messages []Message, tools []ToolSpec, stream bool, onText func(string) error) (Message, error) {
	wire := make([]wireObject, 0, len(messages))
	for _, msg := range messages {
		item := wireObject{"role": msg.Role, "content": msg.Content}
		if len(msg.ToolCalls) > 0 {
			item["tool_calls"] = msg.ToolCalls
			if msg.Role == "assistant" && msg.Content == "" {
				item["content"] = nil
			}
		}
		if msg.ToolCallID != "" {
			item["tool_call_id"] = msg.ToolCallID
		}
		wire = append(wire, item)
	}
	payload := wireObject{"model": m.model, "messages": wire, "stream": stream}
	if m.options.MaxOutputTokens > 0 {
		key := "max_completion_tokens"
		if m.options.OpenAIUseLegacyMaxTokens {
			key = "max_tokens"
		}
		payload[key] = m.options.MaxOutputTokens
	}
	if m.options.Temperature != nil && (m.provider != "azure" || !azureReasoningDeployment(m.model)) {
		payload["temperature"] = *m.options.Temperature
	}
	if stream {
		payload["stream_options"] = wireObject{"include_usage": true}
	}
	if len(tools) > 0 {
		payload["tools"] = chatToolSchema(tools)
		payload["tool_choice"] = "auto"
	}
	endpoint := m.baseURL + "/chat/completions"
	headers := bearerHeaders(m.key)
	if org := firstValue(m.options.OpenAIOrganization, os.Getenv("OPENAI_ORG")); org != "" && m.provider == "openai" {
		headers.Set("OpenAI-Organization", org)
	}
	if m.provider == "azure" {
		endpoint = m.baseURL + "/openai/deployments/" + url.PathEscape(m.model) + "/chat/completions?api-version=" + url.QueryEscape(m.apiVersion)
		headers = http.Header{}
		if m.options.AzureToken != nil {
			token, err := m.options.AzureToken(ctx)
			if err != nil {
				return Message{}, err
			}
			if token == "" {
				return Message{}, errors.New("Azure token provider returned an empty token")
			}
			headers = bearerHeaders(token)
		} else if m.key != "" {
			headers.Set("api-key", m.key)
		} else {
			headers = bearerHeaders(os.Getenv("AZURE_OPENAI_AD_TOKEN"))
		}
	}
	body, err := m.post(ctx, endpoint, headers, payload)
	if err != nil {
		return Message{}, err
	}
	defer body.Close()
	if !stream {
		var out struct {
			Usage   *chatUsage
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Role, Content, Refusal string
					ToolCalls              []ToolCall `json:"tool_calls"`
				}
			}
			Error json.RawMessage
		}
		if err := json.NewDecoder(body).Decode(&out); err != nil {
			return Message{}, err
		}
		if hasModelError(out.Error) {
			return Message{}, errors.New(errorDetail(out.Error))
		}
		if len(out.Choices) == 0 {
			return Message{}, errors.New("model returned no choices")
		}
		wire := out.Choices[0].Message
		reply := Message{Role: firstValue(wire.Role, "assistant"), Content: firstValue(wire.Content, wire.Refusal), ToolCalls: normalizeChatCalls(wire.ToolCalls), Usage: out.Usage.counts(), FinishReason: out.Choices[0].FinishReason}
		return reply, nil
	}
	var usage *TokenUsage
	var finishReason string
	var answer strings.Builder
	calls := map[int]ToolCall{}
	err = readSSE(body, func(data []byte) (bool, error) {
		if string(data) == "[DONE]" {
			return true, nil
		}
		var chunk struct {
			Usage   *chatUsage
			Error   json.RawMessage
			Choices []struct {
				Index        int
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content   string
					Refusal   string
					ToolCalls []struct {
						Index int `json:"index"`
						ToolCall
					} `json:"tool_calls"`
				}
			}
		}
		if err := json.Unmarshal(data, &chunk); err != nil {
			return false, err
		}
		if hasModelError(chunk.Error) {
			return false, errors.New(errorDetail(chunk.Error))
		}
		if chunk.Usage != nil {
			usage = chunk.Usage.counts()
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
			fragment := firstValue(choice.Delta.Content, choice.Delta.Refusal)
			answer.WriteString(fragment)
			if err := notifyText(onText, fragment); err != nil {
				return false, err
			}
			for _, part := range choice.Delta.ToolCalls {
				if part.Index < 0 {
					return false, fmt.Errorf("invalid tool call index %d", part.Index)
				}
				call := calls[part.Index]
				call.ID += part.ID
				call.Type = "function"
				call.Function.Name += part.Function.Name
				call.Function.Arguments += part.Function.Arguments
				calls[part.Index] = call
			}
		}
		return false, nil
	})
	if err != nil {
		return Message{}, err
	}
	msg := Message{Role: "assistant", Content: answer.String(), Usage: usage, FinishReason: finishReason}
	keys := make([]int, 0, len(calls))
	for index := range calls {
		keys = append(keys, index)
	}
	sort.Ints(keys)
	for _, index := range keys {
		msg.ToolCalls = append(msg.ToolCalls, calls[index])
	}
	msg.ToolCalls = normalizeChatCalls(msg.ToolCalls)
	return msg, nil
}

func normalizeChatCalls(calls []ToolCall) []ToolCall {
	result := calls[:0]
	for _, call := range calls {
		if call.Function.Name == "" {
			continue
		}
		if call.ID == "" {
			call.ID = newID()
		}
		call.Function.Arguments = string(modelArguments(call.Function.Arguments))
		result = append(result, call)
	}
	return result
}
