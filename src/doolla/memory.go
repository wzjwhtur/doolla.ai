package doolla

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pmezard/go-difflib/difflib"
)

// MemoryEntry represents either a free-form text memory or a successful tool use.
type MemoryEntry struct {
	ID        string         `json:"memory_id"`
	Kind      string         `json:"kind"`
	Question  string         `json:"question,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
	Content   string         `json:"content,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Success   bool           `json:"success"`
	Timestamp time.Time      `json:"timestamp"`
}
type MemoryQuery struct {
	Kind, Text, ToolName string
	Limit                int
	Threshold            float64
}

// MemoryRecall selects the maximum number of matches and minimum similarity.
type MemoryRecall struct {
	Limit     int
	Threshold float64
}

func DefaultTextRecall() MemoryRecall { return MemoryRecall{Limit: 5, Threshold: 0.7} }
func DefaultToolRecall() MemoryRecall { return MemoryRecall{Limit: 10, Threshold: 0.7} }

func (r MemoryRecall) Validate() error {
	if r.Limit < 1 {
		return errors.New("recall limit must be positive")
	}
	if math.IsNaN(r.Threshold) || r.Threshold < 0 || r.Threshold > 1 {
		return errors.New("recall threshold must be between 0 and 1")
	}
	return nil
}

type MemoryMatch struct {
	Memory MemoryEntry `json:"memory"`
	Score  float64     `json:"similarity_score"`
	Rank   int         `json:"rank"`
}
type MemoryStore interface {
	Save(context.Context, MemoryEntry) error
	Search(context.Context, MemoryQuery) ([]MemoryMatch, error)
	Recent(context.Context, string, int) ([]MemoryEntry, error)
	Delete(context.Context, string, string) (bool, error)
	Clear(context.Context, string, time.Time) (int, error)
}

type LocalMemory struct {
	mu       sync.RWMutex
	byKind   map[string][]MemoryEntry
	capacity int
}

func NewLocalMemory(capacity int) *LocalMemory {
	if capacity < 1 {
		capacity = 10000
	}
	return &LocalMemory{byKind: make(map[string][]MemoryEntry), capacity: capacity}
}

