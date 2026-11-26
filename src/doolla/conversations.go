package doolla

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Conversation struct {
	Metadata json.RawMessage `json:"metadata,omitempty"`
	ID       string          `json:"id"`
	User     User            `json:"user"`
	Created  time.Time       `json:"created_at"`
	Updated  time.Time       `json:"updated_at"`
	Messages []Message       `json:"messages"`
}
type ConversationStore interface {
	Load(context.Context, User, string) ([]Message, error)
	Save(context.Context, User, string, []Message) error
	List(context.Context, User, int, int) ([]Conversation, error)
	Delete(context.Context, User, string) (bool, error)
}

type MemoryConversations struct {
	mu    sync.RWMutex
	items map[conversationKey]Conversation
}

func NewMemoryConversations() *MemoryConversations {
	return &MemoryConversations{items: make(map[conversationKey]Conversation)}
}
func cloneMessages(src []Message) []Message {
	out := append([]Message(nil), src...)
	for i := range out {
		if src[i].Usage != nil {
			usage := *src[i].Usage
			out[i].Usage = &usage
		}
		out[i].ToolCalls = append([]ToolCall(nil), src[i].ToolCalls...)
		out[i].Metadata = append(json.RawMessage(nil), src[i].Metadata...)
		out[i].ModelState = append(json.RawMessage(nil), src[i].ModelState...)
	}
	return out
}
func (s *MemoryConversations) Load(ctx context.Context, u User, id string) ([]Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneMessages(s.items[conversationKey{u.ID, id}].Messages), ctx.Err()
}
func (s *MemoryConversations) Save(ctx context.Context, u User, id string, msg []Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := conversationKey{u.ID, id}
	c := s.items[key]
	now := time.Now().UTC()
	if c.ID == "" {
		c = Conversation{ID: id, User: u, Created: now}
	}
	c.Messages = cloneMessages(msg)
	c.Updated = now
	s.items[key] = c
	return nil
}
func (s *MemoryConversations) List(ctx context.Context, u User, limit, offset int) ([]Conversation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Conversation
	for _, c := range s.items {
		if c.User.ID == u.ID {
			c.Messages = cloneMessages(c.Messages)
			out = append(out, c)
		}
	}
	return page(out, limit, offset), ctx.Err()
}
func (s *MemoryConversations) Delete(ctx context.Context, u User, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := conversationKey{u.ID, id}
	_, ok := s.items[k]
	delete(s.items, k)
	return ok, nil
}
func page(cs []Conversation, limit, offset int) []Conversation {
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Updated.Equal(cs[j].Updated) {
			return cs[i].ID < cs[j].ID
		}
		return cs[i].Updated.After(cs[j].Updated)
	})
	cs = cs[min(len(cs), max(0, offset)):]
	if limit >= 0 {
		cs = cs[:min(limit, len(cs))]
	}
	return cs
}

// FileConversations reads Python 2.x conversation directories on first access.
// New turns use atomic per-user snapshots. A tombstone shadows deleted imports.
type FileConversations struct{ Directory string }

