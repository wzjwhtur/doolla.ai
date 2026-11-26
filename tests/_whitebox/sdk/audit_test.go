package doolla

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestAuditAgentExecution(t *testing.T) {
	var buffer bytes.Buffer
	sink := JSONAuditSink(&buffer)
	events := map[AuditEventType][]AuditEvent{}
	cfg := AgentConfig{Audit: &AuditConfig{Sink: func(c context.Context, e AuditEvent) error {
		events[e.Type] = append(events[e.Type], e)
		return sink(c, e)
	}, LogUIFeatureChecks: true}}
	cfg.Recover = func(_ context.Context, e Event, i int) (time.Duration, bool) {
		return 0, e.Operation == "tool" && i == 1
	}
	const final = "答复 π"
	model := &MockModel{Replies: []Message{
		{Content: "working", ToolCalls: []ToolCall{{ID: "denied", Type: "function", Function: FunctionCall{"private", `{}`}}, {ID: "invalid", Type: "function", Function: FunctionCall{"read", `{"value":17}`}}, {ID: "ok", Type: "function", Function: FunctionCall{"read", `{"value":"original"}`}}}},
		{Content: final, Usage: &TokenUsage{Input: 5, Output: 3}},
	}}
	visible := functionTool{spec: ToolSpec{Name: "read", Parameters: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}}}
	attempts := 0
	visible.run = func(_ context.Context, _ User, raw json.RawMessage) (ToolResult, error) {
		attempts++
		if !strings.Contains(string(raw), "usable-by-tool") {
			t.Fatalf("parameters modified by auditing: %s", raw)
		}
		if attempts == 1 {
			return ToolResult{}, errors.New("transient")
		}
		return ToolResult{Content: "工具", Component: &Component{Type: "card"}}, nil
	}
	hidden := functionTool{spec: ToolSpec{Name: "private"}, run: func(context.Context, User, json.RawMessage) (ToolResult, error) {
		t.Fatal("denied tool ran")
		return ToolResult{}, nil
	}}
	cfg.Registry = &ToolRegistry{TransformArgs: func(_ context.Context, _ User, _ Tool, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"value":"governed","token":"usable-by-tool","items":[{"privateKey":"secret-material"}]}`), nil
	}}
	a, err := NewConfiguredAgent(model, cfg, Registration{visible, []string{"user"}}, Registration{hidden, []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	u := User{ID: "tenant-user", Username: "anna", Email: "u@example.test", Groups: []string{"user"}}
	ctx := context.WithValue(t.Context(), requestIDKey{}, "req-1")
	if err := a.Chat(ctx, u, "conversation", "query", func(Component) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(events[AuditToolAccess]) != 3 || len(events[AuditToolInvocation]) != 2 || len(events[AuditToolResult]) != 2 || len(events[AuditAIResponse]) != 2 || len(events[AuditUIFeatureAccess]) != 5 {
		t.Fatalf("unexpected audit events: %v", events)
	}
	access := events[AuditToolAccess][0]
	if access.AccessGranted == nil || *access.AccessGranted || !slices.Equal(*access.RequiredGroups, []string{"admin"}) || access.Reason == "" {
		t.Fatal(access)
	}
	for _, e := range events[AuditToolInvocation] {
		p := *e.Parameters
		if e.ToolCallID != "ok" || !*e.ParametersSanitized || p["value"] != "governed" || p["token"] != "[REDACTED]" || !slices.Contains(*e.UIFeaturesAvailable, "tool_names") {
			t.Fatal(e)
		}
	}
	if strings.Contains(buffer.String(), "usable-by-tool") || strings.Contains(buffer.String(), "secret-material") {
		t.Fatal("sensitive audit arguments escaped redaction")
	}
	results := events[AuditToolResult]
	if *results[0].Success || results[0].Error != "transient" || !*results[1].Success || *results[1].ResultSizeBytes != len("工具") || results[1].UIComponentType != "card" {
		t.Fatal(results)
	}
	e := events[AuditAIResponse][1]
	if e.ResponseText != nil || *e.ResponseLengthChars != utf8.RuneCountInString(final) || e.ResponseHash != fmt.Sprintf("%x", sha256.Sum256([]byte(final))) || *e.ResponseLengthTokens != 3 || e.ModelName != "mock" {
		t.Fatal(e)
	}
	ids := map[string]bool{}
	for _, batch := range events {
		for _, e := range batch {
			if e.RequestID != "req-1" || e.UserEmail != u.Email || e.UserID != u.ID || e.ConversationID != "conversation" || e.Timestamp.IsZero() || e.EventID == "" || ids[e.EventID] {
				t.Fatal(e)
			}
			ids[e.EventID] = true
		}
	}
}
func TestAuditOptInAndSinkFailure(t *testing.T) {
	model := &MockModel{Replies: []Message{{Content: "full text"}}}
	var captured []AuditEvent
	failures := 0
	a, err := NewConfiguredAgent(model, AgentConfig{Audit: &AuditConfig{Sink: func(_ context.Context, e AuditEvent) error {
		captured = append(captured, e)
		return errors.New("sink offline")
	}, IncludeFullAIResponses: true, OnError: func(context.Context, error) { failures++ }}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Chat(t.Context(), User{ID: "u"}, "chat", "q", func(Component) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(captured) != 1 || captured[0].Type != AuditAIResponse || captured[0].ResponseText == nil || *captured[0].ResponseText != "full text" || failures != 1 {
		t.Fatal(captured, failures)
	}
}
func TestAuditSinkConcurrentWrites(t *testing.T) {
	var buffer bytes.Buffer
	sink := JSONAuditSink(&buffer)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sink(t.Context(), AuditEvent{RequestID: "unique"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if len(lines) != 32 {
		t.Fatal(len(lines))
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatal(line)
		}
	}
}

func TestAuditRegistryExecute(t *testing.T) {
	var captured []AuditEvent
	r := &ToolRegistry{Audit: &AuditConfig{Sink: func(_ context.Context, e AuditEvent) error { captured = append(captured, e); return nil }, IncludeSensitiveParameters: true}}
	tool := functionTool{spec: ToolSpec{Name: "tool"}, run: func(context.Context, User, json.RawMessage) (ToolResult, error) {
		return ToolResult{Content: "done"}, nil
	}}
	if err := r.Register(Registration{tool, []string{"admin"}}); err != nil {
		t.Fatal(err)
	}
	call := ToolCall{ID: "c", Function: FunctionCall{"tool", `{"access_token":"explicitly-included"}`}}
	if _, err := r.Execute(t.Context(), User{ID: "denied"}, call); err == nil {
		t.Fatal("expected denial")
	}
	ctx := context.WithValue(t.Context(), turnKey{}, Turn{ConversationID: "conversation", RequestID: "request"})
	if _, err := r.Execute(ctx, User{ID: "allowed", Groups: []string{"admin"}}, call); err != nil {
		t.Fatal(err)
	}
	if len(captured) != 4 || captured[0].Type != AuditToolAccess || *captured[0].AccessGranted || captured[0].RequestID == "" || captured[2].Type != AuditToolInvocation || *captured[2].ParametersSanitized || (*captured[2].Parameters)["access_token"] != "explicitly-included" || captured[3].RequestID != "request" || captured[3].UserID != "allowed" || !*captured[3].Success {
		t.Fatal(captured)
	}
	a, err := NewConfiguredAgent(&MockModel{}, AgentConfig{Registry: r})
	if err != nil || a.config.Audit != r.Audit {
		t.Fatal("missing registry audit on agent", err)
	}
}