// Memory arguments and metadata may contain database identifiers wider than 53 bits.
func decodeMemoryJSON(raw []byte, target any) error {
	if !json.Valid(raw) {
		return errors.New("invalid memory JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func (m *LocalMemory) Save(ctx context.Context, e MemoryEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.Kind != "tool" && e.Kind != "text" {
		return errors.New("unknown memory kind")
	}
	// Copy nested arguments and metadata so callers can safely reuse their input.
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var copy MemoryEntry
	if err = decodeMemoryJSON(raw, &copy); err != nil {
		return err
	}
	if copy.ID == "" {
		copy.ID = memoryID()
	}
	if copy.Timestamp.IsZero() {
		copy.Timestamp = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	arr := m.byKind[e.Kind]
	for i := range arr {
		if arr[i].ID == copy.ID {
			arr[i] = copy
			return nil
		}
	}
	arr = append(arr, copy)
	if len(arr) > m.capacity {
		arr = arr[len(arr)-m.capacity:]
	}
	m.byKind[e.Kind] = arr
	return nil
}

func (m *LocalMemory) Recent(ctx context.Context, kind string, limit int) ([]MemoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := append([]MemoryEntry{}, m.byKind[kind]...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Timestamp.After(items[j].Timestamp) })
	if limit >= 0 && limit < len(items) {
		items = items[:limit]
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	var out []MemoryEntry
	err = decodeMemoryJSON(raw, &out)
	return out, err
}

func (m *LocalMemory) Search(ctx context.Context, q MemoryQuery) ([]MemoryMatch, error) {
	m.mu.RLock()
	items := slices.Clone(m.byKind[q.Kind])
	m.mu.RUnlock()
	out := []MemoryMatch{}
	for _, e := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.Kind == "tool" && !e.Success {
			continue
		}
		if q.ToolName != "" && e.ToolName != q.ToolName {
			continue
		}
		text := e.Content
		if e.Kind == "tool" {
			text = e.Question
		}
		score := similarity(q.Text, text)
		if score >= q.Threshold {
			out = append(out, MemoryMatch{Memory: e, Score: score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if q.Limit >= 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	for i := range out {
		out[i].Rank = i + 1
	}
	// Entry maps are immutable after Save. Clone only the selected hits for callers.
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	var selected []MemoryMatch
	err = decodeMemoryJSON(raw, &selected)
	return selected, err
}

// The demo backend combines token overlap with the matching-block ratio used
// by Python's SequenceMatcher. Vector backends provide their own scoring.
func similarity(a, b string) float64 {
	x, y := strings.Fields(strings.ToLower(a)), strings.Fields(strings.ToLower(b))
	set := make(map[string]int)
	for _, s := range x {
		set[s] |= 1
	}
	for _, s := range y {
		set[s] |= 2
	}
	if len(set) == 0 {
		return 1
	}
	var common int
	for _, v := range set {
		if v == 3 {
			common++
		}
	}
	ra := strings.Split(strings.Join(x, " "), "")
	rb := strings.Split(strings.Join(y, " "), "")
	match := difflib.NewMatcher(ra, rb).Ratio()
	return math.Max(float64(common)/float64(len(set)), match)
}
func (m *LocalMemory) Delete(ctx context.Context, kind, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	arr := m.byKind[kind]
	for i, e := range arr {
		if e.ID == id {
			m.byKind[kind] = append(arr[:i], arr[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}
func (m *LocalMemory) Clear(ctx context.Context, tool string, before time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for kind, arr := range m.byKind {
		keep := arr[:0]
		for _, e := range arr {
			if (tool == "" || e.ToolName == tool) && (before.IsZero() || e.Timestamp.Before(before)) {
				removed++
			} else {
				keep = append(keep, e)
			}
		}
		m.byKind[kind] = keep
	}
	return removed, nil
}

type functionTool struct {
	spec ToolSpec
	run  func(context.Context, User, json.RawMessage) (ToolResult, error)
}

func (t functionTool) Spec() ToolSpec { return t.spec }
func (t functionTool) Run(ctx context.Context, u User, args json.RawMessage) (ToolResult, error) {
	return t.run(ctx, u, args)
}

func MemoryTools(store MemoryStore) []Registration {
	return MemoryToolsWithRecall(store, DefaultToolRecall())
}

// MemoryToolsWithRecall supplies defaults for searches that omit limit or similarity_threshold.
func MemoryToolsWithRecall(store MemoryStore, recall MemoryRecall) []Registration {
	var out []Registration
	for _, name := range []string{"save_text_memory", "save_question_tool_args", "search_saved_correct_tool_uses"} {
		var schema map[string]any
		var desc string
		groups := []string{"admin"}
		switch name {
		case "save_text_memory":
			desc = "Save a piece of domain knowledge."
			schema = objectSchema(map[string]any{"content": strField("Text to remember")}, "content")
		case "save_question_tool_args":
			desc = "Save a correct tool invocation for future questions."
			schema = objectSchema(map[string]any{"question": strField("Original question"), "tool_name": strField("Tool name"), "args": map[string]string{"type": "object"}}, "question", "tool_name", "args")
		default:
			desc = "Find examples of past successful tool use."
			schema = objectSchema(map[string]any{"question": strField("New question"), "tool_name_filter": strField("Optional tool filter"), "limit": map[string]string{"type": "integer"}, "similarity_threshold": map[string]string{"type": "number"}}, "question")
			groups = []string{"admin", "user"}
		}
		out = append(out, Registration{functionTool{ToolSpec{name, desc, schema}, func(ctx context.Context, u User, raw json.RawMessage) (ToolResult, error) {
			args := struct {
				Question, Content string
				ToolName          string `json:"tool_name"`
				ToolFilter        string `json:"tool_name_filter"`
				Args              map[string]any
				Limit             int
				Threshold         float64 `json:"similarity_threshold"`
			}{Limit: recall.Limit, Threshold: recall.Threshold}
			if err := decodeMemoryJSON(raw, &args); err != nil {
				return ToolResult{}, err
			}
			if name == "search_saved_correct_tool_uses" {
				search := MemoryRecall{Limit: args.Limit, Threshold: args.Threshold}
				if args.Question == "" || search.Validate() != nil {
					return ToolResult{}, errors.New("invalid memory search parameters")
				}
				matches, err := store.Search(ctx, MemoryQuery{"tool", args.Question, args.ToolFilter, args.Limit, args.Threshold})
				if err != nil {
					return ToolResult{}, err
				}
				result, err := jsonResult(matches)
				if err != nil {
					return ToolResult{}, err
				}
				brief := fmt.Sprintf("Found %d similar tool usage patterns.", len(matches))
				turn, _ := CurrentTurn(ctx)
				card := status("success", brief)
				if slices.Contains(turn.UIFeatures, "memory_detailed_results") {
					card = Component{Type: "card", Data: map[string]any{"title": "Retrieved memories", "content": result.Content, "collapsible": true}}
				}
				result.Component = &card
				result.Metadata = map[string]any{"count": len(matches)}
				return result, nil
			}
			e := MemoryEntry{ID: memoryID(), Timestamp: time.Now().UTC(), Success: true, Metadata: map[string]any{"user_id": u.ID}}
			if name == "save_text_memory" {
				if args.Content == "" {
					return ToolResult{}, errors.New("content is required")
				}
				e.Kind, e.Content = "text", args.Content
			} else {
				if args.Question == "" || args.ToolName == "" || args.Args == nil {
					return ToolResult{}, errors.New("question, tool_name and args are required")
				}
				e.Kind, e.Question, e.ToolName, e.Args = "tool", args.Question, args.ToolName, args.Args
			}
			err := store.Save(ctx, e)
			if err != nil {
				return ToolResult{}, err
			}
			msg := "Saved memory " + e.ID
			return ToolResult{Content: msg, Component: &Component{Type: "notification", Data: map[string]any{"level": "success", "message": msg}}, Metadata: map[string]any{"memory_id": e.ID}}, nil
		}}, groups})
	}
	return out
}

func jsonResult(v any) (ToolResult, error) {
	raw, err := json.Marshal(v)
	return ToolResult{Content: string(raw)}, err
}
