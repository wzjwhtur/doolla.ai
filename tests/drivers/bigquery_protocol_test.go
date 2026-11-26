//go:build doolla_bigquery

package drivers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/integrations/database"
	_ "doolla.ai/sdk/integrations/database/drivers"
)

// This exercises the linked database/sql driver and the BigQuery REST wire
// format against loopback. It does not validate IAM or the Google service.
func TestBigQuerySQLToolLoopbackProtocol(t *testing.T) {
	const query = "SELECT answer, label, active, optional, sequence FROM fixture"
	const schema = `{"fields":[{"name":"answer","type":"INTEGER","mode":"REQUIRED"},{"name":"label","type":"STRING","mode":"REQUIRED"},{"name":"active","type":"BOOLEAN","mode":"REQUIRED"},{"name":"optional","type":"INTEGER","mode":"NULLABLE"},{"name":"sequence","type":"INTEGER","mode":"REPEATED"}]}`
	const row = `{"f":[{"v":"42"},{"v":"工单"},{"v":"true"},{"v":null},{"v":[{"v":"3"},{"v":"4"}]}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/projects/test-project/queries" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body struct{ Query string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || (body.Query != query && body.Query != query+" LIMIT 0") || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected emulator request, SQL %q, decode error %v", body.Query, err)
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// 服务端返回未经过转换的三种字段模式，既覆盖有数据的结果也覆盖仅有 schema 的结果。
		total, rows := "1", row
		if strings.HasSuffix(body.Query, " LIMIT 0") {
			total, rows = "0", ""
		}
		w.Write([]byte(`{"jobComplete":true,"jobReference":{"projectId":"test-project","jobId":"job-1"},"schema":` + schema + `,"totalRows":"` + total + `","rows":[` + rows + `]}`))
	}))
	defer server.Close()
	db, err := database.Open("bigquery", "bigquery://test-project/test_dataset?emulator=true&queryMode=sync&endpoint="+url.QueryEscape(server.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// 先在 database/sql 边界检查原始可空性，再验证用户实际看到的 dataframe。
	// 只检查最终数字会漏掉把 REQUIRED 静默改成 NULLABLE 的回归。
	raw, err := db.QueryContext(ctx, query+" LIMIT 0")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	columns, err := raw.ColumnTypes()
	if err != nil || len(columns) != 5 {
		t.Fatalf("columns %v: %v", columns, err)
	}
	for i, column := range columns {
		if nullable, known := column.Nullable(); !known || nullable != (i == 3) {
			t.Fatalf("column %s nullable=%v known=%v", column.Name(), nullable, known)
		}
	}
	if raw.Next() || raw.Err() != nil {
		t.Fatalf("unexpected empty result: %v", raw.Err())
	}
	for _, empty := range []bool{false, true} {
		sql := query
		if empty {
			sql += " LIMIT 0"
		}
		args, _ := json.Marshal(map[string]string{"sql": sql})
		result, err := (doolla.SQLTool{DB: db, DisableArtifacts: true}).Run(ctx, doolla.User{ID: "tester"}, args)
		if err != nil {
			t.Fatal(err)
		}
		rows, ok := result.Component.Data["data"].([]map[string]any)
		if result.Component.Type != "dataframe" || !ok || result.Metadata["row_count"] != len(rows) {
			t.Fatalf("unexpected dataframe %+v", result)
		}
		if empty {
			if len(rows) != 0 {
				t.Fatalf("expected empty dataframe, got %+v", rows)
			}
			continue
		}
		want := map[string]any{"answer": 42, "label": "工单", "active": true, "optional": nil, "sequence": []int{3, 4}}
		if len(rows) != 1 || !reflect.DeepEqual(rows[0], want) {
			t.Fatalf("unexpected dataframe rows %#v", rows)
		}
	}
}

// 默认 DSN 通过 jobs.insert 提交 SQL，再轮询并分页读取结果；这里覆盖与同步 queries API 不同的入口。
func TestBigQuerySQLToolDefaultJobProtocol(t *testing.T) {
	const query = "SELECT answer FROM fixture ORDER BY answer"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /projects/p/jobs":
			var job struct {
				Configuration struct{ Query struct{ Query string } }
			}
			if err := json.NewDecoder(r.Body).Decode(&job); err != nil || job.Configuration.Query.Query != query {
				t.Errorf("unexpected job SQL %q: %v", job.Configuration.Query.Query, err)
				http.Error(w, "bad job", 400)
				return
			}
			w.Write([]byte(`{"jobReference":{"projectId":"p","jobId":"j"},"status":{"state":"RUNNING"}}`))
		case "GET /projects/p/jobs/j":
			w.Write([]byte(`{"jobReference":{"projectId":"p","jobId":"j"},"status":{"state":"DONE"}}`))
		case "GET /projects/p/queries/j":
			const fields = `"schema":{"fields":[{"name":"answer","type":"INTEGER","mode":"REQUIRED"}]},"totalRows":"2"`
			switch r.URL.Query().Get("pageToken") {
			case "":
				w.Write([]byte(`{"jobComplete":true,` + fields + `,"pageToken":"second","rows":[{"f":[{"v":"41"}]}]}`))
			case "second":
				w.Write([]byte(`{"jobComplete":true,` + fields + `,"rows":[{"f":[{"v":"42"}]}]}`))
			default:
				t.Error("unexpected page token")
				http.Error(w, "bad page", 400)
			}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	db, err := database.Open("bigquery", "bigquery://p/d?emulator=true&endpoint="+url.QueryEscape(server.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	args, _ := json.Marshal(map[string]string{"sql": query})
	result, err := (doolla.SQLTool{DB: db, DisableArtifacts: true}).Run(ctx, doolla.User{ID: "tester"}, args)
	if err != nil {
		t.Fatal(err)
	}
	if result.Component.Type != "dataframe" || !reflect.DeepEqual(result.Component.Data["data"], []map[string]any{{"answer": 41}, {"answer": 42}}) {
		t.Fatalf("unexpected dataframe %+v", result)
	}
}
