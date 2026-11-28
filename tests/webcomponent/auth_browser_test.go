package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	doolla "doolla.ai/sdk"
)

// Set CHROME_BIN to run the embedded release against real SSE, polling,
// WebSocket and a second API origin using a fresh browser profile.
func TestEmbeddedBrowserAuthentication(t *testing.T) {
	chrome := os.Getenv("CHROME_BIN")
	if chrome == "" {
		t.Skip("set CHROME_BIN to run the browser integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	finished := make(chan string, 1)
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
		if r.Method == "OPTIONS" {
			return
		}
		if r.Header.Get("X-Account") != "" {
			http.Error(w, "foreign origin received previous credentials", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"chunks":[]}`)
	}))
	defer foreign.Close()
	agent, err := doolla.NewAgent(&doolla.MockModel{Response: "Confirmed"}, "")
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(w http.ResponseWriter, r *http.Request) (doolla.User, error) {
		switch r.URL.Path {
		case "/api/doolla/v2/chat_websocket":
			// Browser WebSocket carries the session cookie on upgrade. Recheck
			// it through the same resolver when the Go server receives a frame.
			c, err := r.Cookie("acceptance_session")
			if err == nil && c.Value == "ws-user" && r.Header.Get("X-Account") == "" {
				return doolla.User{ID: "ws-user"}, nil
			}
		case "/api/doolla/v2/chat_sse":
			if r.Header.Get("X-Account") == "initial" {
				return doolla.User{ID: "analyst"}, nil
			}
		case "/api/doolla/v2/chat_poll":
			if r.Header.Get("X-Account") == "rotated" {
				return doolla.User{ID: "analyst"}, nil
			}
		}
		return doolla.User{}, errors.New("credentials did not match transport")
	}
	mux := http.NewServeMux()
	mux.Handle("/", doolla.NewServer(agent, resolver))
	mux.HandleFunc("GET /verify", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "acceptance_session", Value: "ws-user", Path: "/", HttpOnly: true})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><script type="module">
import {DoollaChat} from "/assets/doolla-components.js";
try {
  const chat = new DoollaChat();
  chat.customHeaders = {"X-Account":"initial"};
  document.body.append(chat);
  await chat.updateComplete;
  const outcomes=[];
  chat.addEventListener("turn-completed",e=>outcomes.push(e.detail.status));
  if (!(await chat.sendMessage("SSE")) || outcomes[0]!=="succeeded") throw Error("SSE response");
  chat.updateApiBaseUrl(location.origin);
  chat.setCustomHeaders({"X-Account":"rotated"});
  const api=chat.getApiClient();
  const result=await api.sendPollMessage({message:"Poll",conversation_id:"poll"});
  if (!result.chunks.some(c=>c.rich.type==="turn_outcome" && c.rich.data.status==="succeeded")) throw Error("poll response");
  const ws=await api.createWebSocketConnection();
  try {
    const frames=await api.sendWebSocketMessage(ws,{message:"WS",conversation_id:"ws"});
    let outcome;
    for await (const f of frames) if(f.rich.type==="turn_outcome") outcome=f.rich.data.status;
    if(outcome!=="succeeded") throw Error("WebSocket session");
  } finally { ws.close() }
  chat.pollEndpoint=%q+"/foreign";
  await chat.getApiClient().sendPollMessage({message:"absolute endpoint"});
  chat.updateApiBaseUrl(%q);
  await chat.getApiClient().sendPollMessage({message:"different origin"});
  await fetch("/report",{method:"POST",body:"passed"});
} catch(e) { await fetch("/report",{method:"POST",body:String(e)}); }
</script>`, foreign.URL, foreign.URL)
	})
	mux.HandleFunc("POST /report", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		finished <- string(body)
	})
	host := httptest.NewServer(mux)
	defer host.Close()
	command := exec.CommandContext(ctx, chrome, "--headless", "--disable-gpu", "--disable-extensions", "--disable-background-networking", "--user-data-dir="+t.TempDir(), host.URL+"/verify")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		// Let Chrome stop its profile writers before testing removes the directory.
		_ = command.Process.Signal(os.Interrupt)
		stopped := make(chan struct{})
		go func() { _ = command.Wait(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(4 * time.Second):
			cancel()
			<-stopped
		}
	}()
	select {
	case result := <-finished:
		if strings.TrimSpace(result) != "passed" {
			t.Fatal(result)
		}
	case <-ctx.Done():
		t.Fatal("browser acceptance:", ctx.Err())
	}
}
