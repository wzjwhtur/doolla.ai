package doolla

import (
	"context"
	"errors"
	"maps"
	"math"
)

// ModelRequest is the per-call input to LLMMiddleware and request-aware models.
// Messages contains the conversation. SystemPrompt is supplied separately. The
// original around-call Middleware still receives the system prompt in Messages.
// Nil generation options inherit the model's settings. Metadata is available
// to custom models and middleware; native adapters only transmit protocol fields.
type ModelRequest struct {
	Messages     []Message
	Tools        []ToolSpec
	User         User
	Temperature  *float64
	MaxTokens    *int
	SystemPrompt *string
	Stream       bool
	Metadata     map[string]any
}

// RequestModel accepts the complete, middleware-adjusted request.
type RequestModel interface {
	Model
	ReplyRequest(context.Context, ModelRequest) (Message, error)
}

// StreamingRequestModel supports complete requests on the streaming path.
type StreamingRequestModel interface {
	RequestModel
	StreamRequest(context.Context, ModelRequest, func(string) error) (Message, error)
}

func (r ModelRequest) splitSystem() ModelRequest {
	if len(r.Messages) > 0 && r.Messages[0].Role == "system" {
		if r.SystemPrompt == nil {
			prompt := r.Messages[0].Content
			r.SystemPrompt = &prompt
		}
		r.Messages = r.Messages[1:]
	}
	return r
}
func (r ModelRequest) withSystem() []Message {
	if r.SystemPrompt == nil {
		return r.Messages
	}
	return append([]Message{{Role: "system", Content: *r.SystemPrompt}}, r.Messages...)
}

func (r ModelRequest) validate() error {
	if t := r.Temperature; t != nil && (math.IsNaN(*t) || *t < 0 || *t > 2) {
		return errors.New("request temperature must be between 0 and 2")
	}
	if r.MaxTokens != nil && *r.MaxTokens <= 0 {
		return errors.New("request max_tokens must be positive")
	}
	return nil
}

func (m *remoteModel) ReplyRequest(ctx context.Context, r ModelRequest) (Message, error) {
	return m.generateRequest(ctx, r, false, nil)
}
func (m *remoteModel) StreamRequest(ctx context.Context, r ModelRequest, onText func(string) error) (Message, error) {
	return m.generateRequest(ctx, r, true, onText)
}
func (m OpenAIModel) ReplyRequest(ctx context.Context, r ModelRequest) (Message, error) {
	return m.remote().ReplyRequest(ctx, r)
}
func (m OpenAIModel) StreamRequest(ctx context.Context, r ModelRequest, onText func(string) error) (Message, error) {
	return m.remote().StreamRequest(ctx, r, onText)
}
func (m *remoteModel) generateRequest(ctx context.Context, r ModelRequest, stream bool, onText func(string) error) (Message, error) {
	if err := r.validate(); err != nil {
		return Message{}, err
	}
	// 多个会话可共享同一模型。只覆盖本次调用的副本，避免并发请求互相改变采样参数。
	// nil 继承模型默认值；指向 0 的温度必须原样传给后端。
	configured := *m
	opts := &configured.options
	if t := r.Temperature; t != nil {
		opts.Temperature = t
	}
	if n := r.MaxTokens; n != nil {
		opts.MaxOutputTokens = *n
	}
	if (r.Temperature != nil || r.MaxTokens != nil) && opts.OllamaOptions != nil {
		opts.OllamaOptions = maps.Clone(opts.OllamaOptions)
		if r.Temperature != nil {
			delete(opts.OllamaOptions, "temperature")
		}
		if r.MaxTokens != nil {
			delete(opts.OllamaOptions, "num_predict")
		}
	}
	if r.Temperature != nil && opts.GeminiGenerationConfig != nil {
		opts.GeminiGenerationConfig = maps.Clone(opts.GeminiGenerationConfig)
		delete(opts.GeminiGenerationConfig, "temperature")
	}
	return configured.generate(ctx, r.withSystem(), r.Tools, stream, onText)
}

func (a *Agent) sendModel(ctx context.Context, r ModelRequest) (Message, error) {
	if err := r.validate(); err != nil {
		return Message{}, err
	}
	structured, supportsRequest := a.model.(RequestModel)
	streaming, supportsStream := a.model.(StreamingModel)
	fullStream, supportsFullStream := a.model.(StreamingRequestModel)
	if r.Stream && (supportsFullStream || supportsStream) {
		child, trace := a.startSpan(ctx, "llm.stream", "llm.stream.duration", a.modelAttributes(true), nil)
		var msg Message
		var err error
		if supportsFullStream {
			msg, err = fullStream.StreamRequest(child, r, nil)
		} else {
			msg, err = streaming.Stream(child, r.withSystem(), r.Tools, nil)
		}
		trace.set("content_length", len([]rune(msg.Content)))
		trace.set("tool_call_count", len(msg.ToolCalls))
		trace.end(err)
		return msg, err
	}
	child, trace := a.startSpan(ctx, "llm.request", "llm.request.duration", a.modelAttributes(false), nil)
	var msg Message
	var err error
	if supportsRequest {
		msg, err = structured.ReplyRequest(child, r)
	} else {
		msg, err = a.model.Reply(child, r.withSystem(), r.Tools)
	}
	trace.end(err)
	return msg, err
}
