package tests

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"doolla.ai/sdk"
)

type scalarFixture struct{ results []doolla.SQLResult }

func (r *scalarFixture) RunSQL(context.Context, doolla.User, string) (doolla.SQLResult, error) {
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}

func TestScalarAnswersUseCurrentSQLCells(t *testing.T) {
	single := func(col string, value any) doolla.SQLResult {
		return doolla.SQLResult{Columns: []string{col}, Rows: []map[string]any{{col: value}}}
	}
	empty := doolla.SQLResult{Columns: []string{"count"}, Rows: []map[string]any{}}
	text := single("production_date", "2026-11-01")
	twoColumns := doolla.SQLResult{Columns: []string{"key", "value"}, Rows: []map[string]any{{"key": "A", "value": 2}}}
	tabular := []doolla.SQLResult{single("first", int64(41)), twoColumns}
	for _, tc := range []struct {
		name        string
		verified    bool
		results     []doolla.SQLResult
		modelAnswer string
		want        []string
		omit        string
	}{
		{"disagree", true, []doolla.SQLResult{single("total_orders", int64(4104))}, "工单为 0 条。", []string{"4104", "total_orders"}, "工单为 0 条"},
		{"real zero", true, []doolla.SQLResult{single("qty", int64(0))}, "工单为 19 条。", []string{"0", "qty"}, "19"},
		{"wide int", true, []doolla.SQLResult{single("qty", int64(9007199254740993))}, "约九千万", []string{"9007199254740993"}, "九千万"},
		{"decimal", true, []doolla.SQLResult{single("yield_ratio", json.Number("0.973246811119000"))}, "良品率 99%", []string{"0.973246811119000"}, "%"},
		{"driver decimal text", true, []doolla.SQLResult{single("amount", "12345678901234567890.001200")}, "约 1 元", []string{"12345678901234567890.001200"}, "约 1 元"},
		{"null", true, []doolla.SQLResult{single("mean", nil)}, "均值为 0", []string{"NULL"}, "均值为 0"},
		{"no row", true, []doolla.SQLResult{empty}, "工单为 100 条。", []string{"未返回记录"}, "100"},
		{"multiple scalar results", true, []doolla.SQLResult{single("yesterday", 8), single("today", 6)}, "减少 100 条", []string{"yesterday", "8", "today", "6"}, "100"},
		{"mixed results", true, tabular, "请结合上面的明细分析。", []string{"请结合上面的明细分析。"}, ""},
		{"sample then scalar", true, []doolla.SQLResult{twoColumns, single("total", 4104)}, "样本显示有 1 条", []string{"4104", "total", "表格"}, "1 条"},
		{"date clarification", true, []doolla.SQLResult{text}, "需要哪个车间？", []string{"需要哪个车间？"}, ""},
		{"unverified sdk", false, []doolla.SQLResult{single("total_orders", 4104)}, "SDK 自定义回答", []string{"SDK 自定义回答"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index := 0
			model := testModel(func(context.Context, []doolla.Message, []doolla.ToolSpec) (doolla.Message, error) {
				if index < len(tc.results) {
					index++
					return doolla.Message{ToolCalls: []doolla.ToolCall{{ID: "read", Type: "function", Function: doolla.FunctionCall{Name: "run_sql", Arguments: `{"sql":"SELECT value FROM sample"}`}}}}, nil
				}
				return doolla.Message{Content: tc.modelAnswer}, nil
			})
			store := doolla.NewMemoryConversations()
			a, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{VerifyToolAnswers: tc.verified, Conversations: store}, doolla.Registration{Tool: doolla.SQLTool{Runner: &scalarFixture{tc.results}, DisableArtifacts: true}})
			if err != nil {
				t.Fatal(err)
			}
			u := doolla.User{ID: "viewer"}
			var answer string
			if err := a.Chat(t.Context(), u, "chat", "请查询工单数据", func(c doolla.Component) error {
				if c.Type == "text" {
					answer, _ = c.Data["content"].(string)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(answer, want) {
					t.Errorf("answer %q missing %q", answer, want)
				}
			}
			if tc.omit != "" && strings.Contains(answer, tc.omit) {
				t.Fatalf("unexpected %q in %q", tc.omit, answer)
			}
			history, err := store.Load(t.Context(), u, "chat")
			if err != nil || len(history) == 0 || history[len(history)-1].Content != answer {
				t.Fatalf("history disagrees with emitted answer: %v, %v", history, err)
			}
		})
	}
}

func TestSQLDataframePreservesBrowserPrecision(t *testing.T) {
	original := map[string]any{"safe": int64(1<<53 - 1), "large": int64(1<<53 + 1), "negative": int64(-1<<53 - 1), "unsigned": uint64(math.MaxUint64), "decimal": json.Number("0.1234567890123456789"), "safeJSON": json.Number("42")}
	runner := &scalarFixture{[]doolla.SQLResult{{Columns: []string{"safe", "large", "negative", "unsigned", "decimal", "safeJSON"}, Rows: []map[string]any{original}}}}
	raw, err := (doolla.SQLTool{Runner: runner, DisableArtifacts: true}).Run(t.Context(), doolla.User{ID: "viewer"}, []byte(`{"sql":"SELECT * FROM sample"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(raw.Component.Data["data"])
	if err != nil {
		t.Fatal(err)
	}
	var browser []map[string]any
	if err := json.Unmarshal(b, &browser); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"safe": float64(1<<53 - 1), "large": "9007199254740993", "negative": "-9007199254740993", "unsigned": "18446744073709551615", "decimal": "0.1234567890123456789", "safeJSON": float64(42)}
	if len(browser) != 1 || !reflect.DeepEqual(want, browser[0]) {
		t.Fatalf("wire cells %s", b)
	}
	sdk := raw.Metadata["results"].([]map[string]any)[0]
	if sdk["large"] != int64(1<<53+1) || sdk["unsigned"] != uint64(math.MaxUint64) || sdk["decimal"] != json.Number("0.1234567890123456789") {
		t.Fatalf("SDK values changed: %v", sdk)
	}
	if !strings.Contains(raw.Content, "9007199254740991,9007199254740993,-9007199254740993,18446744073709551615,0.1234567890123456789,42") {
		t.Fatalf("CSV preview changed: %q", raw.Content)
	}
}
