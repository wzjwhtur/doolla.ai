package database_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/integrations/database"
)

// Send the complete batch in a single model reply, as happens when the model
// asks to inspect several tables at once. Inspect both model feedback and the
// SQL calls that actually reached the runner.
type retryScript []string

func (script retryScript) Reply(_ context.Context, messages []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
	if messages[len(messages)-1].Role == "tool" {
		return doolla.Message{Content: "done"}, nil
	}
	reply := doolla.Message{}
	for i, query := range script {
		args, err := json.Marshal(map[string]string{"sql": query})
		if err != nil {
			return doolla.Message{}, err
		}
		reply.ToolCalls = append(reply.ToolCalls, doolla.ToolCall{
			ID: fmt.Sprint(i), Type: "function", Function: doolla.FunctionCall{Name: "run_sql", Arguments: string(args)},
		})
	}
	return reply, nil
}

type retryRunner struct {
	mu      sync.Mutex
	byTurn  map[string][]string
	invalid string
}

func (r *retryRunner) RunSQL(ctx context.Context, _ doolla.User, sql string) (doolla.SQLResult, error) {
	turn, _ := doolla.CurrentTurn(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byTurn[turn.RequestID] = append(r.byTurn[turn.RequestID], sql)
	if sql == r.invalid {
		return doolla.SQLResult{}, errors.New("mssql: Invalid column name 'SaleOrderId'.")
	}
	return doolla.SQLResult{}, nil
}

func TestMSSQLRetryFiltersOnlyRejectedOwnerInCurrentTurn(t *testing.T) {
	const invalid = "SELECT TOP 3 m.MoCode, m.SaleOrderId, p.SourceId FROM dbo.Pro_Mo m LEFT JOIN dbo.Pro_PlanOrder p ON p.PlanOrderId=m.PlanOrderId ORDER BY m.CreateDate DESC"
	const repeat = "SELECT TOP 5 [wo].[SaleOrderId], po.SourceNo FROM [dbo].[Pro_Mo] AS [wo] LEFT JOIN dbo.Pro_PlanOrder po ON po.PlanOrderId=wo.PlanOrderId"
	script := retryScript{
		invalid, repeat,
		"SELECT wo.SO FROM dbo.Pro_Mo wo",
		"SELECT x.SaleOrderId FROM dbo.Pro_MPS x",
		"SELECT x.SaleOrderId FROM other.Pro_Mo x",
		"SELECT x.SaleorderId FROM dbo.Pro_Mo x",
		"SELECT 'x.SaleOrderId' FROM dbo.Pro_Mo x",
		"SELECT x.SO FROM dbo.Pro_Mo x /* x.SaleOrderId */",
		"WITH x AS (SELECT SO AS SaleOrderId FROM dbo.Pro_Mo) SELECT x.SaleOrderId FROM x",
		"SELECT x.SO FROM dbo.Pro_Mo x WHERE EXISTS (SELECT x.SaleOrderId FROM dbo.Pro_MPS x)",
	}
	want := append([]string{invalid}, script[2:]...)
	runner := &retryRunner{byTurn: make(map[string][]string), invalid: invalid}
	config := doolla.AgentConfig{Hooks: []doolla.Hooks{{AfterConversation: func(_ context.Context, _ doolla.Turn, c doolla.Conversation) error {
		var results []string
		for _, msg := range c.Messages {
			if msg.Role == "user" {
				results = nil // The store may also contain a completed earlier turn.
			}
			if msg.Role == "tool" {
				results = append(results, msg.Content)
			}
		}
		if len(results) != len(script) || !strings.Contains(results[0], "inspect_mssql_schema") ||
			!strings.Contains(results[1], "already rejected dbo.Pro_Mo.SaleOrderId") {
			return fmt.Errorf("unexpected SQL tool feedback: %v", results)
		}
		return nil
	}}}}
	database.ConfigureMSSQLRetry(&config)
	agent, err := doolla.NewConfiguredAgent(script, config, doolla.Registration{Tool: doolla.SQLTool{Runner: runner}})
	if err != nil {
		t.Fatal(err)
	}
	chat := func(id string) {
		if err := agent.Chat(t.Context(), doolla.User{ID: "user"}, id, "describe orders", func(doolla.Component) error { return nil }); err != nil {
			t.Error(err)
		}
	}
	var group sync.WaitGroup
	for _, id := range []string{"orders", "parallel"} {
		group.Go(func() { chat(id) })
	}
	group.Wait()
	chat("orders")
	if len(runner.byTurn) != 3 {
		t.Fatalf("runner observed %d turns, want 3", len(runner.byTurn))
	}
	for _, queries := range runner.byTurn {
		if !slices.Equal(queries, want) {
			t.Errorf("queries reaching database = %q, want %q", queries, want)
		}
	}
}

func TestMSSQLRetryAmbiguousOwnerOnlyGivesHint(t *testing.T) {
	const ambiguous = "SELECT a.SaleOrderId, b.SaleOrderId FROM dbo.Pro_Mo a JOIN dbo.Pro_MPS b ON a.SO=b.SO"
	script := retryScript{ambiguous, "SELECT m.SaleOrderId FROM dbo.Pro_Mo m", "SELECT s.SaleOrderId FROM dbo.Pro_MPS s"}
	runner := &retryRunner{byTurn: make(map[string][]string), invalid: ambiguous}
	config := doolla.AgentConfig{}
	database.ConfigureMSSQLRetry(&config)
	a, err := doolla.NewConfiguredAgent(script, config, doolla.Registration{Tool: doolla.SQLTool{Runner: runner}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Chat(t.Context(), doolla.User{ID: "test"}, "test", "query orders", func(doolla.Component) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, queries := range runner.byTurn {
		if !slices.Equal(queries, []string(script)) {
			t.Fatalf("ambiguous error hid a possible valid query: %v", queries)
		}
	}
}

func TestMSSQLRetryExpiresAfterDDL(t *testing.T) {
	const invalid = "SELECT m.SaleOrderId FROM dbo.Pro_Mo m"
	script := retryScript{invalid, "ALTER TABLE dbo.Pro_Mo ADD SaleOrderId int", "SELECT x.SaleOrderId FROM dbo.Pro_Mo x"}
	runner := &retryRunner{byTurn: make(map[string][]string), invalid: invalid}
	config := doolla.AgentConfig{}
	database.ConfigureMSSQLRetry(&config)
	a, err := doolla.NewConfiguredAgent(script, config, doolla.Registration{Tool: doolla.SQLTool{Runner: runner}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Chat(t.Context(), doolla.User{ID: "test"}, "test", "query again after DDL", func(doolla.Component) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, queries := range runner.byTurn {
		if !slices.Equal(queries, []string(script)) {
			t.Fatalf("stale guard after DDL: %v", queries)
		}
	}
}
