package doolla

import (
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sync"
	"time"
)

// Component data follows the web component's wire format, including Plotly,
// dataframes, status bars, buttons, cards and custom component types.
func NewComponent(kind string, data map[string]any) Component {
	return Component{ID: newID(), Type: kind, Data: data}
}
func (c Component) payload(now time.Time) map[string]any {
	id := c.ID
	if id == "" {
		id = newID()
	}
	op := c.Lifecycle
	if op == "" {
		op = "create"
	}
	visible := c.Visible == nil || *c.Visible
	children := c.Children
	if children == nil {
		children = []string{}
	}
	data := c.Data
	if data == nil {
		data = map[string]any{}
	}
	return map[string]any{"id": id, "type": c.Type, "data": data, "lifecycle": op, "visible": visible, "children": children, "interactive": c.Interactive, "timestamp": now.UTC().Format(time.RFC3339Nano)}
}

func startTask(id, title, description string) Component {
	return Component{Type: "task_tracker_update", ID: "doolla-task-tracker", Data: map[string]any{
		"operation": "add_task",
		"task":      map[string]any{"id": id, "title": title, "description": description, "status": "in_progress"},
	}}
}

func endTask(id string, success bool) Component {
	state := "completed"
	if !success {
		state = "error"
	}
	return Component{Type: "task_tracker_update", ID: "doolla-task-tracker", Data: map[string]any{
		"operation": "update_task", "task_id": id, "status": state,
	}}
}

// ComponentManager maintains rich UI state for a conversation. Apply returns a
// component suited to the existing frontend lifecycle renderer.
type ComponentManager struct {
	batchID    string
	changes    []ComponentChange
	mu         sync.Mutex
	components map[string]Component
	order      []string
	parents    map[string]string
}

func (m *ComponentManager) Apply(c Component) (Component, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applyLocked(c)
}

func (m *ComponentManager) applyLocked(c Component) (Component, error) {
	if m.components == nil {
		m.components = make(map[string]Component)
	}
	if c.ID == "" {
		c.ID = newID()
	}
	old, exists := m.components[c.ID]
	switch c.Lifecycle {
	case "", "create":
		if exists {
			return Component{}, errors.New("component already exists")
		}
		c.Lifecycle = "create"
		m.order = append(m.order, c.ID)
	case "remove":
		if !exists {
			return Component{}, errors.New("unknown component")
		}
		remove := map[string]bool{}
		pending := []string{c.ID}
		for len(pending) > 0 {
			id := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if remove[id] {
				continue
			}
			remove[id] = true
			pending = append(pending, m.components[id].Children...)
			for child, parent := range m.parents {
				if parent == id {
					pending = append(pending, child)
				}
			}
			delete(m.parents, id)
			delete(m.components, id)
		}
		m.order = slices.DeleteFunc(m.order, func(id string) bool { return remove[id] })
		for id, x := range m.components {
			x.Children = slices.DeleteFunc(slices.Clone(x.Children), func(child string) bool { return remove[child] })
			m.components[id] = x
		}
		m.record(c.Lifecycle, c, c.ID, nil, nil)
		return c, nil
	case "update":
		if !exists {
			return Component{}, errors.New("unknown component")
		}
		if c.Type == "" {
			c.Type = old.Type
		}
		data := make(map[string]any)
		maps.Copy(data, old.Data)
		maps.Copy(data, c.Data)
		c.Data = data
		if c.Visible == nil {
			c.Visible = old.Visible
		}
		if c.Children == nil {
			c.Children = slices.Clone(old.Children)
		}
	case "replace":
		if !exists {
			return Component{}, errors.New("unknown component")
		}
	default:
		return Component{}, errors.New("unknown component lifecycle")
	}
	m.components[c.ID] = cloneComponent(c)
	m.record(c.Lifecycle, c, c.ID, nil, nil)
	return c, nil
}

// ComponentPosition is the frontend placement hint. The Go tree uses "inside"
// to attach to the anchor, and sibling relations to attach to its parent.
type ComponentPosition struct {
	Index    *int   `json:"index"`
	AnchorID string `json:"anchor_id,omitempty"`
	Relation string `json:"relation"`
}

// MarshalJSON preserves the nullable anchor and default sibling relation.
func (pos ComponentPosition) MarshalJSON() ([]byte, error) {
	var anchor *string
	if pos.AnchorID != "" {
		anchor = &pos.AnchorID
	}
	relation := pos.Relation
	if relation == "" {
		relation = "after"
	}
	return json.Marshal(map[string]any{"index": pos.Index, "anchor_id": anchor, "relation": relation})
}