func (s FileConversations) path(u User, id string) string {
	return filepath.Join(s.Directory, ".go", fmt.Sprintf("%x", sha256.Sum256([]byte(u.ID))), fmt.Sprintf("%x.json", sha256.Sum256([]byte(id))))
}
func (s FileConversations) load(u User, id string) (*Conversation, error) {
	path := s.path(u, id)
	data, err := os.ReadFile(path)
	if err == nil {
		var c *Conversation
		err = json.Unmarshal(data, &c)
		return c, err
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if !filepath.IsLocal(id) || filepath.Base(id) != id || strings.HasPrefix(id, ".") {
		return nil, nil
	}
	root, err := os.OpenRoot(s.Directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	meta, err := fs.ReadFile(root.FS(), id+"/metadata.json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m struct {
		ID       string
		User     User
		Created  string          `json:"created_at"`
		Updated  string          `json:"updated_at"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(meta, &m); err != nil {
		return nil, err
	}
	if m.User.ID != u.ID || m.ID != id {
		return nil, nil
	}
	parse := func(value string) time.Time {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
			if t, err := time.Parse(layout, value); err == nil {
				return t
			}
		}
		return time.Time{}
	}
	c := Conversation{ID: id, User: m.User, Metadata: m.Metadata, Created: parse(m.Created), Updated: parse(m.Updated)}
	files, err := fs.ReadDir(root.FS(), id+"/messages")
	if errors.Is(err, fs.ErrNotExist) {
		return &c, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range files {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		file := id + "/messages/" + entry.Name()
		b, err := fs.ReadFile(root.FS(), file)
		if err != nil {
			return nil, err
		}
		var v struct {
			Role, Content, Timestamp string
			Metadata                 json.RawMessage
			CallID                   string `json:"tool_call_id"`
			Calls                    []struct {
				ID, Name  string
				Arguments json.RawMessage
			} `json:"tool_calls"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, fmt.Errorf("decode %s: %w", file, err)
		}
		msg := Message{Role: v.Role, Content: v.Content, ToolCallID: v.CallID, Timestamp: parse(v.Timestamp), Metadata: v.Metadata}
		for _, tc := range v.Calls {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{tc.ID, "function", FunctionCall{tc.Name, string(tc.Arguments)}})
		}
		c.Messages = append(c.Messages, msg)
	}
	return &c, nil
}
func (s FileConversations) Load(ctx context.Context, u User, id string) ([]Message, error) {
	c, err := s.load(u, id)
	if err != nil || c == nil {
		return nil, err
	}
	return c.Messages, ctx.Err()
}
func atomicJSON(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	writeErr := json.NewEncoder(f).Encode(v)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func (s FileConversations) Save(ctx context.Context, u User, id string, msg []Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	prev, err := s.load(u, id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if prev == nil {
		prev = &Conversation{ID: id, User: u, Created: now}
	}
	prev.Updated = now
	prev.Messages = msg
	return atomicJSON(s.path(u, id), prev)
}
func (s FileConversations) List(ctx context.Context, u User, limit, offset int) ([]Conversation, error) {
	var out []Conversation
	seen := map[string]bool{}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(s.path(u, "")), "*.json"))
	if err != nil {
		return nil, err
	}
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var c *Conversation
		if err = json.Unmarshal(data, &c); err != nil {
			return nil, err
		}
		if c != nil && c.User.ID == u.ID {
			out = append(out, *c)
			seen[c.ID] = true
		}
	}
	entries, err := os.ReadDir(s.Directory)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || seen[e.Name()] {
			continue
		}
		c, err := s.load(u, e.Name())
		if err != nil {
			return nil, err
		}
		if c != nil {
			out = append(out, *c)
		}
	}
	return page(out, limit, offset), ctx.Err()
}
func (s FileConversations) Delete(ctx context.Context, u User, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	c, err := s.load(u, id)
	if c == nil || err != nil {
		return false, err
	}
	err = atomicJSON(s.path(u, id), nil)
	return err == nil, err
}

// ExportPython writes a user-owned conversation under a new export directory in
// the Python 2.x format. It returns the base directory for FileSystemConversationStore.
func (s FileConversations) ExportPython(ctx context.Context, u User, id, parent string) (string, error) {
	if !filepath.IsLocal(id) || filepath.Base(id) != id || strings.HasPrefix(id, ".") {
		return "", errors.New("conversation ID cannot be a Python directory name")
	}
	c, err := s.load(u, id)
	if err != nil {
		return "", err
	}
	if c == nil {
		return "", fs.ErrNotExist
	}
	base, err := os.MkdirTemp(parent, "doolla-conversations-")
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(base)
		}
	}()
	dir := filepath.Join(base, id)
	meta := map[string]any{"id": id, "user": c.User, "created_at": c.Created, "updated_at": c.Updated}
	if len(c.Metadata) > 0 {
		meta["metadata"] = c.Metadata
	}
	if err := atomicJSON(filepath.Join(dir, "metadata.json"), meta); err != nil {
		return "", err
	}
	for i, msg := range c.Messages {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		python := map[string]any{"role": msg.Role, "content": msg.Content}
		if !msg.Timestamp.IsZero() {
			python["timestamp"] = msg.Timestamp
		}
		if len(msg.Metadata) > 0 {
			python["metadata"] = msg.Metadata
		}
		if msg.ToolCallID != "" {
			python["tool_call_id"] = msg.ToolCallID
		}
		if len(msg.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				calls = append(calls, map[string]any{"id": call.ID, "name": call.Function.Name, "arguments": json.RawMessage(call.Function.Arguments)})
			}
			python["tool_calls"] = calls
		}
		if err := atomicJSON(filepath.Join(dir, "messages", fmt.Sprintf("%012d.json", i)), python); err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	complete = true
	return base, nil
}
