package doolla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

type registeredTool struct {
	Registration
	spec   ToolSpec
	schema *jsonschema.Resolved
}

// ToolRegistry validates model arguments and applies the caller's access groups
// at discovery and execution. Configure TransformArgs before serving requests.
type ToolRegistry struct {
	// Audit covers direct Execute calls and supplies the default for agents using this registry.
	Audit         *AuditConfig
	mu            sync.RWMutex
	entries       map[string]registeredTool
	order         []string
	TransformArgs func(context.Context, User, Tool, json.RawMessage) (json.RawMessage, error)
}

func (r *ToolRegistry) Register(tools ...Registration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	add := make(map[string]registeredTool, len(tools))
	var order []string
	for _, t := range tools {
		if t.Tool == nil {
			return errors.New("nil tool")
		}
		spec := t.Tool.Spec()
		if spec.Name == "" || add[spec.Name].Tool != nil || r.entries[spec.Name].Tool != nil {
			return fmt.Errorf("invalid or duplicate tool name %q", spec.Name)
		}
		schema := jsonschema.Schema{}
		if spec.Parameters != nil {
			b, err := json.Marshal(spec.Parameters)
			if err != nil {
				return err
			}
			if err = json.Unmarshal(b, &schema); err != nil {
				return fmt.Errorf("tool %s schema: %w", spec.Name, err)
			}
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return fmt.Errorf("tool %s: %w", spec.Name, err)
		}
		t.Groups = slices.Clone(t.Groups)
		add[spec.Name] = registeredTool{t, spec, resolved}
		order = append(order, spec.Name)
	}
	if r.entries == nil {
		r.entries = make(map[string]registeredTool)
	}
	for _, name := range order {
		r.entries[name] = add[name]
	}
	r.order = append(r.order, order...)
	return nil
}

// ListTools returns all registered names in registration order for SDK introspection.
// Use Schemas to discover the tools available to a particular user.
func (r *ToolRegistry) ListTools() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.order)
}

// GetTool retrieves the registration for host-side configuration and inspection.
// Execute applies the caller's access groups when dispatching tool calls.
func (r *ToolRegistry) GetTool(name string) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.entries[name]
	reg := t.Registration
	reg.Groups = slices.Clone(reg.Groups)
	return reg, ok
}

func (r *ToolRegistry) Schemas(u User) []ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolSpec, 0, len(r.entries))
	for _, name := range r.order {
		if t := r.entries[name]; t.allows(u) {
			out = append(out, t.spec)
		}
	}
	return out
}
func (r *ToolRegistry) lookup(u User, name string) (registeredTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.entries[name]
	return t, ok && t.allows(u)
}
func (t registeredTool) validate(args json.RawMessage) error {
	var obj any
	if err := json.Unmarshal(args, &obj); err != nil {
		return err
	}
	return t.schema.Validate(obj)
}
func (r *ToolRegistry) prepare(ctx context.Context, user User, t registeredTool, args json.RawMessage) (json.RawMessage, error) {
	if err := t.validate(args); err != nil {
		return nil, err
	}
	if r.TransformArgs != nil {
		var err error
		args, err = r.TransformArgs(ctx, user, t.Tool, args)
		if err != nil {
			return nil, err
		}
		if err = t.validate(args); err != nil {
			return nil, err
		}
	}
	return args, nil
}
func (r *ToolRegistry) Execute(ctx context.Context, user User, call ToolCall) (ToolResult, error) {
	t, ok := r.lookup(user, call.Function.Name)
	turn, _ := CurrentTurn(ctx)
	turn.User = user
	if r.Audit != nil && r.Audit.Sink != nil && turn.RequestID == "" {
		turn.RequestID = newID()
	}
	r.Audit.access(ctx, turn, call, t.Groups, ok)
	if !ok {
		return ToolResult{}, errors.New("tool unavailable")
	}
	args, err := r.prepare(ctx, user, t, json.RawMessage(call.Function.Arguments))
	if err != nil {
		return ToolResult{}, err
	}
	return r.Audit.invoke(ctx, turn, call, args, func() (ToolResult, error) { return t.Tool.Run(ctx, user, args) })
}
