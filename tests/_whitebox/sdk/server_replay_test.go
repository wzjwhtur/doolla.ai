package doolla

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func replayRequest(mode, payload string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/api/doolla/v2/chat_"+mode, strings.NewReader(payload))
}
func replayPoll(h http.Handler, payload string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, replayRequest("poll", payload))
	return rec
}
func replayCounterServer(t *testing.T, onResult func(context.Context) (Message, error)) (http.Handler, *sql.DB) {
	t.Helper()
	db, err := OpenWritableSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("CREATE TABLE tally(n INTEGER); INSERT INTO tally VALUES(0)"); err != nil {
		t.Fatal(err)
	}
	m := replyFunc(func(ctx context.Context, msg []Message, _ []ToolSpec) (Message, error) {
		if msg[len(msg)-1].Role == "tool" {
			return onResult(ctx)
		}
		return Message{ToolCalls: []ToolCall{wireCall("counter", "run_sql", `{"sql":"UPDATE tally SET n=n+1"}`)}}, nil
	})
	a, err := NewAgent(m, "", Registration{Tool: SQLTool{DB: db}})
	if err != nil {
		t.Fatal(err)
	}
	h := NewServer(a, func(_ http.ResponseWriter, _ *http.Request) (User, error) { return User{ID: "reader"}, nil })
	return h, db
}
func verifyCounter(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT n FROM tally").Scan(&n); err != nil || n != want {
		t.Fatalf("counter=%d want=%d err=%v", n, want, err)
	}
}

func TestReplayConcurrentSSEPoll(t *testing.T) {
	executed := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	h, db := replayCounterServer(t, func(ctx context.Context) (Message, error) {
		if calls.Add(1) == 1 {
			close(executed)
		}
		select {
		case <-ctx.Done():
			return Message{}, ctx.Err()
		case <-release:
			return Message{Content: "updated"}, nil
		}
	})
	const req = `{"message":"increment","request_id":"same","metadata":{"x":1}}`
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	sse := httptest.NewRecorder()
	sseDone := make(chan struct{})
	go func() { defer close(sseDone); h.ServeHTTP(sse, replayRequest("sse", req).WithContext(ctx)) }()
	select {
	case <-executed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if c := replayPoll(h, strings.Replace(req, "increment", "new question", 1)); c.Code != http.StatusConflict {
		t.Fatalf("expected conflict, got %d", c.Code)
	}
	pollDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { pollDone <- replayPoll(h, req) }()
	close(release)
	<-sseDone
	poll := <-pollDone
	if sse.Code != 200 || poll.Code != 200 {
		t.Fatalf("HTTP %d, %d, body=%s", sse.Code, poll.Code, poll.Body.String())
	}
	var result struct {
		ID     string `json:"conversation_id"`
		Chunks []json.RawMessage
	}
	if err := json.Unmarshal(poll.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Chunks) < 2 || result.ID == "" {
		t.Fatal("missing replay result")
	}
	var lines []string
	for line := range strings.SplitSeq(sse.Body.String(), "\n") {
		if raw, ok := strings.CutPrefix(line, "data: "); ok && raw != "[DONE]" {
			lines = append(lines, raw)
		}
	}
	if len(lines) != len(result.Chunks) {
		t.Fatalf("different chunk counts: %d, %d", len(lines), len(result.Chunks))
	}
	for i, raw := range result.Chunks {
		if string(raw) != lines[i] {
			t.Fatalf("chunk %d differs after replay", i)
		}
	}
	withID := strings.Replace(req, `"message"`, `"conversation_id":"`+result.ID+`","message"`, 1)
	if r := replayPoll(h, withID); r.Code != 200 || r.Body.String() != poll.Body.String() {
		t.Fatalf("retry with generated conversation ID failed %d", r.Code)
	}
	verifyCounter(t, db, 1)
}

type brokenChatWriter struct{ *httptest.ResponseRecorder }

func (w brokenChatWriter) Write(b []byte) (int, error) {
	if strings.Contains(string(b), `"type":"notification"`) {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(b)
}

func TestReplayCachesCompletedFailureAndInterrupt(t *testing.T) {
	const req = `{"message":"increment","conversation_id":"conv","request_id":"same"}`
	h, db := replayCounterServer(t, func(context.Context) (Message, error) { return Message{}, errors.New("provider unavailable") })
	first := replayPoll(h, req)
	again := replayPoll(h, req)
	if first.Code != 200 || again.Body.String() != first.Body.String() || !strings.Contains(first.Body.String(), `"status":"error"`) {
		t.Fatalf("failed response not replayed: %s", again.Body.String())
	}
	verifyCounter(t, db, 1)
	broken := strings.Replace(req, `"same"`, `"new"`, 1)
	h.ServeHTTP(brokenChatWriter{httptest.NewRecorder()}, replayRequest("sse", broken))
	if next := replayPoll(h, broken); next.Code != http.StatusConflict || !strings.Contains(next.Body.String(), "interrupted") {
		t.Fatalf("interrupted request retried: %d %s", next.Code, next.Body.String())
	}
	verifyCounter(t, db, 2)
}

func TestTurnOutcomeAcrossTransports(t *testing.T) {
	for _, tc := range []struct {
		name, status, code string
		reply              Message
		err                error
	}{
		{name: "success", status: "succeeded", reply: Message{Content: "Done"}},
		{name: "provider error", status: "failed", code: "processing_error", err: errors.New("upstream offline")},
		{name: "truncated answer", status: "partial", code: "model_response_incomplete", reply: Message{Content: "Started", FinishReason: "length"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, db := replayCounterServer(t, func(context.Context) (Message, error) { return tc.reply, tc.err })
			const payload = `{"message":"increment","conversation_id":"conv","request_id":"test"}`
			var pollResult struct{ Chunks []Chunk }
			poll := replayPoll(h, payload)
			if err := json.Unmarshal(poll.Body.Bytes(), &pollResult); err != nil || len(pollResult.Chunks) == 0 {
				t.Fatalf("poll response: %v %s", err, poll.Body)
			}
			last := pollResult.Chunks[len(pollResult.Chunks)-1]
			data, _ := last.Rich["data"].(map[string]any)
			code, _ := data["code"].(string)
			if last.Rich["type"] != "turn_outcome" || data["status"] != tc.status || code != tc.code || last.RequestID != "test" || last.ConversationID != "conv" {
				t.Fatalf("unexpected terminal frame: %+v", last)
			}
			sse := httptest.NewRecorder()
			h.ServeHTTP(sse, replayRequest("sse", payload))
			frames := strings.Split(strings.TrimSpace(sse.Body.String()), "\n\n")
			var streamed Chunk
			if len(frames) < 2 || frames[len(frames)-1] != "data: [DONE]" {
				t.Fatalf("SSE completion: %s", sse.Body)
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(frames[len(frames)-2], "data: ")), &streamed); err != nil || streamed.Rich["id"] != last.Rich["id"] {
				t.Fatalf("SSE should replay the same outcome: %+v, %v", streamed, err)
			}
			server := httptest.NewServer(h)
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/doolla/v2/chat_websocket", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if err := conn.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
				t.Fatal(err)
			}
			for i := range pollResult.Chunks {
				var chunk Chunk
				if err := wsjson.Read(ctx, conn, &chunk); err != nil || chunk.Rich["id"] != pollResult.Chunks[i].Rich["id"] {
					t.Fatalf("WebSocket replay chunk %d: %+v, %v", i, chunk, err)
				}
			}
			var completion map[string]any
			if err := wsjson.Read(ctx, conn, &completion); err != nil || completion["type"] != "completion" {
				t.Fatalf("WebSocket completion: %v, %v", completion, err)
			}
			verifyCounter(t, db, 1)
		})
	}
}

