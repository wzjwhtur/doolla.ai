package doolla

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"doolla.ai/sdk/web_components"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type UserResolver func(http.ResponseWriter, *http.Request) (User, error)

// BrowserUsers issues signed, anonymous browser identities for the demo server.
// Applications can pass their own authenticated resolver to NewServer.
func BrowserUsers() UserResolver {
	key := make([]byte, 32)
	rand.Read(key)
	return SignedBrowserUsers(key)
}

func SignedBrowserUsers(secret []byte) UserResolver {
	if len(secret) < 32 {
		return func(http.ResponseWriter, *http.Request) (User, error) {
			return User{}, errors.New("session signing key must be at least 32 bytes")
		}
	}
	key := append([]byte(nil), secret...)
	sign := func(id string) string {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id))
		return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	return func(w http.ResponseWriter, r *http.Request) (User, error) {
		if c, err := r.Cookie("doolla_session"); err == nil {
			id, sig, ok := strings.Cut(c.Value, ".")
			if ok && id != "" && hmac.Equal([]byte(sig), []byte(sign(id))) {
				return User{ID: id, Groups: []string{"user"}}, nil
			}
		}
		id := newID()
		cookie := &http.Cookie{Name: "doolla_session", Value: id + "." + sign(id), Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil}
		if origin, _ := r.Context().Value(trustedOriginKey{}).(string); strings.HasPrefix(origin, "https://") && !originMatchesHost(r) {
			cookie.SameSite = http.SameSiteNoneMode
			cookie.Secure = true
		}
		http.SetCookie(w, cookie)
		return User{ID: id, Groups: []string{"user"}}, nil
	}
}

type ChatRequest struct {
	Message        *string        `json:"message"`
	ConversationID string         `json:"conversation_id"`
	RequestID      string         `json:"request_id"`
	Metadata       map[string]any `json:"metadata"`
}
type Chunk struct {
	Rich           map[string]any `json:"rich"`
	Simple         map[string]any `json:"simple"`
	ConversationID string         `json:"conversation_id"`
	RequestID      string         `json:"request_id"`
	Timestamp      float64        `json:"timestamp"`
}
type Server struct {
	replay  *requestReplay
	options ServerOptions
	agent   *Agent
	resolve UserResolver
}

func NewServer(agent *Agent, resolve UserResolver) http.Handler {
	return NewServerWithOptions(agent, resolve, ServerOptions{})
}

