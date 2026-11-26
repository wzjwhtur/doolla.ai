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

	"doolla.ai/sdk/internal/i18n"
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
	Locale         string         `json:"locale,omitempty"`
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
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		locale := s.locale(r, r.URL.Query().Get("locale"))
		setContentLanguage(w, locale)
		webui.ServeDocs(w, r.WithContext(i18n.WithLocale(r.Context(), locale)))
	})
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
		locale := s.locale(r, r.URL.Query().Get("locale"))
		setContentLanguage(w, locale)
		webui.ServeSchema(w, r.WithContext(i18n.WithLocale(r.Context(), locale)))
	})
	mux.HandleFunc("GET /assets/doolla-components.js", webui.Serve)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		locale := s.locale(r, "")
		setContentLanguage(w, locale)
		fmt.Fprintf(w, `<!doctype html>
<html lang="%s">
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
<doolla-chat standalone default-locale="%s"></doolla-chat>
</html>`, locale, i18n.Resolve("", "", s.agent.config.DefaultLocale))
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "healthy", "service": "doolla"})
	})
	mux.HandleFunc("POST /api/doolla/v2/chat_sse", s.httpChat)
	mux.HandleFunc("POST /api/doolla/v2/chat_poll", s.httpChat)
	mux.HandleFunc("GET /api/doolla/v2/chat_websocket", s.socket)
	return cors(mux, options.AllowedOrigins)
}
func (s *Server) locale(r *http.Request, explicit string) string {
	return i18n.Resolve(explicit, r.Header.Get("Accept-Language"), s.agent.config.DefaultLocale)
}

func setContentLanguage(w http.ResponseWriter, locale string) {
	w.Header().Set("Content-Language", locale)
	w.Header().Add("Vary", "Accept-Language")
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
	ctx = i18n.WithLocale(ctx, i18n.Resolve(req.Locale, "", s.agent.config.DefaultLocale))
	ctx = context.WithValue(ctx, metadataKey{}, req.Metadata)
	ctx = context.WithValue(ctx, requestIDKey{}, req.RequestID)
	question := *req.Message
	if req.Metadata["starter_ui_request"] == true {
		question = ""
	}
	var outcome turnOutcome
	if err := s.agent.chat(ctx, u, req.ConversationID, question, emit, &outcome); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var limit *ToolIterationLimitError
		if errors.As(err, &limit) {
			if outcome.status == "" {
				outcome = failedOutcome("tool_iteration_limit")
			}
			warning := i18n.T(ctx, "server.error.tool_limit_body", limit.Limit)
			for _, c := range []Component{
				status("warning", i18n.T(ctx, "server.error.tool_limit")), textComponent(warning),
				{Type: "chat_input_update", ID: "doolla-chat-input", Data: map[string]any{"disabled": false, "placeholder": i18n.T(ctx, "server.error.continue_task")}},
			} {
				if err := emit(c); err != nil {
					return err
				}
			}
			return emit(outcome.component())
		}
		slog.ErrorContext(ctx, "Doolla chat failed", "request_id", req.RequestID, "conversation_id", req.ConversationID, "error", err)
		for _, component := range []Component{
			{Type: "status_card", Data: map[string]any{"title": i18n.T(ctx, "server.error.processing_error"), "status": "error", "description": i18n.T(ctx, "server.error.try_again_body")}, Simple: map[string]any{"type": "text", "text": i18n.T(ctx, "server.error.request_incomplete")}},
			status("error", i18n.T(ctx, "server.error.unable")),
			{Type: "chat_input_update", ID: "doolla-chat-input", Data: map[string]any{"disabled": false, "placeholder": i18n.T(ctx, "server.error.retry")}},
		} {
			if err := emit(component); err != nil {
				return err
			}
		}
		return emit(failedOutcome("processing_error").component())
	}
	return emit(outcome.component())
}

func (s *Server) httpChat(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	locale := s.locale(r, "")
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil || req.prepare() != nil || dec.Decode(new(json.RawMessage)) != io.EOF {
		w.Header().Set("Content-Type", "application/json")
		setContentLanguage(w, locale)
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": i18n.Format(locale, "server.error.invalid_request"), "code": "invalid_chat_request"})
		return
	}
	req.Locale = s.locale(r, req.Locale)
	setContentLanguage(w, req.Locale)
	r = r.WithContext(context.WithValue(i18n.WithLocale(r.Context(), req.Locale), metadataKey{}, req.Metadata))
	user, err := s.resolveUser(w, r)
	if err != nil || user.ID == "" {
		http.Error(w, i18n.T(r.Context(), "server.error.unauthorized"), 401)
		return
	}
	transport := "poll"
	if strings.HasSuffix(r.URL.Path, "_sse") {
		transport = "sse"
	}
	entry, owner, err := s.reserveReplay(r.Context(), user, &req, transport)
	if err != nil {
		http.Error(w, replayErrorText(r.Context(), err), replayStatus(err))
		return
	}
	if transport == "sse" {
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
		err := s.deliver(r.Context(), user, req, entry, owner, transport, func(data json.RawMessage) error {
			start()
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return err
			}
			return ctrl.Flush()
		})
		if err != nil {
			if !started && r.Context().Err() == nil {
				http.Error(w, replayErrorText(r.Context(), err), replayStatus(err))
			}
			return
		}
		start()
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	chunks := []json.RawMessage{}
	err = s.deliver(r.Context(), user, req, entry, owner, transport, func(raw json.RawMessage) error { chunks = append(chunks, raw); return nil })
	if err != nil {
		if r.Context().Err() == nil {
			http.Error(w, replayErrorText(r.Context(), err), replayStatus(err))
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
				"type": "error", "data": map[string]any{"status": 400, "message": i18n.Format(s.locale(r, ""), "server.error.invalid_request"), "code": "invalid_chat_request"},
			}); err != nil {
				return
			}
			continue
		}
		req.Locale = s.locale(r, req.Locale)
		frame := r.WithContext(context.WithValue(i18n.WithLocale(r.Context(), req.Locale), metadataKey{}, req.Metadata))
		authResponse := socketResolverResponse{http.Header{}}
		user, authErr := s.resolveUser(authResponse, frame)
		if authErr != nil || user.ID == "" {
			conn.Close(websocket.StatusPolicyViolation, i18n.T(frame.Context(), "server.error.unauthorized"))
			return
		}
		r = withResolverCookies(r, authResponse.header)
		const transport = "websocket"
		entry, owner, err := s.reserveReplay(frame.Context(), user, &req, transport)
		if err == nil {
			err = s.deliver(frame.Context(), user, req, entry, owner, transport, func(raw json.RawMessage) error {
				return conn.Write(frame.Context(), websocket.MessageText, raw)
			})
		}
		if err != nil {
			var p *replayProblem
			if !errors.As(err, &p) {
				return
			}
			body := map[string]any{"status": p.status, "message": replayErrorText(frame.Context(), err)}
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
