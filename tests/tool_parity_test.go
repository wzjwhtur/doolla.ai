package tests

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"doolla.ai/sdk"
)

func TestRegistryIntrospectionAndAccess(t *testing.T) {
	var r doolla.ToolRegistry
	if err := r.Register(doolla.Registration{Tool: echoTool("private"), Groups: []string{"admin"}}, doolla.Registration{Tool: echoTool("public")}); err != nil {
		t.Fatal(err)
	}
	names := r.ListTools()
	if !slices.Equal(names, []string{"private", "public"}) {
		t.Fatal(names)
	}
	names[0] = "changed"
	reg, ok := r.GetTool("private")
	if !ok || !slices.Equal(reg.Groups, []string{"admin"}) {
		t.Fatal(reg)
	}
	reg.Groups[0] = "guest"
	if _, ok := r.GetTool("missing"); ok || r.ListTools()[0] != "private" {
		t.Fatal("incorrect tool registry snapshot")
	}
	guest := doolla.User{Groups: []string{"guest"}}
	if visible := r.Schemas(guest); len(visible) != 1 || visible[0].Name != "public" {
		t.Fatal(visible)
	}
	if _, err := r.Execute(t.Context(), guest, doolla.ToolCall{Function: doolla.FunctionCall{Name: "private", Arguments: "{}"}}); err == nil {
		t.Fatal("guest executed private tool after inspecting its registration")
	}
}

func TestRealSQLToolAndVisualizationGenerator(t *testing.T) {
	db, err := doolla.OpenWritableSQLite(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		if strings.Contains(err.Error(), "requires a build with CGO_ENABLED=1") {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE orders (amount INTEGER)"); err != nil {
		t.Fatal(err)
	}
	files := doolla.Artifacts{Directory: t.TempDir()}
	user := doolla.User{ID: "tool-parity"}
	tool := doolla.SQLTool{DB: db, Artifacts: files}
	run := func(query, kind string) doolla.ToolResult {
		t.Helper()
		args, err := json.Marshal(map[string]string{"sql": query})
		if err != nil {
			t.Fatal(err)
		}
		res, err := tool.Run(t.Context(), user, args)
		if err != nil || res.Metadata["query_type"] != kind || res.Component.Simple["text"] != res.Content {
			t.Fatalf("%s: %+v %v", query, res, err)
		}
		return res
	}
	empty := run("select amount from orders", "SELECT")
	if empty.Component.Data["description"] != "No rows returned" {
		t.Fatal(empty)
	}
	insert := run("insert into orders values (42)", "INSERT")
	if insert.Metadata["rows_affected"] != int64(1) {
		t.Fatal(insert)
	}
	result := run("-- first row\nselect amount from orders", "SELECT")
	filename := result.Metadata["output_file"].(string)
	figure := map[string]any{"layout": map[string]any{}, "data": []any{}}
	chart := (doolla.VisualizationTool{Artifacts: files}).WithGenerator(func(rows [][]string, title string) (map[string]any, error) {
		if len(rows) != 2 || rows[0][0] != "amount" || rows[1][0] != "42" || title != "Orders" {
			t.Fatalf("unexpected chart input %v %q", rows, title)
		}
		return figure, nil
	})
	args, _ := json.Marshal(map[string]string{"filename": filename, "title": "Orders"})
	visual, err := chart.Run(t.Context(), user, args)
	if err != nil || visual.Component.Data["title"] != "Orders" || visual.Component.Simple["text"] != visual.Content {
		t.Fatal(visual, err)
	}
	if _, ok := figure["chart_type"]; ok {
		t.Fatal("visualization mutated the application's chart")
	}
	if metadata := visual.Metadata["chart"].(map[string]any); len(metadata) != 2 {
		t.Fatal(metadata)
	}
}

func TestWorkspaceCommandOutputChannels(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 interpreter unavailable")
	}
	u := doolla.User{ID: "runner", Groups: []string{"admin"}}
	files := doolla.Artifacts{Directory: t.TempDir()}
	if err := files.Write(u, "run.py", []byte("import sys\nprint('数' * 4010)\nprint('error detail',file=sys.stderr)\nsys.exit(int(sys.argv[1]))\n")); err != nil {
		t.Fatal(err)
	}
	var registry doolla.ToolRegistry
	if err := registry.Register(doolla.PythonToolsWithRunner(doolla.LocalPythonRunner(files, python))...); err != nil {
		t.Fatal(err)
	}
	run := func(code string) (doolla.ToolResult, error) {
		return registry.Execute(t.Context(), u, doolla.ToolCall{Function: doolla.FunctionCall{Name: "run_python_file", Arguments: `{"filename":"run.py","arguments":["` + code + `"]}`}})
	}
	res, err := run("0")
	if err != nil || res.Component.Data["status"] != "success" || !strings.Contains(res.Content, "STDERR:\nerror detail") || !utf8.ValidString(res.Content) {
		t.Fatal(err, res.Component)
	}
	if strings.Count(res.Content, "数") != 3999 || !strings.Contains(res.Content, "数…") {
		t.Fatal("stdout truncation differed from 4000 Unicode characters")
	}
	res, err = run("7")
	if err == nil || !strings.Contains(err.Error(), "exit code 7") || res.Component == nil || res.Component.Data["status"] != "error" {
		t.Fatalf("unexpected nonzero result: %v %v", res.Component, err)
	}
}

