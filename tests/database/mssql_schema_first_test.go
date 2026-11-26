//go:build cgo

package database_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mattn/go-sqlite3"
	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/integrations/database"
)

type schemaFirstModel func([]doolla.Message) (doolla.Message, error)

func (m schemaFirstModel) Reply(_ context.Context, msgs []doolla.Message, _ []doolla.ToolSpec) (doolla.Message, error) {
	return m(msgs)
}

func sqlCall(id, query string) doolla.ToolCall {
	args, _ := json.Marshal(map[string]string{"sql": query})
	return doolla.ToolCall{ID: id, Type: "function", Function: doolla.FunctionCall{Name: "run_sql", Arguments: string(args)}}
}

func TestMSSQLSchemaFirstNewDictionaryInMixedBatch(t *testing.T) {
	db := newMSSQLCatalogFixture(t)
	_, err := db.Exec(`
INSERT INTO sys.schemas VALUES (1,'dbo'), (2,'mes');
INSERT INTO sys.tables VALUES (10,1,'Base_CommonType'), (20,2,'Base_CommonType');
INSERT INTO sys.columns (object_id,column_id,name) VALUES (10,1,'TypeName'), (20,1,'TypeCode'), (20,2,'CommonTypeName');
CREATE TABLE Base_CommonType (TypeCode TEXT, CommonTypeName TEXT);
INSERT INTO Base_CommonType VALUES ('MO','工单');
`)
	if err != nil {
		t.Fatal(err)
	}
	// SQL Server resolves a one-part name using the current account's default
	// schema. Here dbo contains a decoy column, while the account uses mes.
	installNameResolution(t, db)
	const original = "SELECT * FROM Base_CommonType WHERE TypeCode LIKE '%MO%' OR TypeCode LIKE '%Mo%' OR TypeName LIKE '%工单%' ORDER BY TypeCode"
	const corrected = "SELECT CommonTypeName FROM Base_CommonType WHERE TypeCode LIKE '%MO%' ORDER BY TypeCode"
	const independent = "SELECT 7 AS already_known"
	model := schemaFirstModel(func(msgs []doolla.Message) (doolla.Message, error) {
		last := msgs[len(msgs)-1]
		switch last.ToolCallID {
		case "":
			return doolla.Message{ToolCalls: []doolla.ToolCall{sqlCall("dict", original), sqlCall("other", independent)}}, nil
		case "other":
			if len(msgs) < 3 {
				return doolla.Message{}, fmt.Errorf("incomplete batch: %v", msgs)
			}
			assistant, dictionary := msgs[len(msgs)-3], msgs[len(msgs)-2]
			if len(assistant.ToolCalls) != 2 || assistant.ToolCalls[0].Function.Name != "inspect_mssql_schema" ||
				assistant.ToolCalls[1] != sqlCall("other", independent) || dictionary.ToolCallID != "dict" ||
				!strings.Contains(dictionary.Content, `"schema":"mes"`) || !strings.Contains(dictionary.Content, `"CommonTypeName"`) ||
				strings.Contains(dictionary.Content, `"TypeName"`) || !strings.Contains(last.Content, "already_known\n7") {
				return doolla.Message{}, fmt.Errorf("unexpected tool-call history: %v, %s, %s", assistant.ToolCalls, dictionary.Content, last.Content)
			}
			// Repeat the proposed filter after inspection to verify the known
			// invalid predicate still cannot reach the data runner.
			return doolla.Message{ToolCalls: []doolla.ToolCall{sqlCall("still_bad", original)}}, nil
		case "still_bad":
			var report struct {
				Deferred struct {
					Unknown []string `json:"unknown_columns"`
				} `json:"deferred_sql"`
			}
			if err := json.Unmarshal([]byte(last.Content), &report); err != nil || !slices.Equal(report.Deferred.Unknown, []string{"TypeName"}) {
				return doolla.Message{}, fmt.Errorf("missing invalid predicate detail: %s", last.Content)
			}
			return doolla.Message{ToolCalls: []doolla.ToolCall{sqlCall("corrected", corrected)}}, nil
		case "corrected":
			if !strings.Contains(last.Content, "CommonTypeName\n工单") {
				return doolla.Message{}, fmt.Errorf("corrected SQL result: %s", last.Content)
			}
			return doolla.Message{Content: "工单"}, nil
		}
		return doolla.Message{}, fmt.Errorf("unexpected reply: %v", last)
	})
	var mu sync.Mutex
	var executed []string
	config := doolla.AgentConfig{Hooks: []doolla.Hooks{{BeforeTool: func(_ context.Context, _ doolla.Turn, tool doolla.Tool, raw json.RawMessage) (json.RawMessage, error) {
		if tool.Spec().Name == "run_sql" {
			var a struct{ SQL string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, err
			}
			mu.Lock()
			executed = append(executed, a.SQL)
			mu.Unlock()
		}
		return raw, nil
	}}}}
	database.ConfigureMSSQLSchemaFirst(&config, db)
	a, err := doolla.NewConfiguredAgent(model, config, doolla.Registration{Tool: doolla.SQLTool{DB: db, DisableArtifacts: true}}, doolla.Registration{Tool: database.MSSQLSchemaTool{DB: db}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, id := range []string{"first", "parallel"} {
		wg.Go(func() {
			if err := a.Chat(t.Context(), doolla.User{ID: "u"}, id, "近一个月有哪些工单，分布是什么样的", func(doolla.Component) error { return nil }); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	// Start another turn in the original conversation. Old catalog results
	// persisted in its transcript must not bypass a fresh request's inspection.
	if err := a.Chat(t.Context(), doolla.User{ID: "u"}, "first", "再查一次", func(doolla.Component) error { return nil }); err != nil {
		t.Fatal(err)
	}
	slices.Sort(executed)
	want := []string{independent, independent, independent, corrected, corrected, corrected}
	slices.Sort(want)
	if !slices.Equal(executed, want) {
		t.Fatalf("run_sql received %v, want %v", executed, want)
	}
}

func installNameResolution(t *testing.T, db *sql.DB) {
	t.Helper()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	err = conn.Raw(func(raw any) error {
		c := raw.(*sqlite3.SQLiteConn)
		for name, f := range map[string]any{
			"QUOTENAME": func(s string) string { return "[" + strings.ReplaceAll(s, "]", "]]") + "]" },
			"OBJECT_ID": func(name, kind string) any {
				if name == "[Base_CommonType]" && kind == "U" {
					return int64(20)
				}
				return nil
			},
			"OBJECT_SCHEMA_NAME": func(id int64) string {
				if id == 20 {
					return "mes"
				}
				return ""
			},
			"OBJECT_NAME": func(id int64) string {
				if id == 20 {
					return "Base_CommonType"
				}
				return ""
			},
		} {
			if err := c.RegisterFunc(name, f, true); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMSSQLSchemaFirstHonorsToolVisibility(t *testing.T) {
	db := newMSSQLCatalogFixture(t)
	cfg := doolla.AgentConfig{}
	database.ConfigureMSSQLSchemaFirst(&cfg, db)
	const query = "SELECT name FROM main.sqlite_master"
	model := schemaFirstModel(func(msgs []doolla.Message) (doolla.Message, error) {
		last := msgs[len(msgs)-1]
		if last.Role != "tool" {
			return doolla.Message{ToolCalls: []doolla.ToolCall{sqlCall("q", query)}}, nil
		}
		if calls := msgs[len(msgs)-2].ToolCalls; len(calls) != 1 || calls[0] != sqlCall("q", query) || strings.HasPrefix(last.Content, "Tool error") {
			return doolla.Message{}, fmt.Errorf("hidden metadata tool affected SQL: %v, %s", calls, last.Content)
		}
		return doolla.Message{Content: "done"}, nil
	})
	a, err := doolla.NewConfiguredAgent(model, cfg, doolla.Registration{Tool: doolla.SQLTool{DB: db, DisableArtifacts: true}}, doolla.Registration{Tool: database.MSSQLSchemaTool{DB: db}, Groups: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Chat(t.Context(), doolla.User{ID: "reader"}, "limited", "query", func(doolla.Component) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestMSSQLSchemaFirstRejectsMissingProjectionAfterInspection(t *testing.T) {
	db := newMSSQLCatalogFixture(t)
	_, err := db.Exec(`
INSERT INTO sys.schemas VALUES (1,'dbo');
INSERT INTO sys.tables VALUES (10,1,'Pro_Mo');
INSERT INTO sys.columns (object_id,column_id,name) VALUES (10,1,'MoCode'),(10,2,'MoQty'),(10,3,'CompletionQty'),
(10,4,'Status'),(10,5,'WorkShopId'),(10,6,'CreateDate');
`)
	if err != nil {
		t.Fatal(err)
	}
	const invalid = "SELECT TOP 20 MoCode, MoType, MoQty, CompletionQty, Status, WorkShopId, CreateDate FROM dbo.Pro_Mo WHERE CreateDate >= DATEADD(month, -1, CAST(GETDATE() AS DATE)) AND CreateDate < DATEADD(month, 1, CAST(GETDATE() AS DATE)) ORDER BY CreateDate DESC"
	corrected := strings.Replace(invalid, "MoType, ", "", 1)
	// MoType is a legal output alias here. The SQL Server catalog uses
	// case-insensitive collation in this fixture, so mocode is also valid.
	const alias = "SELECT TOP 20 mocode, COALESCE(MoQty, 0) AS MoType FROM dbo.Pro_Mo ORDER BY MoType"
	model := schemaFirstModel(func(msgs []doolla.Message) (doolla.Message, error) {
		last := msgs[len(msgs)-1]
		switch last.ToolCallID {
		case "":
			call := doolla.ToolCall{ID: "inspect", Type: "function", Function: doolla.FunctionCall{Name: "inspect_mssql_schema", Arguments: `{"tables":[{"schema":"dbo","name":"Pro_Mo"}]}`}}
			return doolla.Message{ToolCalls: []doolla.ToolCall{call}}, nil
		case "inspect":
			if !strings.Contains(last.Content, `"MoCode"`) {
				return doolla.Message{}, fmt.Errorf("initial catalog: %s", last.Content)
			}
			return doolla.Message{ToolCalls: []doolla.ToolCall{sqlCall("bad", invalid)}}, nil
		case "bad":
			var report struct {
				Deferred struct {
					SQL     string   `json:"sql"`
					Unknown []string `json:"unknown_columns"`
				} `json:"deferred_sql"`
			}
			if err := json.Unmarshal([]byte(last.Content), &report); err != nil || report.Deferred.SQL != invalid || len(report.Deferred.Unknown) != 1 || !strings.HasSuffix(report.Deferred.Unknown[0], "MoType") {
				return doolla.Message{}, fmt.Errorf("missing actionable metadata for invalid projection: %s", last.Content)
			}
			return doolla.Message{ToolCalls: []doolla.ToolCall{sqlCall("correct", corrected), sqlCall("alias", alias)}}, nil
		case "alias":
			return doolla.Message{Content: "done"}, nil
		}
		return doolla.Message{}, fmt.Errorf("unexpected tool result: %s", last.Content)
	})
	received := schemaFirstSQL(t, db, model)
	if !slices.Equal(received, []string{corrected, alias}) {
		t.Fatalf("unexpected SQL reaching the runner: %v", received)
	}
}

func TestMSSQLSchemaFirstPreservesMixedStatementBatch(t *testing.T) {
	// A SQL Server batch may omit semicolons between statements. Exercise the
	// scheduling contract with a recording runner; no database is mutated.
	const batch = "SELECT MoId FROM dbo.Pro_Mo DELETE FROM dbo.Staging WHERE Processed = 1"
	script := retryScript{batch}
	runner := &retryRunner{byTurn: make(map[string][]string)}
	cfg := doolla.AgentConfig{}
	database.ConfigureMSSQLSchemaFirst(&cfg, nil)
	a, err := doolla.NewConfiguredAgent(script, cfg,
		doolla.Registration{Tool: doolla.SQLTool{Runner: runner, DisableArtifacts: true}},
		doolla.Registration{Tool: database.MSSQLSchemaTool{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Chat(t.Context(), doolla.User{ID: "u"}, "mixed", "process", func(doolla.Component) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(runner.byTurn) != 1 {
		t.Fatalf("batch was substituted: %v", runner.byTurn)
	}
	for _, received := range runner.byTurn {
		if !slices.Equal(received, []string{batch}) {
			t.Fatalf("batch changed: %v", received)
		}
	}
}

func TestMSSQLSchemaFirstIntegerDictionaryFilter(t *testing.T) {
	db := newMSSQLCatalogFixture(t)
	_, err := db.Exec(`
INSERT INTO sys.schemas VALUES (1,'dbo');
INSERT INTO sys.tables VALUES (10,1,'Base_CommonType');
INSERT INTO sys.columns (object_id,column_id,name,system_type_id,user_type_id) VALUES
(10,1,'CommonTypeCode',231,231),(10,2,'CommonTypeName',231,231),(10,3,'TypeCode',56,1001),
(10,4,'SortCode',56,56),(10,5,'Value1',231,231),(10,6,'Value2',231,231);
`)
	if err != nil {
		t.Fatal(err)
	}
	const invalid = "SELECT CommonTypeCode, CommonTypeName, TypeCode, SortCode, Value1 FROM dbo.Base_CommonType WHERE TypeCode IN ('MoStatus','Status') OR CommonTypeCode LIKE '%Mo%' OR CommonTypeName LIKE '%工单%' ORDER BY TypeCode, SortCode"
	const anotherInvalid = "SELECT TOP 40 CommonTypeCode, CommonTypeName, TypeCode, Value1, Value2 FROM dbo.Base_CommonType WHERE TypeCode IN ('Mo','MoStatus','Status','OrderStatus') OR CommonTypeCode IN ('Mo','MoStatus','OrderStatus') ORDER BY TypeCode, SortCode;"
	const validNumeric = "SELECT CommonTypeName FROM dbo.Base_CommonType WHERE TypeCode IN ('1', '002', '-3', '+4')"
	const validText = "SELECT CommonTypeName FROM dbo.Base_CommonType WHERE CommonTypeCode IN ('Status', 'OrderStatus')"
	const explicitConversion = "SELECT TypeCode FROM dbo.Base_CommonType WHERE CAST(TypeCode AS varchar(20)) IN ('MoStatus', 'Status')"
	checkDeferred := func(m doolla.Message, query string) error {
		var report struct {
			Deferred struct {
				SQL     string
				Filters []struct {
					Column  string
					Type    string `json:"sql_type"`
					Literal string
				} `json:"invalid_integer_filters"`
			} `json:"deferred_sql"`
		}
		if err := json.Unmarshal([]byte(m.Content), &report); err != nil {
			return err
		}
		if report.Deferred.SQL != query || len(report.Deferred.Filters) == 0 {
			return fmt.Errorf("missing original SQL or integer diagnostics: %s", m.Content)
		}
		for _, f := range report.Deferred.Filters {
			if f.Column != "TypeCode" || f.Type != "int" || !strings.Contains(query, "'"+f.Literal+"'") {
				return fmt.Errorf("unexpected column, type or literal: %s", m.Content)
			}
		}
		return nil
	}
	model := schemaFirstModel(func(msgs []doolla.Message) (doolla.Message, error) {
		last := msgs[len(msgs)-1]
		switch last.ToolCallID {
		case "":
			return doolla.Message{ToolCalls: []doolla.ToolCall{{ID: "schema", Type: "function", Function: doolla.FunctionCall{Name: "inspect_mssql_schema", Arguments: `{"tables":[{"schema":"dbo","name":"Base_CommonType"}]}`}}}}, nil
		case "schema":
			var catalog struct {
				Tables []struct {
					Types map[string]string `json:"column_types"`
				}
			}
			if err := json.Unmarshal([]byte(last.Content), &catalog); err != nil || len(catalog.Tables) != 1 ||
				catalog.Tables[0].Types["TypeCode"] != "int" || catalog.Tables[0].Types["CommonTypeCode"] != "nvarchar" {
				return doolla.Message{}, fmt.Errorf("unexpected base types from catalog: %s", last.Content)
			}
			return doolla.Message{ToolCalls: []doolla.ToolCall{sqlCall("bad", invalid)}}, nil
		case "bad":
			if err := checkDeferred(last, invalid); err != nil {
				return doolla.Message{}, err
			}
			return doolla.Message{ToolCalls: []doolla.ToolCall{sqlCall("otherBad", anotherInvalid)}}, nil
		case "otherBad":
			if err := checkDeferred(last, anotherInvalid); err != nil {
				return doolla.Message{}, err
			}
			return doolla.Message{ToolCalls: []doolla.ToolCall{sqlCall("numbers", validNumeric), sqlCall("text", validText), sqlCall("cast", explicitConversion)}}, nil
		case "cast":
			return doolla.Message{Content: "done"}, nil
		}
		return doolla.Message{}, fmt.Errorf("unexpected turn %v", last)
	})
	received := schemaFirstSQL(t, db, model)
	if !slices.Equal(received, []string{validNumeric, validText, explicitConversion}) {
		t.Fatalf("unexpected SQL reaching the runner: %v", received)
	}
}

func schemaFirstSQL(t *testing.T, db *sql.DB, model doolla.Model) []string {
	t.Helper()
	cfg := doolla.AgentConfig{}
	database.ConfigureMSSQLSchemaFirst(&cfg, db)
	runner := &retryRunner{byTurn: make(map[string][]string)}
	a, err := doolla.NewConfiguredAgent(model, cfg, doolla.Registration{Tool: doolla.SQLTool{Runner: runner, DisableArtifacts: true}}, doolla.Registration{Tool: database.MSSQLSchemaTool{DB: db}})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Chat(t.Context(), doolla.User{ID: "u"}, "test", "查询工单", func(doolla.Component) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(runner.byTurn) != 1 {
		t.Fatalf("expected one request with SQL executions: %v", runner.byTurn)
	}
	for _, queries := range runner.byTurn {
		return queries
	}
	return nil
}

func TestMSSQLSchemaFirstChecksQualifiedColumnInJoin(t *testing.T) {
	db := newMSSQLCatalogFixture(t)
	_, err := db.Exec(`
INSERT INTO sys.schemas VALUES (1,'dbo');
INSERT INTO sys.tables VALUES (10,1,'Pro_Mo'), (20,1,'Base_Material'), (30,1,'Base_MaterialType');
INSERT INTO sys.columns (object_id,column_id,name) VALUES
(10,1,'MoQty'),(10,2,'MaterialId'),(10,3,'CreateDate'),
(20,1,'MaterialId'),(20,2,'MaterialTypeId'),(20,3,'MaterialTypeName'),
(30,1,'MaterialTypeId'),(30,2,'MaterialTypeCode');
`)
	if err != nil {
		t.Fatal(err)
	}
	const invalid = "SELECT mt.MaterialTypeCode, mt.MaterialTypeName, COUNT(*) AS MoCnt, SUM(mo.MoQty) AS TotalQty FROM dbo.Pro_Mo mo LEFT JOIN dbo.Base_Material m ON mo.MaterialId = m.MaterialId LEFT JOIN dbo.Base_MaterialType mt ON m.MaterialTypeId = mt.MaterialTypeId WHERE mo.CreateDate >= '2026-10-25' AND mo.CreateDate < '2026-11-25' GROUP BY mt.MaterialTypeCode, mt.MaterialTypeName ORDER BY MoCnt DESC"
	// The same name actually exists on a different table in the join. Its alias
	// identifies which metadata to check; the unqualified name has no fixed owner.
	corrected := strings.ReplaceAll(invalid, "mt.MaterialTypeName", "m.MaterialTypeName")
	bare := strings.ReplaceAll(invalid, "mt.MaterialTypeName", "MaterialTypeName")
	model := schemaFirstModel(func(msgs []doolla.Message) (doolla.Message, error) {
		last := msgs[len(msgs)-1]
		switch last.ToolCallID {
		case "":
			return doolla.Message{ToolCalls: []doolla.ToolCall{{ID: "schema", Type: "function", Function: doolla.FunctionCall{Name: "inspect_mssql_schema", Arguments: `{"tables":[{"schema":"dbo","name":"Pro_Mo"},{"schema":"dbo","name":"Base_Material"},{"schema":"dbo","name":"Base_MaterialType"}]}`}}}}, nil
		case "schema":
			return doolla.Message{ToolCalls: []doolla.ToolCall{sqlCall("bad", invalid)}}, nil
		case "bad":
			var report struct {
				Tables   []struct{ Name string }
				Deferred struct {
					Unknown []string `json:"unknown_columns"`
				} `json:"deferred_sql"`
			}
			if err := json.Unmarshal([]byte(last.Content), &report); err != nil || len(report.Tables) != 1 || report.Tables[0].Name != "Base_MaterialType" || !slices.Equal(report.Deferred.Unknown, []string{"MaterialTypeName"}) {
				return doolla.Message{}, fmt.Errorf("incorrect join alias ownership: %s", last.Content)
			}
			return doolla.Message{ToolCalls: []doolla.ToolCall{sqlCall("corrected", corrected), sqlCall("bare", bare)}}, nil
		case "bare":
			return doolla.Message{Content: "done"}, nil
		}
		return doolla.Message{}, fmt.Errorf("unexpected result: %s", last.Content)
	})
	if got := schemaFirstSQL(t, db, model); !slices.Equal(got, []string{corrected, bare}) {
		t.Fatalf("SQL reaching the runner: %v", got)
	}
}