// Remove removes the component subtree and returns the frontend change.
func (m *ComponentManager) Remove(id string) (ComponentChange, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.applyLocked(Component{ID: id, Lifecycle: "remove"}); err != nil {
		return ComponentChange{}, err
	}
	return cloneChange(m.changes[len(m.changes)-1]), nil
}

// Add inserts a component and records its position for the renderer.
func (m *ComponentManager) Add(c Component, pos *ComponentPosition) (ComponentChange, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addLocked(c, pos)
}

func (m *ComponentManager) addLocked(c Component, pos *ComponentPosition) (ComponentChange, error) {
	if c.ID == "" {
		c.ID = newID()
	}
	if _, exists := m.components[c.ID]; exists {
		return ComponentChange{}, errors.New("component already exists")
	}
	parent := ""
	if len(m.order) > 0 {
		parent = m.order[0]
	}
	if pos != nil {
		if _, ok := m.components[pos.AnchorID]; ok {
			switch pos.Relation {
			case "inside":
				parent = pos.AnchorID
			case "", "before", "after", "replace":
				if siblingParent := m.parents[pos.AnchorID]; siblingParent != "" {
					parent = siblingParent
				}
			}
		}
	}
	if parent != "" {
		if m.parents == nil {
			m.parents = make(map[string]string)
		}
		m.parents[c.ID] = parent
	}
	if m.components == nil {
		m.components = make(map[string]Component)
	}
	m.components[c.ID] = cloneComponent(c)
	m.order = append(m.order, c.ID)
	m.record("create", c, c.ID, nil, pos)
	return cloneChange(m.changes[len(m.changes)-1]), nil
}

// Emit follows the smart lifecycle used by the Python component manager.
func (m *ComponentManager) Emit(c Component) (ComponentChange, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, exists := m.components[c.ID]
	if !exists {
		return m.addLocked(c, nil)
	}
	if c.Lifecycle != "update" {
		return m.replaceLocked(c.ID, c)
	}
	// Emit receives a complete component. A field absent from its new data
	// must disappear, even though Apply supports partial data patches.
	before, after := old.payload(time.Time{}), c.payload(time.Time{})
	updates := make(map[string]any)
	for key, value := range after {
		if !reflect.DeepEqual(value, before[key]) {
			updates[key] = value
		}
	}
	m.components[c.ID] = cloneComponent(c)
	m.record(c.Lifecycle, c, c.ID, updates, nil)
	return cloneChange(m.changes[len(m.changes)-1]), nil
}

// Update applies named component fields. Component-specific attributes (for
// example content on text or rows on a dataframe) are normalized for the UI.
func (m *ComponentManager) Update(id string, updates map[string]any) (ComponentChange, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.components[id]
	if !ok {
		return ComponentChange{}, errors.New("unknown component")
	}
	c := cloneComponent(old)
	if raw, present := updates["data"]; present {
		data, ok := raw.(map[string]any)
		if !ok {
			return ComponentChange{}, errors.New("data must be an object")
		}
		c.Data = cloneData(data)
	}
	if c.Data == nil {
		c.Data = make(map[string]any)
	}
	if raw, present := updates["type"]; present {
		kind, ok := raw.(string)
		if !ok || kind == "" {
			return ComponentChange{}, errors.New("invalid component type")
		}
		c.Type = kind
	}
	for k, v := range updates {
		switch k {
		case "id":
			if v != id {
				return ComponentChange{}, errors.New("use Replace to change component ID")
			}
		case "data", "type", "timestamp", "lifecycle":
		case "visible", "interactive":
			flag, ok := v.(bool)
			if !ok {
				return ComponentChange{}, errors.New(k + " must be boolean")
			}
			if k == "visible" {
				c.Visible = &flag
			} else {
				c.Interactive = flag
			}
		case "children":
			raw, err := json.Marshal(v)
			if err != nil || json.Unmarshal(raw, &c.Children) != nil {
				return ComponentChange{}, errors.New("children must be an array of component IDs")
			}
		default:
			if k == "rows" && c.Type == "dataframe" {
				k = "data"
			}
			c.Data[k] = v
		}
	}
	c.Lifecycle = "update"
	m.components[id] = cloneComponent(c)
	m.record("update", c, id, updates, nil)
	return cloneChange(m.changes[len(m.changes)-1]), nil
}

// Replace swaps an existing component, preserving descendants and its position
// even when the replacement has a different ID.
func (m *ComponentManager) Replace(targetID string, c Component) (ComponentChange, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.replaceLocked(targetID, c)
}

