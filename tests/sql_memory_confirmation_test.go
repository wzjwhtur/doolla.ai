package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	doolla "doolla.ai/sdk"
)

// Exercise the HTTP identity resolver, actual SQLite SQLTool, transformed SQL,
// registry policy, persisted examples, replay and both tenant stores together.
func TestConfirmedMemoryTenantHTTP(t *testing.T) {
	db, err := doolla.OpenWritableSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	alpha, beta := doolla.NewLocalMemory(8), doolla.NewLocalMemory(8)
	router, err := doolla.NewTenantMemoryStore(map[string]doolla.MemoryStore{"alpha": alpha, "beta": beta})
	if err != nil {
		t.Fatal(err)
	}
	var tamper atomic.Bool
	hook := doolla.Hooks{BeforeTool: func(_ context.Context, _ doolla.Turn, tool doolla.Tool, args json.RawMessage) (json.RawMessage, error) {
		switch tool.Spec().Name {
		case "run_sql":
			return json.RawMessage(`{"sql":"SELECT 42 AS verified"}`), nil
		case "save_question_tool_args":
			if tamper.Load() {
				return json.RawMessage(`{"tool_name":"run_sql","question":"injected","args":{"sql":"SELECT 7"}}`), nil
			}
		}
		return args, nil
	}}
	model := testModel(func(_ context.Context, messages []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
		if messages[len(messages)-1].Role == "tool" {
			return doolla.Message{Content: "done"}, nil
		}
		fn, args := "run_sql", `{"sql":"SELECT 1"}`
		if messages[len(messages)-1].Content == "save it" {
			fn, args = "save_question_tool_args", `{"tool_name":"run_sql","question":"fake","args":{"sql":"SELECT 7"}}`
		}
		return doolla.Message{ToolCalls: []doolla.ToolCall{{ID: "call", Type: "function", Function: doolla.FunctionCall{Name: fn, Arguments: args}}}}, nil
	})
	var contexts = make(map[string]context.Context)
	readContext := func(ctx context.Context, t doolla.Turn) (context.Context, error) {
		tenant := t.User.Metadata["tenant_id"].(string)
		contexts[tenant] = context.WithoutCancel(ctx)
		return ctx, nil
	}
	regs := append(doolla.MemoryTools(router), doolla.Registration{Tool: doolla.SQLTool{DB: db, DisableArtifacts: true}})
	a, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{Memory: router, ConfirmSQLMemory: true, UIFeatures: map[string][]string{"sql_query": {"sql_view"}}, Hooks: []doolla.Hooks{hook}, Enrichers: []func(context.Context, doolla.Turn) (context.Context, error){readContext}}, regs...)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(doolla.NewServer(a, func(_ http.ResponseWriter, r *http.Request) (doolla.User, error) {
		// Fixed test credentials simulate verified identity-provider claims.
		tenant := map[string]string{"Bearer a": "alpha", "Bearer b": "beta"}[r.Header.Get("Authorization")]
		groups := []string{"admin"}
		if tenant == "alpha" {
			groups = append(groups, "sql_view")
		}
		return doolla.TenantUser(tenant, doolla.User{ID: "same-subject", Groups: groups})
	}))
	defer srv.Close()
	post := func(token, conversation, request, msg string) string {
		t.Helper()
		b, e := json.Marshal(map[string]any{"message": msg, "request_id": request, "conversation_id": conversation, "metadata": map[string]any{"tenant_id": "alpha"}})
		if e != nil {
			t.Fatal(e)
		}
		req, e := http.NewRequest("POST", srv.URL+"/api/doolla/v2/chat_poll", bytes.NewReader(b))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, e := srv.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		raw, e := io.ReadAll(resp.Body)
		if e != nil || resp.StatusCode != 200 {
			t.Fatalf("HTTP %d %s %v", resp.StatusCode, raw, e)
		}
		return string(raw)
	}
	post("a", "one", "model-save", "save it")
	offered := post("a", "one", "orig", "count the parts")
	action := regexp.MustCompile(`/confirm-sql [a-zA-Z0-9-]+`).FindString(offered)
	if action == "" || !strings.Contains(offered, "SELECT 42 AS verified") {
		t.Fatal(offered)
	}
	recent := func(store doolla.MemoryStore) []doolla.MemoryEntry {
		t.Helper()
		entries, e := store.Recent(t.Context(), "tool", 10)
		if e != nil {
			t.Fatal(e)
		}
		return entries
	}
	post("b", "one", "orig", action) // same subject, room and request ID, different tenant
	post("a", "other", "wrong-room", action)
	tamper.Store(true)
	post("a", "one", "mutated", action)
	if len(recent(alpha))+len(recent(beta)) != 0 {
		t.Fatal("example written before valid confirmation")
	}
	tamper.Store(false)
	post("a", "one", "approved", action)
	post("a", "one", "retry", action)
	saved := recent(alpha)
	principal, _ := doolla.TenantUser("alpha", doolla.User{ID: "same-subject"})
	if len(saved) != 1 || saved[0].Question != "count the parts" || saved[0].Args["sql"] != "SELECT 42 AS verified" || saved[0].Metadata["confirmed_by"] != principal.ID || saved[0].Metadata["source_request_id"] != "orig" {
		t.Fatal(saved)
	}
	if len(recent(beta)) != 0 || strings.Contains(post("b", "one", "recent", "/memories"), "SELECT 42") {
		t.Fatal("tenant history leak")
	}
	other := contexts["beta"]
	q := doolla.MemoryQuery{Kind: "tool", Text: "count the parts", Limit: 1}
	hits, e := router.Search(other, q)
	if e != nil || len(hits) > 0 {
		t.Fatal(hits, e)
	}
	if ok, e := router.Delete(other, "tool", saved[0].ID); e != nil || ok {
		t.Fatal(ok, e)
	}
	if n, e := router.Clear(other, "", time.Time{}); e != nil || n != 0 || len(recent(alpha)) != 1 {
		t.Fatal(n, e)
	}
	if _, e = router.Recent(context.Background(), "tool", 1); e == nil {
		t.Fatal("missing principal accepted")
	}
	hidden := post("b", "one", "hidden", "count the widgets")
	action = regexp.MustCompile(`/confirm-sql [a-zA-Z0-9-]+`).FindString(hidden)
	if action == "" || strings.Contains(hidden, "SELECT 42 AS verified") || !strings.Contains(hidden, "count the widgets") {
		t.Fatal("hidden-SQL confirmation exposed SQL or lost its action/question:", hidden)
	}
	post("b", "one", "confirm-hidden", action)
	entries := recent(beta)
	if len(entries) != 1 || entries[0].Args["sql"] != "SELECT 42 AS verified" || entries[0].Question != "count the widgets" {
		t.Fatal(entries)
	}

}
