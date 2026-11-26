package doolla

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"doolla.ai/sdk/internal/i18n"
)

// ReplayOptions bounds in-process chat request deduplication. A full table
// rejects new requests until a completed entry expires. Bodies larger than
// the byte budgets are delivered to the original caller and leave a marker
// that prevents another execution until expiry.
type ReplayOptions struct {
	Retention  time.Duration
	MaxEntries int
	// MaxInFlight limits new executions separately from retained request IDs.
	// Zero uses MaxEntries. Existing requests can still be replayed at the limit.
	MaxInFlight      int
	MaxBytes         int
	MaxResponseBytes int
}

type replayProblem struct {
	status int
	reason string
	key    string
}

func (e *replayProblem) Error() string { return e.reason }

var (
	errRequestConflict    = &replayProblem{http.StatusConflict, "Request ID already used for a different request or authorization context", "server.error.request_conflict"}
	errRequestInterrupted = &replayProblem{http.StatusConflict, "The original request was interrupted. Its actions may already have completed", "server.error.request_interrupted"}
	errReplayTooLarge     = &replayProblem{http.StatusConflict, "The original response exceeds the replay budget. The request has already been processed", "server.error.replay_too_large"}
	errReplayFull         = &replayProblem{http.StatusServiceUnavailable, "The request cache is full. Retry later", "server.error.replay_full"}
	errReplayBusy         = &replayProblem{http.StatusServiceUnavailable, "Too many active chat requests. Retry later", "server.error.replay_busy"}
)

// Keys and fingerprints have fixed size regardless of supplied metadata or IDs.
type replayEntry struct {
	key, fingerprint      [32]byte
	generatedConversation string
	done                  chan struct{}
	expires               time.Time
	chunks                []json.RawMessage
	bytes                 int
	err                   error
}

type requestReplay struct {
	mu        sync.Mutex
	entries   map[[32]byte]*replayEntry
	completed list.List
	options   ReplayOptions
	bytes     int
	now       func() time.Time
}

func newRequestReplay(o ReplayOptions) *requestReplay {
	if o.Retention <= 0 {
		o.Retention = 5 * time.Minute
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = 4096
	}
	if o.MaxInFlight <= 0 {
		o.MaxInFlight = o.MaxEntries
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 64 << 20
	}
	if o.MaxResponseBytes <= 0 {
		o.MaxResponseBytes = 4 << 20
	}
	return &requestReplay{entries: make(map[[32]byte]*replayEntry), options: o, now: time.Now}
}
func replayIdentity(u User, req ChatRequest) (key, fingerprint [32]byte, err error) {
	ids, _ := json.Marshal([2]string{u.ID, req.RequestID})
	key = sha256.Sum256(ids)
	u.Groups = slices.Clone(u.Groups)
	slices.Sort(u.Groups)
	u.Groups = slices.Compact(u.Groups)
	if len(req.Metadata) == 0 {
		req.Metadata = nil
	}
	raw, err := json.Marshal(struct {
		User    User
		Request ChatRequest
	}{u, req})
	return key, sha256.Sum256(raw), err
}

