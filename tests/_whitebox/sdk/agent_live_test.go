package doolla

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// Run against a tool-capable local model. Each role sends a real HTTP chat
// request through the Go Agent and its read-only SQLite tool.
func TestLiveAgentUIFeatures(t *testing.T) {
	name, endpoint := os.Getenv("DOOLLA_TEST_AGENT_MODEL"), os.Getenv("DOOLLA_TEST_AGENT_URL")
	if name == "" || endpoint == "" {
		t.Skip("set DOOLLA_TEST_AGENT_MODEL and DOOLLA_TEST_AGENT_URL to a local Ollama model and endpoint")
	}
	model, err := NewModel(ModelConfig{Provider: "ollama", Model: name, BaseURL: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenSQLite(sampleDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, group := range []string{"analyst", "guest", "admin"} {
		t.Run(group, func(t *testing.T) {
			want := group == "analyst"
			cfg := AgentConfig{
				SystemPrompt:             "Use run_sql to answer every database question. The SQLite table sales has columns region and value. Use the actual result in the final answer.",
				UIFeatures:               map[string][]string{"tool_names": {"analyst"}, "export_report": {"analyst"}},
				ReplaceDefaultUIFeatures: true,
			}
			// Fail the turn if an invocation receives the wrong feature set.
			cfg.Registry = &ToolRegistry{TransformArgs: func(ctx context.Context, _ User, _ Tool, args json.RawMessage) (json.RawMessage, error) {
				turn, ok := CurrentTurn(ctx)
				if !ok || slices.Contains(turn.UIFeatures, "export_report") != want || slices.Contains(turn.UIFeatures, "tool_arguments") {
					return nil, fmt.Errorf("wrong feature context for %s", group)
				}
				return args, nil
			}}
			a, err := NewConfiguredAgent(model, cfg, Registration{Tool: SQLTool{DB: db, DisableArtifacts: true}})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(NewServer(a, func(http.ResponseWriter, *http.Request) (User, error) {
				return User{ID: group, Groups: []string{group}}, nil
			}))
			defer server.Close()
			client := &http.Client{Timeout: 2 * time.Minute}
			resp, err := client.Post(server.URL+"/api/doolla/v2/chat_poll", "application/json", strings.NewReader(`{"message":"Use run_sql to run SELECT SUM(value) AS total FROM sales and tell me the total."}`))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var result struct{ Chunks []Chunk }
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatal(resp.Status)
			}
			var hasTotal, answered, hasToolTask bool
			added := map[string]bool{}
			for _, c := range result.Chunks {
				data, _ := c.Rich["data"].(map[string]any)
				switch c.Rich["type"] {
				case "dataframe":
					if rows, ok := data["data"].([]any); ok && len(rows) == 1 {
						row, _ := rows[0].(map[string]any)
						for _, value := range row {
							if fmt.Sprint(value) == "10" {
								hasTotal = true
							}
						}
					}
				case "text":
					if content, ok := data["content"].(string); ok && strings.Contains(content, "10") {
						answered = true
					}
				case "task_tracker_update":
					switch data["operation"] {
					case "add_task":
						task, _ := data["task"].(map[string]any)
						id, _ := task["id"].(string)
						added[id] = false
						if task["title"] == "Execute run_sql" {
							hasToolTask = true
						}
					case "update_task":
						id, _ := data["task_id"].(string)
						if _, ok := added[id]; !ok {
							t.Fatal("unknown task", id)
						}
						added[id] = data["status"] == "completed"
					}
				}
			}
			if !hasTotal || !answered || hasToolTask != want || len(added) == 0 {
				t.Fatalf("real SQLite result=%v answer=%v visible tool task=%v tasks=%v", hasTotal, answered, hasToolTask, added)
			}
			for id, complete := range added {
				if !complete {
					t.Fatalf("unfinished task %s", id)
				}
			}
			t.Logf("model=%s role=%s actual sum=10 completed tasks=%d", name, group, len(added))
		})
	}
}
