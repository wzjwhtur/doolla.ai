package doolla

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

func (a *Agent) workflow(ctx context.Context, t Turn, q string, tools []ToolSpec, emit func(Component) error) (bool, error) {
	if a.config.Workflow != nil {
		return a.config.Workflow(ctx, t, q, emit)
	}
	names := make([]string, 0, len(tools))
	for _, spec := range tools {
		names = append(names, spec.Name)
	}
	setup := toolSetup(names)
	admin := slices.Contains(t.User.Groups, "admin")
	cmd := strings.ToLower(strings.TrimSpace(q))
	switch cmd {
	case "":
		if a.config.WelcomeMessage != "" {
			return true, emit(textComponent(a.config.WelcomeMessage))
		}
		if !admin {
			if !setup[0].available {
				return true, emit(textComponent("Setup required. Connect a database to ask questions about your data."))
			}
			return true, emit(textComponent("Welcome to Doolla AI. Ask a question about your data, or type /help."))
		}
		title := "Admin: System Ready"
		if !setup[0].available {
			title = "Admin: Setup Required"
		}
		content := "**🔒 Admin View**\n\n**Setup:** " + setup.summary()
		actions := []map[string]string{{"label": "Help", "action": "/help", "variant": "secondary"}}
		if setup[1].available {
			content += "\n\n**Memory Management** — As an admin you can review and delete saved memories."
			actions = append(actions, map[string]string{"label": "View memories", "action": "/memories", "variant": "secondary"})
		}
		return true, emit(Component{Type: "card", Data: map[string]any{"title": title, "content": content, "actions": actions, "markdown": true}})
	case "help", "/h", "/help":
		help := "Ask a question about your data or request a chart. Type /help for available commands."
		if admin {
			help += " Admin Commands: /status, /memories and /delete <id>."
		}
		return true, emit(textComponent(help))
	case "/delete", "status", "/status", "memories", "/memories", "/recent_memories", "recent_memories":
	default:
		if !strings.HasPrefix(cmd, "/delete ") {
			return false, nil
		}
	}
	if !admin {
		return true, emit(textComponent("Access Denied. This command is available only to administrators."))
	}
	if cmd == "status" || cmd == "/status" {
		if err := emit(textComponent("## Setup Status Report\n\n" + setup.summary() + "\n\nAvailable tools: " + strings.Join(names, ", "))); err != nil {
			return true, err
		}
		for _, item := range setup {
			state := "warning"
			if item.available {
				state = "success"
			}
			if err := emit(Component{Type: "status_card", Data: map[string]any{"title": item.name, "status": state, "description": item.description()}}); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	if a.config.Memory == nil {
		return true, emit(textComponent("No Memory System. Configure agent memory to use this command."))
	}
	if cmd == "/delete" {
		return true, emit(textComponent("Invalid Command. Usage: /delete [memory_id]"))
	}
	if strings.HasPrefix(cmd, "/delete ") {
		id := strings.TrimSpace(strings.TrimSpace(q)[len("/delete "):])
		for _, kind := range []string{"tool", "text"} {
			ok, err := a.config.Memory.Delete(ctx, kind, id)
			if err != nil {
				return true, err
			}
			if ok {
				return true, emit(textComponent("Memory Deleted — " + id))
			}
		}
		return true, emit(textComponent("Memory Not Found."))
	}
	var all []MemoryEntry
	for _, kind := range []string{"tool", "text"} {
		items, err := a.config.Memory.Recent(ctx, kind, 10)
		if err != nil {
			return true, err
		}
		all = append(all, items...)
	}
	if len(all) == 0 {
		return true, emit(textComponent("No recent memories found."))
	}
	if err := emit(textComponent(fmt.Sprintf("Recent Memories (%d)", len(all)))); err != nil {
		return true, err
	}
	previous := ""
	for _, m := range all {
		if m.Kind != previous {
			section := "Text Memories"
			if m.Kind == "tool" {
				section = "Tool Memories"
			}
			if err := emit(textComponent("## " + section)); err != nil {
				return true, err
			}
			previous = m.Kind
		}
		title, body := "Text memory", m.Content
		if m.Kind == "tool" {
			title = "Tool: " + m.ToolName
			args, err := json.Marshal(m.Args)
			if err != nil {
				return true, err
			}
			body = fmt.Sprintf("Question: %s\n\nArguments: %s\n\nSuccess: %t", m.Question, args, m.Success)
		}
		body += "\n\nSaved: " + m.Timestamp.Format(time.RFC3339) + "\n\nID: " + m.ID
		card := Component{Type: "card", Data: map[string]any{"title": title, "content": body, "actions": []map[string]string{{"label": "Delete", "action": "/delete " + m.ID, "variant": "error"}}}}
		if err := emit(card); err != nil {
			return true, err
		}
	}
	return true, nil
}

type capabilityStatus struct {
	name      string
	available bool
	guidance  string
}

func (s capabilityStatus) description() string {
	if s.available {
		return "Available"
	}
	return s.guidance
}

type setupReport []capabilityStatus

func (s setupReport) summary() string {
	var text []string
	for _, item := range s {
		text = append(text, item.name+" — "+item.description())
	}
	return strings.Join(text, "\n\n")
}
func toolSetup(names []string) setupReport {
	has := func(aliases ...string) bool {
		for _, a := range aliases {
			if slices.Contains(names, a) {
				return true
			}
		}
		return false
	}
	return setupReport{
		{"SQL connection", has("run_sql", "sql_query", "execute_sql", "query_sql"), "Register a SQL runner to query data."},
		{"Memory system", has("search_saved_correct_tool_uses") && has("save_question_tool_args"), "Register the memory search and save tools to reuse successful queries."},
		{"Visualization", has("visualize_data", "create_chart", "plot_data", "generate_chart"), "Register a visualization tool to create charts."},
		{"Calculator", has("calculator", "calc", "calculate"), "No calculator registered."},
	}
}