func (c *requestReplay) reserve(u User, req *ChatRequest) (*replayEntry, bool, error) {
	// When the first request omits the conversation ID, subsequent retries
	// can use that same payload or echo the conversation ID returned in a chunk.
	key, hash, err := replayIdentity(u, *req)
	if err != nil {
		return nil, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for el := c.completed.Front(); el != nil; el = c.completed.Front() {
		e := el.Value.(*replayEntry)
		if e.expires.After(now) {
			break
		}
		delete(c.entries, e.key)
		c.bytes -= e.bytes
		c.completed.Remove(el)
	}
	if e := c.entries[key]; e != nil {
		if e.generatedConversation != "" && req.ConversationID == e.generatedConversation {
			original := *req
			original.ConversationID = ""
			_, hash, err = replayIdentity(u, original)
			if err != nil {
				return nil, false, err
			}
		}
		if e.fingerprint != hash {
			return nil, false, errRequestConflict
		}
		if req.ConversationID == "" {
			req.ConversationID = e.generatedConversation
		}
		return e, false, nil
	}
	if len(c.entries) >= c.options.MaxEntries {
		return nil, false, errReplayFull
	}
	if len(c.entries)-c.completed.Len() >= c.options.MaxInFlight {
		return nil, false, errReplayBusy
	}
	e := &replayEntry{key: key, fingerprint: hash, done: make(chan struct{})}
	if req.ConversationID == "" {
		req.ConversationID = "conv_" + newID()
		e.generatedConversation = req.ConversationID
	}
	c.entries[key] = e
	return e, true, nil
}
func (c *requestReplay) record(e *replayEntry, raw json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.err != nil {
		return
	}
	if len(raw) > c.options.MaxBytes-c.bytes || len(raw) > c.options.MaxResponseBytes-e.bytes {
		c.bytes -= e.bytes
		e.bytes = 0
		e.chunks = nil
		e.err = errReplayTooLarge
		return
	}
	e.chunks = append(e.chunks, raw)
	e.bytes += len(raw)
	c.bytes += len(raw)
}
func (c *requestReplay) finish(e *replayEntry, complete bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !complete {
		e.err = errRequestInterrupted
		c.bytes -= e.bytes
		e.bytes = 0
		e.chunks = nil
	}
	e.expires = c.now().Add(c.options.Retention)
	c.completed.PushBack(e)
	close(e.done)
}

func (s *Server) deliver(ctx context.Context, u User, req ChatRequest, e *replayEntry, owner bool, transport string, send func(json.RawMessage) error) (err error) {
	observer := s.agent.config.Observability
	if observer != nil {
		var trace operationTrace
		ctx, trace = s.agent.startSpan(ctx, "server.chat.deliver", "server.chat.duration", map[string]any{"request_id": req.RequestID},
			map[string]string{"transport": transport})
		trace.outcome("replay", !owner)
		defer func() { trace.end(err) }()
	}
	if !owner {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-e.done:
		}
		if e.err != nil {
			return e.err
		}
		for _, raw := range e.chunks {
			if err := send(raw); err != nil {
				return err
			}
		}
		return nil
	}
	complete := false
	defer func() { s.replay.finish(e, complete) }()
	var outputErr error
	var firstChunk, firstAnswer bool
	started := time.Now()
	err = s.stream(ctx, u, req, func(chunk Chunk) error {
		if outputErr != nil {
			return outputErr
		}
		raw, err := json.Marshal(chunk)
		if err == nil {
			s.replay.record(e, raw)
			err = send(raw)
		}
		if err == nil && observer != nil {
			elapsed := float64(time.Since(started)) / float64(time.Millisecond)
			if !firstChunk {
				firstChunk = true
				observer.RecordMetric(ctx, "server.chat.first_chunk.duration", elapsed, "ms", map[string]string{"transport": transport})
			}
			data, _ := chunk.Rich["data"].(map[string]any)
			switch chunk.Rich["type"] {
			case "text":
				if !firstAnswer && data["chat_section"] != "activity" {
					firstAnswer = true
					observer.RecordMetric(ctx, "server.chat.first_answer.duration", elapsed, "ms", map[string]string{"transport": transport})
				}
			case "turn_outcome":
				status, _ := data["status"].(string)
				observer.RecordMetric(ctx, "server.chat.outcome.count", 1, "count", map[string]string{"transport": transport, "status": status})
			}
		}
		outputErr = err
		return err
	})
	complete = err == nil && outputErr == nil
	if outputErr != nil {
		return outputErr
	}
	return err
}
func replayStatus(err error) int {
	var p *replayProblem
	if errors.As(err, &p) {
		return p.status
	}
	return http.StatusInternalServerError
}
func replayErrorText(ctx context.Context, err error) string {
	var p *replayProblem
	if errors.As(err, &p) {
		return i18n.T(ctx, p.key)
	}
	return i18n.T(ctx, "server.error.request_incomplete")
}
