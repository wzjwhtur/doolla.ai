package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"doolla.ai/sdk"
)

type chartQueryRunner struct {
	queries []string
}

func (r *chartQueryRunner) RunSQL(_ context.Context, _ doolla.User, query string) (doolla.SQLResult, error) {
	r.queries = append(r.queries, query)
	group := strings.TrimPrefix(query, "SELECT ")
	return doolla.SQLResult{
		Columns: []string{"group", "total"},
		Rows:    []map[string]any{{"group": group, "total": 3}},
	}, nil
}

func TestVisualizeQueryResultBinding(t *testing.T) {
	call := func(id, name, args string) doolla.ToolCall {
		return doolla.ToolCall{ID: id, Function: doolla.FunctionCall{Name: name, Arguments: args}}
	}
	const queryA = `{"sql":"SELECT first"}`
	const queryB = `{"sql":"SELECT second"}`
	for _, tc := range []struct {
		name     string
		queries  []string
		want     string
		replies  []doolla.Message
		validate func(*testing.T, doolla.Message)
	}{
		{
			name:    "single result binds before the next model call",
			queries: []string{"SELECT first"},
			want:    "first",
			replies: []doolla.Message{{ToolCalls: []doolla.ToolCall{
				call("query", "run_sql", queryA), call("chart", "visualize_data", `{"title":"Orders"}`),
			}}},
			validate: func(t *testing.T, m doolla.Message) {
				if m.ToolCallID == "query" && !strings.Contains(m.Content, "Query result ID: result_1") {
					t.Fatalf("query result lacks a selectable ID: %s", m.Content)
				}
			},
		},
		{
			name:    "multiple results require selection and a mistyped filename can be corrected",
			queries: []string{"SELECT first", "SELECT second"},
			want:    "second",
			replies: []doolla.Message{
				{ToolCalls: []doolla.ToolCall{call("query-a", "run_sql", queryA), call("query-b", "run_sql", queryB)}},
				{ToolCalls: []doolla.ToolCall{call("ambiguous", "visualize_data", `{}`)}},
				{ToolCalls: []doolla.ToolCall{call("mistyped", "visualize_data", `{"filename":"query_results_PMWZKRY0XZ6JUDTM6D4B7GHLKM2.csv"}`)}},
				{ToolCalls: []doolla.ToolCall{call("chart", "visualize_data", `{"result_id":"result_2","title":"Orders"}`)}},
			},
			validate: func(t *testing.T, m doolla.Message) {
				switch m.ToolCallID {
				case "query-b":
					if !strings.Contains(m.Content, "Query result ID: result_2") {
						t.Fatalf("second query ID missing: %s", m.Content)
					}
				case "ambiguous", "mistyped":
					if !strings.Contains(m.Content, "Tool error:") || !strings.Contains(m.Content, "result_1, result_2") {
						t.Fatalf("selection error should identify the current results: %s", m.Content)
					}
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &chartQueryRunner{}
			files := doolla.Artifacts{Directory: t.TempDir()}
			replies := tc.replies
			model := testModel(func(_ context.Context, messages []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
				for _, m := range messages {
					if m.Role == "tool" {
						tc.validate(t, m)
					}
				}
				if len(replies) > 0 {
					reply := replies[0]
					replies = replies[1:]
					return reply, nil
				}
				if last := messages[len(messages)-1]; !strings.HasPrefix(last.Content, "Created visualization") {
					t.Fatalf("chart tool did not report success: %+v", last)
				}
				return doolla.Message{Content: "Chart completed."}, nil
			})
			var chartInputs []string
			chart := (doolla.VisualizationTool{Artifacts: files}).WithGenerator(func(rows [][]string, title string) (map[string]any, error) {
				chartInputs = append(chartInputs, rows[1][0])
				return doolla.PlotlyFigure(rows, title)
			})
			agent, err := doolla.NewAgent(model, "", doolla.Registration{Tool: doolla.SQLTool{Runner: runner, Artifacts: files}},
				doolla.Registration{Tool: chart})
			if err != nil {
				t.Fatal(err)
			}
			var charts int
			err = agent.Chat(t.Context(), doolla.User{ID: "viewer"}, "chart", "Plot orders.", func(c doolla.Component) error {
				if c.Type == "chart" {
					charts++
				}
				return nil
			})
			if err != nil || charts != 1 || !slices.Equal(chartInputs, []string{tc.want}) || !slices.Equal(runner.queries, tc.queries) {
				t.Fatalf("charts %d, inputs %v, queries %v, error %v", charts, chartInputs, runner.queries, err)
			}
		})
	}
}

func TestVisualizationResultIDsAreScopedToTurn(t *testing.T) {
	files := doolla.Artifacts{Directory: t.TempDir()}
	user := doolla.User{ID: "viewer"}
	runner := &chartQueryRunner{}
	var calls int
	var actualFile string
	model := testModel(func(_ context.Context, messages []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
		last := messages[len(messages)-1]
		calls++
		tool := func(id, name string, args any) (doolla.Message, error) {
			raw, err := json.Marshal(args)
			return doolla.Message{ToolCalls: []doolla.ToolCall{{ID: id, Function: doolla.FunctionCall{Name: name, Arguments: string(raw)}}}}, err
		}
		switch calls {
		case 1:
			return tool("query", "run_sql", map[string]string{"sql": "SELECT first"})
		case 2:
			_, suffix, _ := strings.Cut(last.Content, "Results saved to file: ")
			actualFile, _, _ = strings.Cut(suffix, "\n")
			if actualFile == "" {
				t.Fatal("missing explicit CSV filename")
			}
			return doolla.Message{Content: "Query finished."}, nil
		case 3:
			return tool("old-id", "visualize_data", map[string]string{"result_id": "result_1"})
		case 4:
			if !strings.Contains(last.Content, "does not identify a result in this turn") {
				t.Fatalf("stale result ID was accepted: %s", last.Content)
			}
			return tool("explicit-file", "visualize_data", map[string]string{"filename": actualFile})
		case 5:
			if !strings.HasPrefix(last.Content, "Created visualization") {
				t.Fatalf("explicit filename did not work: %s", last.Content)
			}
			return doolla.Message{Content: "Chart completed."}, nil
		default:
			return doolla.Message{}, fmt.Errorf("unexpected model call %d", calls)
		}
	})
	agent, err := doolla.NewAgent(model, "", doolla.Registration{Tool: doolla.SQLTool{Runner: runner, Artifacts: files}},
		doolla.Registration{Tool: doolla.VisualizationTool{Artifacts: files}})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"Get a query result.", "Plot the explicit CSV."} {
		if err := agent.Chat(t.Context(), user, "chart", q, func(doolla.Component) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if len(runner.queries) != 1 {
		t.Fatalf("charting reran SQL: %v", runner.queries)
	}
}
