package doolla

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type AuditEventType string

const (
	AuditToolAccess      AuditEventType = "tool_access_check"
	AuditToolInvocation  AuditEventType = "tool_invocation"
	AuditToolResult      AuditEventType = "tool_result"
	AuditUIFeatureAccess AuditEventType = "ui_feature_access_check"
	AuditAIResponse      AuditEventType = "ai_response_generated"
)

// AuditEvent uses the Python audit event names. Pointer fields distinguish an
// observed zero/false value from fields that do not apply to this event type.
type AuditEvent struct {
	EventID              string          `json:"event_id"`
	Type                 AuditEventType  `json:"event_type"`
	Timestamp            time.Time       `json:"timestamp"`
	UserID               string          `json:"user_id"`
	Username             string          `json:"username,omitempty"`
	UserEmail            string          `json:"user_email,omitempty"`
	UserGroups           []string        `json:"user_groups"`
	ConversationID       string          `json:"conversation_id"`
	RequestID            string          `json:"request_id"`
	ToolName             string          `json:"tool_name,omitempty"`
	ToolCallID           string          `json:"tool_call_id,omitempty"`
	FeatureName          string          `json:"feature_name,omitempty"`
	RequiredGroups       *[]string       `json:"required_groups,omitempty"`
	AccessGranted        *bool           `json:"access_granted,omitempty"`
	Reason               string          `json:"reason,omitempty"`
	Parameters           *map[string]any `json:"parameters,omitempty"`
	ParametersSanitized  *bool           `json:"parameters_sanitized,omitempty"`
	UIFeaturesAvailable  *[]string       `json:"ui_features_available,omitempty"`
	Success              *bool           `json:"success,omitempty"`
	Error                string          `json:"error,omitempty"`
	ExecutionTimeMS      *float64        `json:"execution_time_ms,omitempty"`
	ResultSizeBytes      *int            `json:"result_size_bytes,omitempty"`
	UIComponentType      string          `json:"ui_component_type,omitempty"`
	ResponseLengthChars  *int            `json:"response_length_chars,omitempty"`
	ResponseLengthTokens *int            `json:"response_length_tokens,omitempty"`
	ResponseText         *string         `json:"response_text,omitempty"`
	ResponseHash         string          `json:"response_hash,omitempty"`
	ModelName            string          `json:"model_name,omitempty"`
	Temperature          *float64        `json:"temperature,omitempty"`
	ToolCallsCount       *int            `json:"tool_calls_count,omitempty"`
	ToolNames            *[]string       `json:"tool_names,omitempty"`
}

// AuditSink receives events synchronously. It must support concurrent requests.
// Delivery errors are reported to OnError and do not repeat tool side effects.
type AuditSink func(context.Context, AuditEvent) error
type AuditConfig struct {
	Sink                                                                        AuditSink
	OnError                                                                     func(context.Context, error)
	SkipToolAccessChecks, SkipToolInvocations, SkipToolResults, SkipAIResponses bool
	LogUIFeatureChecks, IncludeFullAIResponses, IncludeSensitiveParameters      bool
}

