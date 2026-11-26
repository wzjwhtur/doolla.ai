package cli

import (
	"context"
	"log/slog"

	"doolla.ai/sdk"
)

type timingRequestKey struct{}

// timingLogger exports measurements independently of audit logging. It keeps
// the request ID for correlation and omits SQL, prompts, credentials and users.
type timingLogger struct{ logger *slog.Logger }

func (l timingLogger) CreateSpan(ctx context.Context, _ string, attributes map[string]any) (context.Context, doolla.ObservabilitySpan) {
	if id, _ := attributes["request_id"].(string); id != "" {
		ctx = context.WithValue(ctx, timingRequestKey{}, id)
	}
	return ctx, nil
}

func (l timingLogger) EndSpan(context.Context, doolla.ObservabilitySpan) {}

func (l timingLogger) RecordMetric(ctx context.Context, name string, value float64, unit string, tags map[string]string) {
	attrs := []slog.Attr{slog.String("name", name), slog.Float64("value", value), slog.String("unit", unit)}
	if id, _ := ctx.Value(timingRequestKey{}).(string); id != "" {
		attrs = append(attrs, slog.String("request_id", id))
	}
	for _, key := range []string{"transport", "tool", "enhancer", "phase", "success", "status", "result", "replay"} {
		if value, ok := tags[key]; ok {
			attrs = append(attrs, slog.String(key, value))
		}
	}
	l.logger.LogAttrs(ctx, slog.LevelInfo, "doolla.metric", attrs...)
}