func (m *ComponentManager) replaceLocked(targetID string, c Component) (ComponentChange, error) {
	old, ok := m.components[targetID]
	if !ok {
		return ComponentChange{}, errors.New("unknown component")
	}
	if c.ID == "" {
		c.ID = newID()
	}
	if _, exists := m.components[c.ID]; exists && c.ID != targetID {
		return ComponentChange{}, errors.New("component already exists")
	}
	if c.Children == nil {
		c.Children = slices.Clone(old.Children)
	}
	// 前端用旧 ID 定位被替换的节点，新 ID 用于后续事件。两个索引和父子引用
	// 在同一把锁内更新，避免快照读到子节点仍指向旧 ID 的中间状态。
	if targetID != c.ID {
		delete(m.components, targetID)
		m.order[slices.Index(m.order, targetID)] = c.ID
		if parent, hasParent := m.parents[targetID]; hasParent {
			delete(m.parents, targetID)
			m.parents[c.ID] = parent
		}
		for id, parent := range m.parents {
			if parent == targetID {
				m.parents[id] = c.ID
			}
		}
		for _, item := range m.components {
			for i, child := range item.Children {
				if child == targetID {
					item.Children[i] = c.ID
				}
			}
		}
	}
	m.components[c.ID] = cloneComponent(c)
	m.record("replace", c, targetID, nil, nil)
	return cloneChange(m.changes[len(m.changes)-1]), nil
}

func (m *ComponentManager) Snapshot() []Component {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Component, 0, len(m.order))
	for _, id := range m.order {
		c := cloneComponent(m.components[id])
		out = append(out, c)
	}
	return out
}

// ComponentChange is a timestamped lifecycle update. BatchID associates updates
// emitted together by the application.
type ComponentChange struct {
	Operation string             `json:"operation"`
	TargetID  string             `json:"target_id"`
	Component Component          `json:"component"`
	Updates   map[string]any     `json:"updates"`
	Position  *ComponentPosition `json:"position"`
	Timestamp time.Time          `json:"timestamp"`
	BatchID   string             `json:"batch_id,omitempty"`
}

func (m *ComponentManager) StartBatch() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batchID = newID()
	return m.batchID
}
func (m *ComponentManager) EndBatch() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.batchID
	m.batchID = ""
	return id
}
func (m *ComponentManager) ChangesSince(after time.Time) []ComponentChange {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []ComponentChange
	for _, change := range m.changes {
		if change.Timestamp.After(after) {
			result = append(result, cloneChange(change))
		}
	}
	return result
}
func (m *ComponentManager) ClearHistory() { m.mu.Lock(); defer m.mu.Unlock(); m.changes = nil }
func (m *ComponentManager) Get(id string) (Component, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.components[id]
	return cloneComponent(c), ok
}

// MarshalJSON uses the frontend component envelope for lifecycle deltas.
func (c ComponentChange) MarshalJSON() ([]byte, error) {
	var component map[string]any
	if c.Operation != "remove" {
		component = c.Component.payload(c.Timestamp)
	}
	var batch *string
	if c.BatchID != "" {
		batch = &c.BatchID
	}
	return json.Marshal(map[string]any{
		"operation": c.Operation, "target_id": c.TargetID, "component": component,
		"updates": c.Updates, "position": c.Position, "timestamp": c.Timestamp, "batch_id": batch,
	})
}

func (m *ComponentManager) record(op string, c Component, target string, updates map[string]any, pos *ComponentPosition) {
	m.changes = append(m.changes, cloneChange(ComponentChange{Operation: op, TargetID: target, Component: c, Updates: updates, Position: pos, Timestamp: time.Now().UTC(), BatchID: m.batchID}))
}

func clonePosition(p *ComponentPosition) *ComponentPosition {
	if p == nil {
		return nil
	}
	out := *p
	if p.Index != nil {
		index := *p.Index
		out.Index = &index
	}
	return &out
}

func cloneChange(c ComponentChange) ComponentChange {
	c.Component = cloneComponent(c.Component)
	c.Updates = cloneData(c.Updates)
	c.Position = clonePosition(c.Position)
	return c
}

// Preserve concrete Go numeric and collection types while copying JSON-like
// payloads, including typed rows and series supplied by database/chart tools.
func cloneData(data map[string]any) map[string]any {
	return copyContainer(reflect.ValueOf(data)).Interface().(map[string]any)
}

func copyContainer(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		return copyContainer(v.Elem())
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), copyContainer(iter.Value()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(copyContainer(v.Index(i)))
		}
		return out
	default:
		return v
	}
}

func cloneComponent(c Component) Component {
	c.Data = cloneData(c.Data)
	c.Simple = cloneData(c.Simple)
	c.Children = slices.Clone(c.Children)
	if c.Visible != nil {
		v := *c.Visible
		c.Visible = &v
	}
	return c
}
