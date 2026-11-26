package doolla

import (
	"context"
	"maps"
	"slices"
	"time"
)

// ConversationReader is an optional extension to ConversationStore. It exposes
// stored timestamps and metadata to completion hooks. A missing conversation is
// returned as nil. Stores implementing just Load still supply the full history.
type ConversationReader interface {
	LoadConversation(context.Context, User, string) (*Conversation, error)
}

func (s *MemoryConversations) LoadConversation(ctx context.Context, user User, id string) (*Conversation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.items[conversationKey{user.ID, id}]
	if !ok {
		return nil, nil
	}
	return cloneConversation(c), nil
}

func (s FileConversations) LoadConversation(ctx context.Context, user User, id string) (*Conversation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.load(user, id)
}

func cloneConversation(c Conversation) *Conversation {
	c.Messages = cloneMessages(c.Messages)
	c.Metadata = slices.Clone(c.Metadata)
	c.User.Groups = slices.Clone(c.User.Groups)
	c.User.Metadata = maps.Clone(c.User.Metadata)
	return &c
}

func (a *Agent) loadConversation(ctx context.Context, t Turn) (Conversation, error) {
	ctx, trace := a.startSpan(ctx, "agent.conversation.load", "agent.conversation.load.duration", map[string]any{"conversation_id": t.ConversationID, "user_id": t.User.ID}, nil)
	messages, err := a.config.Conversations.Load(ctx, t.User, t.ConversationID)
	trace.set("is_new", len(messages) == 0)
	trace.set("message_count", len(messages))
	trace.end(err)
	return Conversation{ID: t.ConversationID, User: t.User, Messages: messages}, err
}

func (a *Agent) saveMessages(ctx context.Context, t Turn, messages []Message) error {
	ctx, trace := a.startSpan(ctx, "agent.conversation.save", "agent.conversation.save.duration", map[string]any{"conversation_id": t.ConversationID, "message_count": len(messages)}, nil)
	err := a.config.Conversations.Save(ctx, t.User, t.ConversationID, messages)
	trace.end(err)
	return err
}

// finishConversation 在模型给出最终答复或耗尽工具轮数时触发。耗尽轮数时历史
// 截止到最后一次已执行的工具，AfterConversation 仍能读取完整经过，此时没有
// 最终模型消息可传给 AfterMessage。
func (a *Agent) finishConversation(ctx context.Context, t Turn, c Conversation, messages []Message, reply *Message) error {
	c.Messages = messages
	c.Updated = time.Now().UTC()
	if c.Created.IsZero() {
		c.Created = c.Updated
		if len(messages) > 0 && !messages[0].Timestamp.IsZero() {
			c.Created = messages[0].Timestamp
		}
	}
	if !a.config.DisableAutoSave && reply != nil {
		if err := a.saveMessages(ctx, t, messages); err != nil {
			return err
		}
	}
	// Load 可能已经按用户过滤历史。可选的 ConversationReader 只补时间和元数据，
	// 避免用底层存储的原始消息覆盖这次对话实际看到的消息。
	if reader, ok := a.config.Conversations.(ConversationReader); ok && slices.ContainsFunc(a.config.Hooks, func(h Hooks) bool { return h.AfterConversation != nil }) {
		stored, err := reader.LoadConversation(ctx, t.User, t.ConversationID)
		if err != nil {
			return err
		}
		if stored != nil {
			c.Metadata, c.Created = stored.Metadata, stored.Created
			if !a.config.DisableAutoSave {
				c.Updated = stored.Updated
			}
		}
	}
	for _, h := range a.config.Hooks {
		if reply != nil && h.AfterMessage != nil {
			child, trace := a.hookSpan(ctx, h.Name, "after_message", "")
			err := h.AfterMessage(child, t, *reply)
			trace.end(err)
			if err != nil {
				return err
			}
		}
		if h.AfterConversation != nil {
			child, trace := a.hookSpan(ctx, h.Name, "after_message", "")
			err := h.AfterConversation(child, t, *cloneConversation(c))
			trace.end(err)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *Agent) hookSpan(ctx context.Context, name, phase, tool string) (context.Context, operationTrace) {
	tags := map[string]string{"phase": phase}
	if name != "" {
		tags["hook"] = name
	}
	if tool != "" {
		tags["tool"] = tool
	}
	return a.startSpan(ctx, "agent.hook."+phase, "agent.hook.duration", nil, tags)
}

func (a *Agent) beforeMessage(ctx context.Context, t Turn, question string) (string, error) {
	for _, hook := range a.config.Hooks {
		if hook.BeforeMessage == nil {
			continue
		}
		child, trace := a.hookSpan(ctx, hook.Name, "before_message", "")
		next, err := hook.BeforeMessage(child, t, question)
		trace.set("modified_message", next != question)
		trace.end(err)
		if err != nil {
			return "", err
		}
		question = next
	}
	return question, nil
}