func TestReplayLimitsAndExpiry(t *testing.T) {
	c := newRequestReplay(ReplayOptions{Retention: time.Minute, MaxEntries: 2, MaxBytes: 10, MaxResponseBytes: 8})
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }
	u := User{ID: "u"}
	msg := "question"
	makeReq := func(id string) ChatRequest { return ChatRequest{RequestID: id, ConversationID: "conv", Message: &msg} }
	req := makeReq("a")
	a, owner, err := c.reserve(u, &req)
	if err != nil || !owner {
		t.Fatal(err)
	}
	c.record(a, []byte("12345678"))
	req = makeReq("b")
	b, _, _ := c.reserve(u, &req)
	c.record(b, []byte("1234"))
	c.finish(b, true)
	if b.err != errReplayTooLarge || c.bytes != 8 {
		t.Fatal("global replay budget")
	}
	req = makeReq("c")
	if _, _, err := c.reserve(u, &req); err != errReplayFull {
		t.Fatalf("expected full cache, got %v", err)
	}
	now = now.Add(time.Minute)
	other, owner, err := c.reserve(u, &req)
	if err != nil || !owner {
		t.Fatal("expired completed entry not reclaimed")
	}
	c.record(other, []byte("01234567890"))
	c.finish(other, true)
	if other.err != errReplayTooLarge {
		t.Fatal("per-response replay budget")
	}
	req = makeReq("a")
	if same, dup, err := c.reserve(u, &req); err != nil || dup || same != a {
		t.Fatal("inflight entry was evicted")
	}
	c.finish(a, true)
	now = now.Add(2 * time.Minute)
	if _, owner, err := c.reserve(u, &req); err != nil || !owner || c.bytes != 0 {
		t.Fatal("request retention after expiry")
	}
}

