package tests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"doolla.ai/sdk"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestChatLocaleNegotiationAndReplay(t *testing.T) {
	var calls atomic.Int32
	agent, err := doolla.NewConfiguredAgent(&doolla.MockModel{}, doolla.AgentConfig{
		Workflow: func(ctx context.Context, _ doolla.Turn, question string, emit func(doolla.Component) error) (bool, error) {
			if question == "/help" {
				return true, emit(doolla.Component{Type: "text", Data: map[string]any{"content": doolla.CurrentLocale(ctx)}})
			}
			calls.Add(1)
			return true, emit(doolla.Component{Type: "text", Data: map[string]any{"content": doolla.CurrentLocale(ctx)}})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(http.ResponseWriter, *http.Request) (doolla.User, error) {
		return doolla.User{ID: "locale-test"}, nil
	}
	handler := doolla.NewServer(agent, resolve)
	for _, tc := range []struct{ name, locale, header, want string }{
		{"explicit language", "zh-Hans", "en-US", "zh-CN"},
		{"weighted language", "", "fr;q=1, en;q=0.2, zh;q=0.7", "zh-CN"},
		{"ignored language", "", "zh-CN;q=0,en-GB;q=0.6", "en"},
		{"fallback", "fr", "zh-Hant", "en"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"message": "/help", "locale": tc.locale})
			req := httptest.NewRequest(http.MethodPost, "/api/doolla/v2/chat_poll", strings.NewReader(string(body)))
			req.Header.Set("Accept-Language", tc.header)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != 200 || w.Header().Get("Content-Language") != tc.want || !strings.Contains(w.Body.String(), `"content":"`+tc.want+`"`) {
				t.Fatalf("status %d, language %s, body %s", w.Code, w.Header().Get("Content-Language"), w.Body)
			}
		})
	}
	for _, tc := range []struct {
		transport, locale string
		status            int
	}{
		{"chat_sse", "zh-Hans", 200},
		{"chat_poll", "zh-CN", 200},
		{"chat_poll", "en", 409},
	} {
		body, _ := json.Marshal(map[string]string{"message": "same operation", "locale": tc.locale, "request_id": "same"})
		req := httptest.NewRequest(http.MethodPost, "/api/doolla/v2/"+tc.transport, strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.status || (tc.status == 200 && !strings.Contains(w.Body.String(), `"content":"zh-CN"`)) {
			t.Fatalf("transport %s status %d body %s", tc.transport, w.Code, w.Body)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("locale replay executed the workflow %d times", calls.Load())
	}

	server := httptest.NewServer(handler)
	defer server.Close()
	socket, _, err := websocket.Dial(t.Context(), strings.Replace(server.URL, "http", "ws", 1)+"/api/doolla/v2/chat_websocket",
		&websocket.DialOptions{HTTPHeader: http.Header{"Accept-Language": []string{"en"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	for _, locale := range []string{"zh-CN", "en"} {
		err = wsjson.Write(t.Context(), socket, map[string]string{"message": "/help", "locale": locale})
		if err != nil {
			t.Fatal(err)
		}
		var chunk doolla.Chunk
		if err := wsjson.Read(t.Context(), socket, &chunk); err != nil || chunk.Simple["text"] != locale {
			t.Fatalf("websocket locale %s, chunk %+v, error %v", locale, chunk, err)
		}
		var outcome doolla.Chunk
		if err := wsjson.Read(t.Context(), socket, &outcome); err != nil || outcome.Rich["type"] != "turn_outcome" || outcome.RequestID != chunk.RequestID {
			t.Fatalf("websocket business outcome %+v: %v", outcome, err)
		}
		var done map[string]any
		if err := wsjson.Read(t.Context(), socket, &done); err != nil || done["type"] != "completion" {
			t.Fatalf("websocket completion %+v: %v", done, err)
		}
	}
}

func TestPublicHelpAndDocsAreLocalized(t *testing.T) {
	agent, err := doolla.NewConfiguredAgent(&doolla.MockModel{}, doolla.AgentConfig{DefaultLocale: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(doolla.NewServer(agent, nil))
	defer s.Close()
	for _, tc := range []struct{ path, locale, text string }{
		{"/", "zh-CN", `lang="zh-CN"`},
		{"/docs", "zh-CN", "试用轮询请求"},
		{"/docs?locale=en", "en", "Try a polling request"},
		{"/openapi.json", "zh-CN", "可选语言标签"},
	} {
		response, err := s.Client().Get(s.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || response.Header.Get("Content-Language") != tc.locale || !strings.Contains(string(raw), tc.text) {
			t.Fatalf("path %s, status %d, locale %s, missing %q: %v", tc.path, response.StatusCode, response.Header.Get("Content-Language"), tc.text, err)
		}
	}
	response, err := s.Client().Post(s.URL+"/api/doolla/v2/chat_poll", "application/json", strings.NewReader(`{"message":"/help"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || !strings.Contains(string(raw), "您可以询问数据") {
		t.Fatalf("help not translated: %v %s", err, raw)
	}
}
