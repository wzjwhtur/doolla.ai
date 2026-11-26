package doolla

import "context"

type turnOutcome struct {
	status string
	code   string
}

type turnOutcomeKey struct{}

func failedOutcome(code string) turnOutcome  { return turnOutcome{status: "failed", code: code} }
func partialOutcome(code string) turnOutcome { return turnOutcome{status: "partial", code: code} }

// The transport owns the terminal frame. SDK component sinks still receive
// the presentation events and errors they received before the wire extension.
func recordTurnOutcome(ctx context.Context, outcome turnOutcome) {
	if target, _ := ctx.Value(turnOutcomeKey{}).(*turnOutcome); target != nil && outcome.status != "" {
		*target = outcome
	}
}

func (o turnOutcome) component() Component {
	status := o.status
	if status == "" {
		status = "succeeded"
	}
	data := map[string]any{"status": status}
	if o.code != "" {
		data["code"] = o.code
	}
	return Component{Type: "turn_outcome", Data: data}
}