func TestFileToolsOverlappingEditsAndSimpleComponents(t *testing.T) {
	var registry doolla.ToolRegistry
	if err := registry.Register(doolla.FileTools(doolla.Artifacts{Directory: t.TempDir()})...); err != nil {
		t.Fatal(err)
	}
	run := func(name, args string) doolla.ToolResult {
		t.Helper()
		res, err := registry.Execute(t.Context(), doolla.User{ID: "editor"}, doolla.ToolCall{Function: doolla.FunctionCall{Name: name, Arguments: args}})
		if err != nil || res.Component == nil || res.Component.Simple["text"] == nil {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		return res
	}
	run("write_file", `{"filename":"a.txt","content":"one\ntwo\nthree\n"}`)
	run("edit_file", `{"filename":"a.txt","edits":[{"start_line":2,"new_content":"a\nb\n"},{"start_line":1,"end_line":2,"new_content":"c\n"}]}`)
	read := run("read_file", `{"filename":"a.txt"}`)
	if read.Content != "c\nb\nthree\n" {
		t.Fatalf("line coordinates after multiline replacement: %q", read.Content)
	}
	run("list_files", `{}`)
	run("search_files", `{"query":"three"}`)
}

func TestRealSQLResultLimits(t *testing.T) {
	db, err := doolla.OpenWritableSQLite(filepath.Join(t.TempDir(), "limits.db"))
	if err != nil {
		if strings.Contains(err.Error(), "requires a build with CGO_ENABLED=1") {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	defer db.Close()
	user := doolla.User{ID: "test"}
	for _, c := range []struct {
		name, sql string
		limit     doolla.SQLResultLimits
		wantRows  int
		truncated bool
	}{
		{"exact rows", "SELECT 1 UNION ALL SELECT 2", doolla.SQLResultLimits{MaxRows: 2}, 2, false},
		{"one extra row", "SELECT 1 UNION ALL SELECT 2", doolla.SQLResultLimits{MaxRows: 1}, 1, true},
		{"exact bytes", "SELECT '雪' AS v", doolla.SQLResultLimits{MaxBytes: 4}, 1, false},
		{"over bytes", "SELECT '雪' AS v UNION ALL SELECT 'a'", doolla.SQLResultLimits{MaxBytes: 5}, 1, true},
		{"first cell too large", "SELECT zeroblob(8000000) AS blob", doolla.SQLResultLimits{MaxBytes: 32}, 0, true},
		{"partial row discarded", "SELECT 'a' AS a, 'b' AS b", doolla.SQLResultLimits{MaxBytes: 3}, 0, true},
		{"empty", "SELECT '雪' AS v WHERE 0", doolla.SQLResultLimits{MaxRows: 1, MaxBytes: 1}, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := doolla.Artifacts{Directory: t.TempDir()}
			args, _ := json.Marshal(map[string]string{"sql": c.sql})
			r, err := (doolla.SQLTool{DB: db, Artifacts: files, Limits: c.limit}).Run(t.Context(), user, args)
			if err != nil {
				t.Fatal(err)
			}
			truncated, _ := r.Metadata["truncated"].(bool)
			if truncated != c.truncated || r.Metadata["row_count"] != c.wantRows || (r.Component.Data["truncated"] == true) != c.truncated {
				t.Fatalf("result=%+v", r)
			}
			if c.truncated && (!strings.Contains(r.Content, "total is unknown") || !strings.Contains(r.Component.Data["description"].(string), "total is unknown")) {
				t.Fatal(r)
			}
			if c.wantRows == 0 && r.Metadata["output_file"] != nil {
				t.Fatal("unexpected artifact for empty preview")
			}
			if c.name == "one extra row" {
				b, err := files.Read(user, r.Metadata["output_file"].(string))
				if err != nil || string(b) != "1\n1\n" {
					t.Fatalf("CSV %q err=%v", b, err)
				}
			}
		})
	}
}

func TestSingleCategoricalChartCounts(t *testing.T) {
	chart, err := doolla.PlotlyFigure([][]string{{"status"}, {"done"}, {""}, {"open"}, {"done"}}, "counts")
	if err != nil {
		t.Fatal(err)
	}
	traces := chart["data"].([]map[string]any)
	if len(traces) != 1 || traces[0]["type"] != "bar" || !slices.Equal(traces[0]["x"].([]string), []string{"done", "open"}) || !slices.Equal(traces[0]["y"].([]int), []int{2, 1}) {
		t.Fatal(traces)
	}
}

// A wrapper that applies access policy before forwarding to DatabaseRunner
// should retain the SQLTool limits, including limits above the SDK default.
func TestSQLToolDelegatedDatabaseRunnerLimits(t *testing.T) {
	db, err := doolla.OpenWritableSQLite(filepath.Join(t.TempDir(), "wrapped.db"))
	if err != nil {
		if strings.Contains(err.Error(), "requires a build with CGO_ENABLED=1") {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	defer db.Close()
	tool := doolla.SQLTool{Runner: doolla.DatabaseRunner{DB: db}, Limits: doolla.SQLResultLimits{MaxRows: doolla.DefaultSQLMaxRows + 1}, DisableArtifacts: true}
	r, err := tool.Run(t.Context(), doolla.User{}, []byte(`{"sql":"WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<1001) SELECT i FROM n"}`))
	if err != nil || r.Metadata["row_count"] != 1001 || r.Metadata["truncated"] == true {
		t.Fatalf("delegated rows=%v truncated=%v err=%v", r.Metadata["row_count"], r.Metadata["truncated"], err)
	}
}

func TestCustomRunnerPreviewLimits(t *testing.T) {
	rows := []map[string]any{
		{"term": "雪", "code": nil}, // Four bytes including separator, plus one for NULL.
		{"term": "A", "code": "B"},
	}
	for _, tc := range []struct {
		name              string
		maxRows, maxBytes int
		alreadyTruncated  bool
		wantRows          int
		wantTruncated     bool
	}{
		{"exact result", 2, 9, false, 2, false},
		{"row cap", 1, 9, false, 1, true},
		{"second row crosses byte cap", 2, 8, false, 1, true},
		{"first row crosses byte cap", 2, 4, false, 0, true},
		{"upstream truncated a smaller result", 4, 20, true, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// This runner returns its own materialized rows and never consults SQLTool's limits.
			runner := &scalarFixture{results: []doolla.SQLResult{{Columns: []string{"term", "code"}, Rows: rows, Truncated: tc.alreadyTruncated}}}
			tool := doolla.SQLTool{Runner: runner, Limits: doolla.SQLResultLimits{MaxRows: tc.maxRows, MaxBytes: tc.maxBytes}, DisableArtifacts: true}
			r, err := tool.Run(t.Context(), doolla.User{}, []byte(`{"sql":"SELECT term, code FROM labels"}`))
			if err != nil {
				t.Fatal(err)
			}
			got := r.Metadata["results"].([]map[string]any)
			if !reflect.DeepEqual(got, rows[:tc.wantRows]) || r.Metadata["row_count"] != tc.wantRows {
				t.Fatalf("retained %v with row_count=%v", got, r.Metadata["row_count"])
			}
			if (r.Component.Data["truncated"] == true) != tc.wantTruncated || strings.Contains(r.Content, "total is unknown") != tc.wantTruncated {
				t.Fatalf("unexpected truncation in component or model preview: %+v", r)
			}
		})
	}
}
