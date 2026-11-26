package doolla

import (
	"context"
	"maps"
	"strconv"
	"strings"
	"time"
)

// ObservabilityProvider receives the Agent's timed operations. CreateSpan can
// attach a tracing context for calls within that operation. Its EndSpan and
// RecordMetric methods can export to the application's tracing and metrics SDKs.
// Implementations must support multiple concurrent conversations.
type ObservabilityProvider interface {
	CreateSpan(context.Context, string, map[string]any) (context.Context, ObservabilitySpan)
	EndSpan(context.Context, ObservabilitySpan)
	RecordMetric(ctx context.Context, name string, value float64, unit string, tags map[string]string)
}

type ObservabilitySpan interface {
	SetAttribute(string, any)
}

type operationTrace struct {
	ctx      context.Context
	provider ObservabilityProvider
	span     ObservabilitySpan
	started  time.Time
	metric   string
	tags     map[string]string
}

func (a *Agent) startSpan(ctx context.Context, name, metric string, attributes map[string]any, tags map[string]string) (context.Context, operationTrace) {
	p := a.config.Observability
	if p == nil {
		return ctx, operationTrace{}
	}
	if attributes == nil {
		attributes = make(map[string]any)
	}
	for k, v := range tags {
		attributes[k] = v
	}
	started := time.Now()
	// 使用观测服务返回的上下文发起后续调用，模型和工具的 span 才会挂在本轮请求下。
	child, span := p.CreateSpan(ctx, name, attributes)
	if child == nil {
		child = ctx
	}
	return child, operationTrace{child, p, span, started, metric, tags}
}

func (t operationTrace) set(key string, value any) {
	if t.span != nil {
		t.span.SetAttribute(key, value)
	}
}

func (t operationTrace) end(err error) {
	if t.provider == nil {
		return
	}
	duration := time.Since(t.started)
	if err != nil {
		t.set("error", err.Error())
	}
	if t.span != nil {
		t.provider.EndSpan(t.ctx, t.span)
	}
	if t.metric != "" {
		t.provider.RecordMetric(t.ctx, t.metric, float64(duration)/float64(time.Millisecond), "ms", maps.Clone(t.tags))
	}
}

func (a *Agent) workflowSpan(ctx context.Context, t Turn, question string) (context.Context, operationTrace) {
	name, metric := "agent.workflow_handler.try_handle", ""
	if strings.TrimSpace(question) == "" {
		name, metric = "agent.workflow_handler.starter_ui", "agent.workflow_handler.starter_ui.duration"
	}
	return a.startSpan(ctx, name, metric, map[string]any{"conversation_id": t.ConversationID, "user_id": t.User.ID}, nil)
}

func (a *Agent) middlewareSpan(ctx context.Context, name, phase string) (context.Context, operationTrace) {
	return a.startSpan(ctx, "agent.middleware."+phase, "agent.middleware.duration", nil, map[string]string{"middleware": name, "phase": phase})
}

func (a *Agent) toolSpan(ctx context.Context, call ToolCall) (context.Context, operationTrace) {
	return a.startSpan(ctx, "agent.tool.execute", "agent.tool.duration", map[string]any{"tool_call_id": call.ID}, map[string]string{"tool": call.Function.Name})
}

func (t *operationTrace) outcome(key string, value bool) {
	if t.provider == nil {
		return
	}
	t.set(key, value)
	if t.tags == nil {
		t.tags = make(map[string]string)
	}
	t.tags[key] = strconv.FormatBool(value)
}

// modelAttributes can be called on each request, including applications that
// use a custom model and choose not to expose its identifier.
func (a *Agent) modelAttributes(stream bool) map[string]any {
	if a.config.Observability == nil {
		return nil
	}
	name := "unknown"
	switch m := a.model.(type) {
	case *remoteModel:
		name = m.model
	case OpenAIModel:
		name = m.Model
	case *OpenAIModel:
		name = m.Model
	case *MockModel:
		name = "mock"
	case interface{ ModelName() string }:
		name = m.ModelName()
	}
	return map[string]any{"model": name, "stream": stream}
}
