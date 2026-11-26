package tests

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"doolla.ai/sdk"
)

type testModel func(context.Context, []doolla.Message, []doolla.ToolSpec) (doolla.Message, error)

func (f testModel) Reply(ctx context.Context, messages []doolla.Message, tools []doolla.ToolSpec) (doolla.Message, error) {
	return f(ctx, messages, tools)
}

// echoTool returns exactly the arguments passed by the registry.
type echoTool string

func (tool echoTool) Spec() doolla.ToolSpec {
	return doolla.ToolSpec{Name: string(tool)}
}

func (tool echoTool) Run(_ context.Context, _ doolla.User, args json.RawMessage) (doolla.ToolResult, error) {
	return doolla.ToolResult{Content: string(args)}, nil
}

func TestToolVisibilityMatchesExecution(t *testing.T) {
	registry := &doolla.ToolRegistry{}
	regs := []doolla.Registration{
		{Tool: echoTool("public")},
		{Tool: echoTool("reports"), Groups: []string{"analyst", "admin"}},
		{Tool: echoTool("manage"), Groups: []string{"admin"}},
	}
	if err := registry.Register(regs...); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		user  doolla.User
		names []string
	}{
		{doolla.User{ID: "guest"}, []string{"public"}},
		{doolla.User{ID: "reader", Groups: []string{"staff", "analyst"}}, []string{"public", "reports"}},
		{doolla.User{ID: "owner", Groups: []string{"admin"}}, []string{"public", "reports", "manage"}},
	} {
		t.Run(tc.user.ID, func(t *testing.T) {
			var names []string
			for _, spec := range registry.Schemas(tc.user) {
				names = append(names, spec.Name)
			}
			if !slices.Equal(names, tc.names) {
				t.Fatalf("visible tools %v, want %v", names, tc.names)
			}
			for _, name := range []string{"public", "reports", "manage", "unknown"} {
				args := `{"query":"weekly"}`
				result, err := registry.Execute(t.Context(), tc.user, doolla.ToolCall{Function: doolla.FunctionCall{Name: name, Arguments: args}})
				if slices.Contains(tc.names, name) {
					if err != nil || result.Content != args {
						t.Errorf("%s: content %q, error %v", name, result.Content, err)
					}
				} else if err == nil {
					t.Errorf("user %s executed unavailable tool %s", tc.user.ID, name)
				}
			}
		})
	}
}

func TestAgentAppliesToolArgumentPolicy(t *testing.T) {
	for _, tc := range []struct {
		role, want string
		reject     bool
	}{
		{"admin", `{"scope":"all"}`, false},
		{"analyst", `{"scope":"department"}`, false},
		{"user", `{"scope":"own"}`, false},
		{"guest", "scope denied", true},
	} {
		t.Run(tc.role, func(t *testing.T) {
			user := doolla.User{ID: "user-" + tc.role, Groups: []string{tc.role}}
			const query = `{"scope":"all"}`
			var transforms, replies int
			registry := &doolla.ToolRegistry{TransformArgs: func(ctx context.Context, who doolla.User, tool doolla.Tool, args json.RawMessage) (json.RawMessage, error) {
				transforms++
				turn, ok := doolla.CurrentTurn(ctx)
				if !ok || turn.ConversationID != "orders" || who.ID != user.ID || tool.Spec().Name != "orders" || string(args) != query {
					t.Fatalf("unexpected transform context %+v, user %+v, tool %s, args %s", turn, who, tool.Spec().Name, args)
				}
				switch who.Groups[0] {
				case "admin":
					return args, nil
				case "analyst":
					return []byte(`{"scope":"department"}`), nil
				case "user":
					return []byte(`{"scope":"own"}`), nil
				default:
					return nil, errors.New("scope denied")
				}
			}}
			model := testModel(func(_ context.Context, messages []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
				replies++
				if replies == 1 {
					return doolla.Message{ToolCalls: []doolla.ToolCall{{ID: "lookup", Type: "function", Function: doolla.FunctionCall{Name: "orders", Arguments: query}}}}, nil
				}
				last := messages[len(messages)-1]
				if last.Role != "tool" || (!tc.reject && last.Content != tc.want) || (tc.reject && !strings.Contains(last.Content, tc.want)) {
					t.Fatalf("unexpected tool response %+v", last)
				}
				return doolla.Message{Content: "done"}, nil
			})
			agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{Registry: registry}, doolla.Registration{Tool: echoTool("orders")})
			if err != nil {
				t.Fatal(err)
			}
			if err := agent.Chat(t.Context(), user, "orders", "Show orders", func(doolla.Component) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if transforms != 1 || replies != 2 {
				t.Fatalf("transform calls %d, model replies %d", transforms, replies)
			}
		})
	}
}

func TestPromptEnhancementReachesModel(t *testing.T) {
	model := testModel(func(_ context.Context, messages []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
		if len(messages) != 2 || messages[0].Content != "orders for u" || messages[1].Content != "filtered question" {
			t.Fatalf("model received %+v", messages)
		}
		return doolla.Message{Content: "ok"}, nil
	})
	config := doolla.AgentConfig{
		SystemPrompt: "orders",
		EnhancePrompt: func(_ context.Context, turn doolla.Turn, prompt, question string) (string, error) {
			if !strings.Contains(prompt, "orders") || question != "original question" {
				t.Fatalf("prompt %q, question %q", prompt, question)
			}
			return "orders for " + turn.User.ID, nil
		},
		Filters: []func(context.Context, doolla.Turn, []doolla.Message) ([]doolla.Message, error){
			func(_ context.Context, turn doolla.Turn, messages []doolla.Message) ([]doolla.Message, error) {
				if turn.User.ID != "u" || messages[1].Content != "original question" {
					t.Fatalf("filter context %+v, messages %+v", turn, messages)
				}
				messages[1].Content = "filtered question"
				return messages, nil
			},
		},
	}
	agent, err := doolla.NewConfiguredAgent(model, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Chat(t.Context(), doolla.User{ID: "u"}, "orders", "original question", func(doolla.Component) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowCommandAccess(t *testing.T) {
	model := testModel(func(context.Context, []doolla.Message, []doolla.ToolSpec) (doolla.Message, error) {
		t.Fatal("workflow command reached the model")
		return doolla.Message{}, nil
	})
	agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		role, command, want string
		hidden              []string
	}{
		{"guest", "/help", "/help", []string{"/status", "/memories", "/delete"}},
		{"admin", "/help", "/delete", nil},
		{"user", "/status", "Access Denied", nil},
		{"user", "/memories", "Access Denied", nil},
		{"user", "/delete row", "Access Denied", nil},
		{"admin", "/status", "Setup Status Report", nil},
		{"admin", "/memories", "No Memory System", nil},
	} {
		t.Run(tc.role+tc.command, func(t *testing.T) {
			var output strings.Builder
			err := agent.Chat(t.Context(), doolla.User{ID: tc.role, Groups: []string{tc.role}}, "orders", tc.command, func(c doolla.Component) error {
				if content, ok := c.Data["content"].(string); ok {
					output.WriteString(content)
				}
				return nil
			})
			if err != nil || !strings.Contains(output.String(), tc.want) {
				t.Fatalf("command output %q, error %v", output.String(), err)
			}
			for _, hidden := range tc.hidden {
				if strings.Contains(output.String(), hidden) {
					t.Errorf("command output contains %q", hidden)
				}
			}
		})
	}
}