func NewServerWithOptions(agent *Agent, resolve UserResolver, options ServerOptions) http.Handler {
	if resolve == nil {
		resolve = BrowserUsers()
	}
	s := Server{agent: agent, resolve: resolve, options: options, replay: newRequestReplay(options.Replay)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /docs", webui.ServeDocs)
	mux.HandleFunc("GET /openapi.json", webui.ServeSchema)
	mux.HandleFunc("GET /assets/doolla-components.js", webui.Serve)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>doolla.ai</title>
<script type="module" src="/assets/doolla-components.js"></script>
<style>
  body { margin: 0; padding: 32px 24px; background: #f2f4f7; }
  doolla-chat { --doolla-chat-height: calc(100dvh - 66px); }
  doolla-chat:not(:defined) { display: block; }
  @media (max-width: 760px) {
    body { padding: 0; }
    doolla-chat { --doolla-chat-height: calc(100dvh - 2px); }
  }
</style>
<doolla-chat></doolla-chat>
</html>`)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "healthy", "service": "doolla"})
	})
	mux.HandleFunc("POST /api/doolla/v2/chat_sse", s.httpChat)
	mux.HandleFunc("POST /api/doolla/v2/chat_poll", s.httpChat)
	mux.HandleFunc("GET /api/doolla/v2/chat_websocket", s.socket)
	return cors(mux, options.AllowedOrigins)
}
func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

func (req *ChatRequest) prepare() error {
	if req.Message == nil {
		return errors.New("message is required")
	}
	if req.RequestID == "" {
		req.RequestID = newID()
	}
	return nil
}

func (s *Server) stream(ctx context.Context, u User, req ChatRequest, send func(Chunk) error) error {
	emit := func(c Component) error {
		now := time.Now()
		rich := c.payload(now)
		simple := c.Simple
		if simple == nil && c.Type == "text" {
			simple = map[string]any{"type": "text", "text": c.Data["content"]}
		}
		return send(Chunk{rich, simple, req.ConversationID, req.RequestID, float64(now.UnixMicro()) / 1e6})
	}
	ctx = context.WithValue(ctx, metadataKey{}, req.Metadata)
	ctx = context.WithValue(ctx, requestIDKey{}, req.RequestID)
	question := *req.Message
	if req.Metadata["starter_ui_request"] == true {
		question = ""
	}
	if err := s.agent.Chat(ctx, u, req.ConversationID, question, emit); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var limit *ToolIterationLimitError
		if errors.As(err, &limit) {
			warning := fmt.Sprintf("Reached the configured limit (%d) for tool rounds. The task may be incomplete. Ask me to continue or break the task into smaller steps.", limit.Limit)
			for _, c := range []Component{
				status("warning", "Tool limit reached"), textComponent(warning),
				{Type: "chat_input_update", ID: "doolla-chat-input", Data: map[string]any{"disabled": false, "placeholder": "Continue the task…"}},
			} {
				if err := emit(c); err != nil {
					return err
				}
			}
			return nil
		}
		slog.ErrorContext(ctx, "Doolla chat failed", "request_id", req.RequestID, "conversation_id", req.ConversationID, "error", err)
		for _, component := range []Component{
			{Type: "status_card", Data: map[string]any{"title": "Error processing message", "status": "error", "description": "The request could not be completed. Please try again."}, Simple: map[string]any{"type": "text", "text": "The request could not be completed."}},
			status("error", "Unable to complete this request"),
			{Type: "chat_input_update", ID: "doolla-chat-input", Data: map[string]any{"disabled": false, "placeholder": "Try again…"}},
		} {
			if err := emit(component); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}

func (s *Server) httpChat(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil || req.prepare() != nil || dec.Decode(new(json.RawMessage)) != io.EOF {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid chat request"})
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), metadataKey{}, req.Metadata))
	user, err := s.resolveUser(w, r)
	if err != nil || user.ID == "" {
		http.Error(w, "Unauthorized", 401)
		return
	}
	entry, owner, err := s.replay.reserve(user, &req)
	if err != nil {
		http.Error(w, replayErrorText(err), replayStatus(err))
		return
	}
	if strings.HasSuffix(r.URL.Path, "_sse") {
		ctrl := http.NewResponseController(w)
		started := false
		start := func() {
			if started {
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			started = true
		}
		err := s.deliver(r.Context(), user, req, entry, owner, func(data json.RawMessage) error {
			start()
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return err
			}
			return ctrl.Flush()
		})
		if err != nil {
			if !started && r.Context().Err() == nil {
				http.Error(w, replayErrorText(err), replayStatus(err))
			}
			return
		}
		start()
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	chunks := []json.RawMessage{}
	err = s.deliver(r.Context(), user, req, entry, owner, func(raw json.RawMessage) error { chunks = append(chunks, raw); return nil })
	if err != nil {
		if r.Context().Err() == nil {
			http.Error(w, replayErrorText(err), replayStatus(err))
		}
		return
	}
	writeJSON(w, map[string]any{"conversation_id": req.ConversationID, "request_id": req.RequestID, "chunks": chunks, "total_chunks": len(chunks)})
}
func (s *Server) socket(w http.ResponseWriter, r *http.Request) {
	// Allow metadata-authenticated resolvers to authenticate the first frame.
	// Browser resolvers can issue their initial cookie during the upgrade.
	initResponse := socketResolverResponse{http.Header{}}
	if initial, err := s.resolveUser(initResponse, r); err == nil && initial.ID != "" {
		for _, cookie := range initResponse.header.Values("Set-Cookie") {
			w.Header().Add("Set-Cookie", cookie)
		}
		r = withResolverCookies(r, initResponse.header)
	}
	var opts *websocket.AcceptOptions
	if !originMatchesHost(r) && allowedOrigin(s.options.AllowedOrigins, r.Header.Get("Origin")) {
		// The exact scheme and authority were verified against the application's allowlist.
		opts = &websocket.AcceptOptions{InsecureSkipVerify: true}
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	for {
		kind, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var req ChatRequest
		// 上游 WebSocket 支持同一连接连续提问。错误帧只结束本次请求，
		// 后续帧仍需重新解析，并用该帧的元数据解析用户身份。
		if kind != websocket.MessageText || json.Unmarshal(data, &req) != nil || req.prepare() != nil {
			if err := wsjson.Write(r.Context(), conn, map[string]any{
				"type": "error", "data": map[string]any{"status": 400, "message": "Invalid chat request"},
			}); err != nil {
				return
			}
			continue
		}
		frame := r.WithContext(context.WithValue(r.Context(), metadataKey{}, req.Metadata))
		authResponse := socketResolverResponse{http.Header{}}
		user, authErr := s.resolveUser(authResponse, frame)
		if authErr != nil || user.ID == "" {
			conn.Close(websocket.StatusPolicyViolation, "Unauthorized")
			return
		}
		r = withResolverCookies(r, authResponse.header)
		entry, owner, err := s.replay.reserve(user, &req)
		if err == nil {
			err = s.deliver(frame.Context(), user, req, entry, owner, func(raw json.RawMessage) error {
				return conn.Write(frame.Context(), websocket.MessageText, raw)
			})
		}
		if err != nil {
			var p *replayProblem
			if !errors.As(err, &p) {
				return
			}
			body := map[string]any{"status": p.status, "message": p.reason}
			if err := wsjson.Write(frame.Context(), conn, map[string]any{"type": "error", "rich": map[string]any{"type": "error", "data": body}, "data": body, "request_id": req.RequestID, "conversation_id": req.ConversationID}); err != nil {
				return
			}
			continue
		}
		if err := wsjson.Write(r.Context(), conn, map[string]any{"type": "completion", "rich": map[string]string{"type": "completion"}, "data": map[string]string{"status": "done"}, "request_id": req.RequestID, "conversation_id": req.ConversationID}); err != nil {
			return
		}
	}
}
