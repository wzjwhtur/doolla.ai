package doolla

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"doolla.ai/sdk/internal/i18n"
)

func (a *Agent) workflow(ctx context.Context, t Turn, q string, tools []ToolSpec, emit func(Component) error) (bool, error) {
	if a.config.Workflow != nil {
		return a.config.Workflow(ctx, t, q, emit)
	}
	names := make([]string, 0, len(tools))
	for _, spec := range tools {
		names = append(names, spec.Name)
	}
	setup := toolSetup(ctx, names)
	admin := slices.Contains(t.User.Groups, "admin")
	cmd := strings.ToLower(strings.TrimSpace(q))
	switch cmd {
	case "":
		if a.config.WelcomeMessage != "" {
			return true, emit(textComponent(a.config.WelcomeMessage))
		}
		if !admin {
			if !setup[0].available {
				return true, emit(textComponent(i18n.T(ctx, "server.setup.needs_database")))
			}
			return true, emit(textComponent(i18n.T(ctx, "server.welcome")))
		}
		title := i18n.T(ctx, "server.setup.admin_ready")
		if !setup[0].available {
			title = i18n.T(ctx, "server.setup.admin_required")
		}
		content := i18n.T(ctx, "server.setup.admin_intro") + setup.summary(ctx)
		actions := []map[string]string{{"label": i18n.T(ctx, "server.help.button"), "action": "/help", "variant": "secondary"}}
		if setup[1].available {
			content += i18n.T(ctx, "server.memory.admin_intro")
			actions = append(actions, map[string]string{"label": i18n.T(ctx, "server.memory.view"), "action": "/memories", "variant": "secondary"})
		}
		return true, emit(Component{Type: "card", Data: map[string]any{"title": title, "content": content, "actions": actions, "markdown": true}})
	case "help", "/h", "/help":
		help := i18n.T(ctx, "server.help.body")
		if admin {
			help += i18n.T(ctx, "server.help.admin")
		}
		return true, emit(textComponent(help))
	case "/delete", "status", "/status", "memories", "/memories", "/recent_memories", "recent_memories":
	default:
		if !strings.HasPrefix(cmd, "/delete ") {
			return false, nil
		}
	}
	if !admin {
		return true, emit(textComponent(i18n.T(ctx, "server.error.admin_only")))
	}
	if cmd == "status" || cmd == "/status" {
		if err := emit(textComponent(i18n.T(ctx, "server.setup.report") + setup.summary(ctx) + i18n.T(ctx, "server.setup.tools") + strings.Join(names, ", "))); err != nil {
			return true, err
		}
		for _, item := range setup {
			state := "warning"
			if item.available {
				state = "success"
			}
			if err := emit(Component{Type: "status_card", Data: map[string]any{"title": item.name, "status": state, "description": item.description(ctx)}}); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	if a.config.Memory == nil {
		return true, emit(textComponent(i18n.T(ctx, "server.memory.not_configured")))
	}
	if cmd == "/delete" {
		return true, emit(textComponent(i18n.T(ctx, "server.memory.delete_usage")))
	}
	if strings.HasPrefix(cmd, "/delete ") {
		id := strings.TrimSpace(strings.TrimSpace(q)[len("/delete "):])
		for _, kind := range []string{"tool", "text"} {
			ok, err := a.config.Memory.Delete(ctx, kind, id)
			if err != nil {
				return true, err
			}
			if ok {
				return true, emit(textComponent(i18n.T(ctx, "server.memory.deleted", id)))
			}
		}
		return true, emit(textComponent(i18n.T(ctx, "server.memory.not_found")))
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
		return true, emit(textComponent(i18n.T(ctx, "server.memory.no_recent")))
	}
	if err := emit(textComponent(i18n.T(ctx, "server.memory.recent", len(all)))); err != nil {
		return true, err
	}
	previous := ""
	for _, m := range all {
		if m.Kind != previous {
			section := i18n.T(ctx, "server.memory.text_section")
			if m.Kind == "tool" {
				section = i18n.T(ctx, "server.memory.tool_section")
			}
			if err := emit(textComponent("## " + section)); err != nil {
				return true, err
			}
			previous = m.Kind
		}
		title, body := i18n.T(ctx, "server.memory.text_title"), m.Content
		if m.Kind == "tool" {
			title = i18n.T(ctx, "server.memory.tool_title", m.ToolName)
			args, err := json.Marshal(m.Args)
			if err != nil {
				return true, err
			}
			body = i18n.T(ctx, "server.memory.tool_details", m.Question, string(args), m.Success)
		}
		body += i18n.T(ctx, "server.memory.saved_details", m.Timestamp.Format(time.RFC3339), m.ID)
		card := Component{Type: "card", Data: map[string]any{"title": title, "content": body, "actions": []map[string]string{{"label": i18n.T(ctx, "server.memory.delete"), "action": "/delete " + m.ID, "variant": "error"}}}}
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

func (s capabilityStatus) description(ctx context.Context) string {
	if s.available {
		return i18n.T(ctx, "server.setup.available")
	}
	return s.guidance
}

type setupReport []capabilityStatus

func (s setupReport) summary(ctx context.Context) string {
	var text []string
	for _, item := range s {
		text = append(text, item.name+" — "+item.description(ctx))
	}
	return strings.Join(text, "\n\n")
}
func toolSetup(ctx context.Context, names []string) setupReport {
	has := func(aliases ...string) bool {
		for _, a := range aliases {
			if slices.Contains(names, a) {
				return true
			}
		}
		return false
	}
	return setupReport{
		{i18n.T(ctx, "server.setup.sql"), has("run_sql", "sql_query", "execute_sql", "query_sql"), i18n.T(ctx, "server.setup.sql_help")},
		{i18n.T(ctx, "server.setup.memory"), has("search_saved_correct_tool_uses") && has("save_question_tool_args"), i18n.T(ctx, "server.setup.memory_help")},
		{i18n.T(ctx, "server.setup.charts"), has("visualize_data", "create_chart", "plot_data", "generate_chart"), i18n.T(ctx, "server.setup.charts_help")},
		{i18n.T(ctx, "server.setup.calc"), has("calculator", "calc", "calculate"), i18n.T(ctx, "server.setup.calc_help")},
	}
}