func TestReplayScopesAuthorizationAndData(t *testing.T) {
	ctx := context.Background()
	c := newRequestReplay(ReplayOptions{})
	msg := "q"
	req := ChatRequest{RequestID: "repeat", Message: &msg, ConversationID: "c"}
	u := User{ID: "alice", Groups: []string{"admin", "analyst"}, Metadata: map[string]any{"tenant": "one"}}
	e, _, _ := c.reserve(u, &req)
	for _, modify := range []func(*User, *ChatRequest){
		func(u *User, _ *ChatRequest) { u.Groups = []string{"analyst"} },
		func(u *User, _ *ChatRequest) { u.Metadata = map[string]any{"tenant": "two"} },
		func(_ *User, r *ChatRequest) { r.Metadata = map[string]any{"filter": "other"} },
		func(_ *User, r *ChatRequest) { r.ConversationID = "other" },
	} {
		v, q := u, req
		modify(&v, &q)
		if _, _, err := c.reserve(v, &q); err != errRequestConflict {
			t.Fatalf("accepted conflicting fingerprint: %v", err)
		}
	}
	again := u
	again.Groups = []string{"analyst", "admin"}
	if same, owner, err := c.reserve(again, &req); err != nil || owner || same != e {
		t.Fatal("group order changed identity")
	}
	other := u
	other.ID = "bob"
	if _, owner, err := c.reserve(other, &req); err != nil || !owner {
		t.Fatal("independent users share entries")
	}
	wait, cancel := context.WithCancel(ctx)
	cancel()
	if err := (&Server{replay: c, agent: &Agent{}}).deliver(wait, u, req, e, false, "poll", func(json.RawMessage) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestReplayInFlightLimitLeavesRetriesAndCompletedIDsIntact(t *testing.T) {
	c := newRequestReplay(ReplayOptions{MaxEntries: 2, MaxInFlight: 1})
	user := User{ID: "viewer"}
	message := "lookup"
	first := ChatRequest{RequestID: "first", Message: &message}
	entry, owner, err := c.reserve(user, &first)
	if err != nil || !owner {
		t.Fatalf("first admission: %t %v", owner, err)
	}
	second := ChatRequest{RequestID: "second", Message: &message}
	if _, _, err := c.reserve(user, &second); !errors.Is(err, errReplayBusy) {
		t.Fatalf("new request should wait: %v", err)
	}
	if existing, owner, err := c.reserve(user, &first); err != nil || owner || existing != entry {
		t.Fatalf("an existing request can still replay: %t %v", owner, err)
	}
	c.finish(entry, true)
	if _, owner, err := c.reserve(user, &second); err != nil || !owner {
		t.Fatalf("another request can run after completion: %t %v", owner, err)
	}
	third := ChatRequest{RequestID: "third", Message: &message}
	if _, _, err := c.reserve(user, &third); !errors.Is(err, errReplayFull) {
		t.Fatalf("completed requests still count toward deduplication capacity: %v", err)
	}
}

func socketEndpoint(s *httptest.Server) string {
	return "ws" + strings.TrimPrefix(s.URL, "http") + "/api/doolla/v2/chat_websocket"
}

func TestWebSocketRecoversAfterInvalidMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	agent, err := NewAgent(&MockModel{}, "")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(agent, nil))
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, socketEndpoint(server), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for _, invalid := range []string{"[", `{"message":null}`, `{}`} {
		if _, status, err := socketChat(ctx, conn, invalid); err != nil || status != 400 {
			t.Fatalf("invalid message: status=%d err=%v", status, err)
		}
	}
	chunks, status, err := socketChat(ctx, conn, `{"message":"/help"}`)
	if err != nil || status != 200 || len(chunks) == 0 {
		t.Fatalf("next message: chunks=%d status=%d err=%v", len(chunks), status, err)
	}
}
func socketChat(ctx context.Context, c *websocket.Conn, raw string) ([]Chunk, int, error) {
	if err := c.Write(ctx, websocket.MessageText, []byte(raw)); err != nil {
		return nil, 0, err
	}
	var chunks []Chunk
	for {
		var payload json.RawMessage
		if err := wsjson.Read(ctx, c, &payload); err != nil {
			return nil, 0, err
		}
		var envelope struct {
			Type string
			Data struct{ Status json.RawMessage }
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return nil, 0, err
		}
		switch envelope.Type {
		case "completion":
			return chunks, 200, nil
		case "error":
			var status int
			err := json.Unmarshal(envelope.Data.Status, &status)
			return nil, status, err
		}
		var chunk Chunk
		if err := json.Unmarshal(payload, &chunk); err != nil {
			return nil, 0, err
		}
		chunks = append(chunks, chunk)
	}
}

func TestReplayWebSocketFrameIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var calls atomic.Int32
	var role atomic.Bool
	role.Store(true)
	resolve := func(_ http.ResponseWriter, r *http.Request) (User, error) {
		// Only a frame carries the validated application session in this fixture.
		if RequestMetadata(r.Context())["session"] != "valid" {
			return User{}, errors.New("no session")
		}
		groups := []string{"user"}
		if role.Load() {
			groups = append(groups, "admin")
		}
		return User{ID: "fixture", Groups: groups}, nil
	}
	m := replyFunc(func(ctx context.Context, ms []Message, ts []ToolSpec) (Message, error) {
		calls.Add(1)
		return Message{Content: "ok"}, nil
	})
	a, err := NewAgent(m, "")
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(NewServer(a, resolve))
	defer s.Close()
	conn, _, err := websocket.Dial(ctx, socketEndpoint(s), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	const req = `{"message":"first","conversation_id":"c","request_id":"r","metadata":{"session":"valid"}}`
	if _, code, err := socketChat(ctx, conn, req); code != 200 || err != nil {
		t.Fatal(code, err)
	}
	poll := replayPoll(s.Config.Handler, req)
	if poll.Code != 200 || calls.Load() != 1 {
		t.Fatalf("cross-transport replay %d calls %d", poll.Code, calls.Load())
	}
	role.Store(false)
	if _, code, err := socketChat(ctx, conn, req); code != 409 || err != nil {
		t.Fatal("role was not refreshed", code, err)
	}
	if _, code, err := socketChat(ctx, conn, strings.Replace(req, `"r"`, `"r2"`, 1)); code != 200 || err != nil {
		t.Fatal("next authorized frame", code, err)
	}
	if _, _, err := socketChat(ctx, conn, strings.Replace(req, "valid", "expired", 1)); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatal("invalid frame executed", err)
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected calls", calls.Load())
	}
}
func TestReplayWebSocketAnonymousCookie(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var owners []string
	var mu sync.Mutex
	agent, err := NewConfiguredAgent(&MockModel{}, AgentConfig{Observe: func(ctx context.Context, e Event) {
		if e.Operation == "message" {
			mu.Lock()
			owners = append(owners, e.Turn.User.ID)
			mu.Unlock()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(NewServer(agent, nil))
	defer s.Close()
	conn, upgrade, err := websocket.Dial(ctx, socketEndpoint(s), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	cookies := upgrade.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "doolla_session" {
		t.Fatal("missing anonymous cookie on upgrade")
	}
	const req = `{"message":"q","conversation_id":"chat","request_id":"one"}`
	first, code, err := socketChat(ctx, conn, req)
	if err != nil || code != 200 || len(first) == 0 {
		t.Fatal(err)
	}
	second, code, err := socketChat(ctx, conn, strings.Replace(req, `"one"`, `"two"`, 1))
	if err != nil || code != 200 || len(second) == 0 {
		t.Fatal(err)
	}
	// A cookie issued by the upgrade is a valid HTTP session for the same
	// user and request, so polling can replay the original output.
	rec := httptest.NewRecorder()
	poll := replayRequest("poll", req)
	poll.AddCookie(cookies[0])
	s.Config.Handler.ServeHTTP(rec, poll)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	cached := struct{ Chunks []Chunk }{}
	if err := json.Unmarshal(rec.Body.Bytes(), &cached); err != nil {
		t.Fatal(err)
	}
	if len(cached.Chunks) != len(first) || cached.Chunks[0].Timestamp != first[0].Timestamp {
		t.Fatal("did not replay the WebSocket turn")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(owners) != 2 || owners[0] != owners[1] {
		t.Fatalf("anonymous identity changed: %v", owners)
	}
}

func TestInvalidHTTPChatUsesJSONAndRejectsTrailingInput(t *testing.T) {
	agent, err := NewAgent(&MockModel{}, "")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(agent, nil)
	for _, mode := range []string{"poll", "sse"} {
		for _, body := range []string{`{}`, `{"message":null}`, `{"message":"valid"}{}`, `{"message":"valid"}garbage`} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, replayRequest(mode, body))
			var rejection map[string]string
			if rec.Code != http.StatusBadRequest || rec.Header().Get("Content-Type") != "application/json" || json.Unmarshal(rec.Body.Bytes(), &rejection) != nil || rejection["error"] == "" {
				t.Fatalf("%s accepted invalid payload or omitted JSON error: %d %s", mode, rec.Code, rec.Body.String())
			}
		}
	}
}