// JSONAuditSink writes one JSON event per line. Leave the writer open for the
// lifetime of the agent.
func JSONAuditSink(w io.Writer) AuditSink {
	var mu sync.Mutex
	out := json.NewEncoder(w)
	return func(_ context.Context, event AuditEvent) error {
		mu.Lock()
		defer mu.Unlock()
		return out.Encode(event)
	}
}
func (a *AuditConfig) emit(ctx context.Context, t Turn, e AuditEvent) {
	e.EventID = memoryID()
	e.Timestamp = time.Now().UTC()
	e.UserID = t.User.ID
	e.Username = t.User.Username
	e.UserEmail = t.User.Email
	e.UserGroups = append([]string{}, t.User.Groups...)
	e.ConversationID = t.ConversationID
	e.RequestID = t.RequestID
	if err := a.Sink(ctx, e); err != nil {
		if a.OnError != nil {
			a.OnError(ctx, err)
		} else {
			slog.WarnContext(ctx, "audit delivery failed", "event_id", e.EventID, "error", err)
		}
	}
}
func (a *AuditConfig) access(ctx context.Context, t Turn, call ToolCall, groups []string, granted bool) {
	if a == nil || a.Sink == nil || a.SkipToolAccessChecks {
		return
	}
	required := append([]string{}, groups...)
	e := AuditEvent{Type: AuditToolAccess, ToolName: call.Function.Name, ToolCallID: call.ID, AccessGranted: &granted, RequiredGroups: &required}
	if !granted {
		e.Reason = "Tool unavailable to user"
	}
	a.emit(ctx, t, e)
}
func (a *AuditConfig) feature(ctx context.Context, t Turn, name string, groups []string, granted bool) {
	if a == nil || a.Sink == nil || !a.LogUIFeatureChecks {
		return
	}
	required := append([]string{}, groups...)
	a.emit(ctx, t, AuditEvent{Type: AuditUIFeatureAccess, FeatureName: name, AccessGranted: &granted, RequiredGroups: &required})
}
func (a *AuditConfig) invoke(ctx context.Context, t Turn, call ToolCall, args json.RawMessage, run func() (ToolResult, error)) (ToolResult, error) {
	if a == nil || a.Sink == nil {
		return run()
	}
	if !a.SkipToolInvocations {
		var parameters map[string]any
		decoder := json.NewDecoder(bytes.NewReader(args))
		decoder.UseNumber()
		sanitized := false
		if err := decoder.Decode(&parameters); err != nil {
			// A hook may produce invalid/custom data. Avoid recording it verbatim.
			parameters = map[string]any{}
			sanitized = true
		} else if !a.IncludeSensitiveParameters {
			sanitized = redactAuditFields(parameters)
		}
		if parameters == nil {
			parameters = map[string]any{}
		}
		features := append([]string{}, t.UIFeatures...)
		a.emit(ctx, t, AuditEvent{Type: AuditToolInvocation, ToolCallID: call.ID, ToolName: call.Function.Name, Parameters: &parameters, ParametersSanitized: &sanitized, UIFeaturesAvailable: &features})
	}
	start := time.Now()
	res, err := run()
	if !a.SkipToolResults {
		ms := float64(time.Since(start)) / float64(time.Millisecond)
		size := len(res.Content)
		success := err == nil
		e := AuditEvent{Type: AuditToolResult, ToolCallID: call.ID, ToolName: call.Function.Name, Success: &success, ExecutionTimeMS: &ms, ResultSizeBytes: &size}
		if err != nil {
			e.Error = err.Error()
		}
		if res.Component != nil {
			e.UIComponentType = res.Component.Type
		}
		a.emit(ctx, t, e)
	}
	return res, err
}

var auditKeyNormalizer = strings.NewReplacer("_", "", "-", "", ".", "", " ", "")
var auditSensitiveKeys = []string{"password", "secret", "token", "apikey", "credential", "auth", "privatekey", "accesskey"}

func redactAuditFields(value any) bool {
	redacted := false
	switch v := value.(type) {
	case map[string]any:
		for k, nested := range v {
			key := auditKeyNormalizer.Replace(strings.ToLower(k))
			if slices.ContainsFunc(auditSensitiveKeys, func(pattern string) bool { return strings.Contains(key, pattern) }) {
				v[k] = "[REDACTED]"
				redacted = true
			} else if redactAuditFields(nested) {
				redacted = true
			}
		}
	case []any:
		for _, nested := range v {
			if redactAuditFields(nested) {
				redacted = true
			}
		}
	}
	return redacted
}
func (a *AuditConfig) response(ctx context.Context, t Turn, msg Message, model Model) {
	if a == nil || a.Sink == nil || a.SkipAIResponses {
		return
	}
	h := sha256.Sum256([]byte(msg.Content))
	size := utf8.RuneCountInString(msg.Content)
	names := make([]string, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		names = append(names, tc.Function.Name)
	}
	count := len(names)
	e := AuditEvent{Type: AuditAIResponse, ResponseHash: hex.EncodeToString(h[:]), ResponseLengthChars: &size, ToolCallsCount: &count, ToolNames: &names}
	if a.IncludeFullAIResponses {
		e.ResponseText = &msg.Content
	}
	if msg.Usage != nil {
		n := msg.Usage.Output
		e.ResponseLengthTokens = &n
	}
	switch m := model.(type) {
	case *remoteModel:
		e.ModelName = m.model
		if m.options.Temperature != nil {
			v := *m.options.Temperature
			e.Temperature = &v
		}
	case OpenAIModel:
		e.ModelName = m.Model
	case *OpenAIModel:
		e.ModelName = m.Model
	case *MockModel:
		e.ModelName = "mock"
	}
	a.emit(ctx, t, e)
}
