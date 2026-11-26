// Command webcomponent serves the interactive component tests over the Go chat API.
package main

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	doolla "doolla.ai/sdk"
)

//go:embed index.html
var page []byte

//go:embed components.json
var scenario string

func main() {
	listen := flag.String("listen", "127.0.0.1:5555", "HTTP listen address")
	flag.Parse()
	var actions atomic.Uint64
	workflow := func(ctx context.Context, _ doolla.Turn, _ []doolla.Message, message string, emit func(doolla.Component) error) (doolla.WorkflowDecision, error) {
		handled := doolla.WorkflowDecision{Handled: true}
		switch strings.TrimSpace(message) {
		case "/test", "/test realistic", "/test rapid":
			step := 350 * time.Millisecond
			if strings.TrimSpace(message) == "/test rapid" {
				step = 50 * time.Millisecond
			}
			return handled, runScenario(ctx, step, emit)
		}
		if strings.HasPrefix(message, "/") {
			err := emit(doolla.NewComponent("notification", map[string]any{
				"title": fmt.Sprintf("Action #%d", actions.Add(1)), "message": message, "level": "success",
			}))
			if err != nil {
				return handled, err
			}
			return handled, emit(doolla.NewComponent("card", map[string]any{
				"title": "Action Handler Response", "content": message, "status": "success",
			}))
		}
		return handled, emit(doolla.NewComponent("text", map[string]any{"content": "Type /test to run the component tests."}))
	}
	agent, err := doolla.NewConfiguredAgent(&doolla.MockModel{}, doolla.AgentConfig{ConversationWorkflow: workflow, HideProgress: true})
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	mux.Handle("/", doolla.NewServer(agent, nil))
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Component test page at http://%s", listener.Addr())
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(server.Serve(listener))
}

func runScenario(ctx context.Context, step time.Duration, emit func(doolla.Component) error) error {
	// Each run needs fresh IDs, while updates within a run must reuse their create IDs.
	var events []doolla.Component
	if err := json.Unmarshal([]byte(strings.ReplaceAll(scenario, "run-id/", rand.Text()+"-")), &events); err != nil {
		return err
	}
	ticker := time.NewTicker(step)
	defer ticker.Stop()
	for i, event := range events {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}
