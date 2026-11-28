package doolla

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"doolla.ai/sdk/internal/i18n"
)

// Receipts are intentionally ephemeral. They never come from model text,
// conversation history, a browser-supplied SQL string or request metadata.
const sqlConfirmationLifetime = 15 * time.Minute

type sqlCandidate struct {
	id, user, conversation, request, call, question, statement string
	created                                                    time.Time
	saved                                                      bool
}
type sqlConfirmations struct {
	mu    sync.Mutex
	slots [512]sqlCandidate
	next  int
}

func (p *sqlConfirmations) offer(ctx context.Context, t Turn, question string, r sqlReceipt) Component {
	candidate := sqlCandidate{id: memoryID(), user: t.User.ID, conversation: t.ConversationID, request: t.RequestID, call: r.callID, question: question, statement: r.statement, created: time.Now().UTC()}
	p.mu.Lock()
	p.slots[p.next] = candidate
	p.next = (p.next + 1) % len(p.slots)
	p.mu.Unlock()
	label := i18n.T(ctx, "server.memory.confirm_sql")
	detail := i18n.T(ctx, "server.memory.confirm_result")
	if slices.Contains(t.UIFeatures, "sql_query") {
		detail = r.statement
	}
	return Component{Type: "card", Data: map[string]any{"title": label, "content": question + "\n\n" + detail, "markdown": false, "actions": []map[string]string{{"label": label, "action": "/confirm-sql " + candidate.id, "variant": "primary"}}}}
}

func (p *sqlConfirmations) find(id string, t Turn) (sqlCandidate, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, c := range p.slots {
		if id != "" && c.id == id && c.user == t.User.ID && c.conversation == t.ConversationID && time.Since(c.created) < sqlConfirmationLifetime {
			return c, i
		}
	}
	return sqlCandidate{}, -1
}

type sqlApprovalKey struct{}
type sqlApproval struct {
	sqlCandidate
	persisted bool
}

func (a *Agent) canConfirmSQL(u User) bool {
	query, ok := a.registry.lookup(u, "run_sql")
	if !ok {
		return false
	}
	switch query.Tool.(type) {
	case SQLTool, *SQLTool:
	default:
		return false
	}
	save, ok := a.registry.lookup(u, "save_question_tool_args")
	_, builtin := save.Tool.(registeredMemoryTool)
	return ok && builtin
}

func (a *Agent) confirmSQL(ctx context.Context, t Turn, id string, emit func(Component) error) error {
	allowed := a.canConfirmSQL(t.User)
	c, index := a.confirmations.find(id, t)
	if !allowed || index < 0 {
		recordTurnOutcome(ctx, failedOutcome("sql_confirmation_unavailable"))
		return emit(textComponent(i18n.T(ctx, "server.memory.confirm_unavailable")))
	}
	if !c.saved {
		// The agent serializes turns for one user/conversation. Reuse the ID even
		// after an ambiguous store error so a retry is an upsert of the same entry.
		args, err := json.Marshal(map[string]any{"question": c.question, "tool_name": "run_sql", "args": map[string]string{"sql": c.statement}})
		if err != nil {
			return err
		}
		approval := &sqlApproval{sqlCandidate: c}
		child := context.WithValue(ctx, sqlApprovalKey{}, approval)
		call := wireCall(newID(), "save_question_tool_args", string(args))
		result, completed, err := a.executeCall(child, t, call, emit, &turnEvidence{})
		if err != nil {
			return err
		}
		if completed != nil {
			if err := emit(*completed); err != nil {
				return err
			}
		}
		if !approval.persisted || result.Error != "" {
			recordTurnOutcome(ctx, failedOutcome("sql_confirmation_denied"))
			return emit(textComponent(i18n.T(ctx, "server.memory.confirm_denied")))
		}
		a.confirmations.mu.Lock()
		if a.confirmations.slots[index].id == id {
			a.confirmations.slots[index].saved = true
		}
		a.confirmations.mu.Unlock()
	}
	return emit(textComponent(i18n.T(ctx, "server.memory.saved_id", c.id)))
}

func sqlMemoryArgs(args json.RawMessage) bool {
	var entry struct {
		ToolName string `json:"tool_name"`
		Args     map[string]json.RawMessage
	}
	if json.Unmarshal(args, &entry) != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(entry.ToolName), "run_sql") || entry.Args["sql"] != nil
}
