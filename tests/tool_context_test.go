package tests

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"doolla.ai/sdk"
)

type noteTool struct{}

func (noteTool) Spec() doolla.ToolSpec { return doolla.ToolSpec{Name: "write_note"} }
func (noteTool) Run(ctx context.Context, user doolla.User, _ json.RawMessage) (doolla.ToolResult, error) {
	turn, ok := doolla.CurrentTurn(ctx)
	if !ok || turn.Memory == nil || turn.Observability == nil || turn.User.ID != user.ID {
		return doolla.ToolResult{}, errors.New("tool services missing")
	}
	turn.Observability.RecordMetric(ctx, "notes", 1, "count", nil)
	err := turn.Memory.Save(ctx, doolla.MemoryEntry{Kind: "text", Content: "written by tool"})
	return doolla.ToolResult{Content: "saved"}, err
}

func TestAgentInjectsServicesIntoCustomTools(t *testing.T) {
	memory := doolla.NewLocalMemory(10)
	var observer operationRecorder
	model := testModel(func(ctx context.Context, msgs []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
		if msgs[len(msgs)-1].Role == "tool" {
			return doolla.Message{Content: "done"}, nil
		}
		call := doolla.ToolCall{ID: "note", Type: "function", Function: doolla.FunctionCall{Name: "write_note", Arguments: "{}"}}
		return doolla.Message{ToolCalls: []doolla.ToolCall{call}}, nil
	})
	agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{Memory: memory, Observability: &observer}, doolla.Registration{Tool: noteTool{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = agent.Chat(t.Context(), doolla.User{ID: "u"}, "c", "save", func(doolla.Component) error { return nil }); err != nil {
		t.Fatal(err)
	}
	notes, err := memory.Recent(t.Context(), "text", 10)
	if err != nil || len(notes) != 1 || notes[0].Content != "written by tool" || len(observer.metrics["notes"]) != 1 {
		t.Fatalf("notes=%v metrics=%v err=%v", notes, observer.metrics, err)
	}
}
