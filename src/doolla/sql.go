package doolla

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"doolla.ai/sdk/internal/i18n"
)

type SQLTool struct {
	DisableArtifacts  bool
	DB                *sql.DB
	Artifacts         ResultFiles
	Runner            SQLRunner
	Name, Description string
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
func strField(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func (s SQLTool) Spec() ToolSpec {
	name, desc := s.Name, s.Description
	if name == "" {
		name = "run_sql"
	}
	if desc == "" {
		desc = "Execute SQL on the configured database."
	}
	return ToolSpec{name, desc, objectSchema(map[string]any{"sql": strField("SQL statement")}, "sql")}
}

func sqlStatement(raw json.RawMessage) (string, error) {
	var args struct {
		SQL string `json:"sql"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.SQL) == "" {
		return "", errors.New("sql is required")
	}
	return args.SQL, nil
}

func (s SQLTool) Run(ctx context.Context, u User, raw json.RawMessage) (ToolResult, error) {
	query, err := sqlStatement(raw)
	if err != nil {
		return ToolResult{}, err
	}
	runner := s.Runner
	if runner == nil {
		if s.DB == nil {
			return ToolResult{}, errors.New("SQL runner is required")
		}
		runner = DatabaseRunner{s.DB}
	}
	result, err := runner.RunSQL(ctx, u, query)
	if err != nil {
		return ToolResult{}, err
	}
	metadata := map[string]any{}
	if words := topLevelWords(query); len(words) > 0 {
		metadata["query_type"] = words[0]
	}
	if len(result.Columns) == 0 {
		text := i18n.T(ctx, "server.query.executed")
		if result.Affected != nil {
			text = i18n.T(ctx, "server.query.affected", *result.Affected)
			metadata["rows_affected"] = *result.Affected
		}
		return ToolResult{Content: text, Metadata: metadata, Component: &Component{Type: "notification", Data: map[string]any{"level": "success", "message": text}, Simple: plainToolText(text)}}, nil
	}
	cols, records := result.Columns, result.Rows
	metadata["results"], metadata["columns"], metadata["row_count"] = records, cols, len(records)
	var csvText strings.Builder
	w := csv.NewWriter(&csvText)
	if err := w.Write(cols); err != nil {
		return ToolResult{}, err
	}
	for _, record := range records {
		fields := make([]string, len(cols))
		for i, col := range cols {
			if value := record[col]; value != nil {
				fields[i] = fmt.Sprint(value)
			}
		}
		if err := w.Write(fields); err != nil {
			return ToolResult{}, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return ToolResult{}, err
	}
	text := csvText.String()
	preview := []rune(text)
	content := string(preview[:min(len(preview), 1000)])
	if len(preview) > 1000 {
		content += i18n.T(ctx, "server.query.truncated")
	}
	description := i18n.T(ctx, "server.query.description", len(records), len(cols))
	var filename string
	if len(records) == 0 {
		description = i18n.T(ctx, "server.query.no_rows")
		content = i18n.T(ctx, "server.query.empty")
	} else if !s.DisableArtifacts {
		files := s.Artifacts
		if files == nil {
			files = Artifacts{Directory: "."}
		}
		filename = "query_results_" + newID() + ".csv"
		if err := files.Write(u, filename, []byte(text)); err != nil {
			return ToolResult{}, err
		}
		metadata["output_file"] = filename
	}
	simpleContent := content
	if filename != "" {
		// The model receipt keeps its stable format across response languages.
		content += "\nResults saved to file: " + filename
		simpleContent += "\n" + i18n.T(ctx, "server.query.saved_file", filename)
	}
	component := Component{Type: "dataframe", Data: map[string]any{
		"title": i18n.T(ctx, "server.query.results_title"), "description": description, "data": browserRows(records), "columns": cols,
		"row_count": len(records), "column_count": len(cols), "column_types": map[string]string{}, "chat_section": "activity",
	}, Simple: plainToolText(simpleContent)}
	return ToolResult{Content: content, Component: &component, Metadata: metadata}, nil
}

// JSON numbers in browser components are parsed as IEEE-754 doubles. Keep
// the full Go types in SDK metadata and the original values in CSV exports.
func browserRows(rows []map[string]any) []map[string]any {
	var browser []map[string]any
	for i, row := range rows {
		var copyRow map[string]any
		for col, v := range row {
			text := exactBrowserNumber(v)
			if text == "" {
				continue
			}
			if browser == nil {
				browser = slices.Clone(rows)
			}
			if copyRow == nil {
				copyRow = maps.Clone(row)
				browser[i] = copyRow
			}
			copyRow[col] = text
		}
	}
	if browser != nil {
		return browser
	}
	return rows
}

func exactBrowserNumber(v any) string {
	const maxSafe = 1<<53 - 1
	switch n := v.(type) {
	case int:
		return exactBrowserNumber(int64(n))
	case uint:
		return exactBrowserNumber(uint64(n))
	case int64:
		if n > maxSafe || n < -maxSafe {
			return strconv.FormatInt(n, 10)
		}
	case uint64:
		if n > maxSafe {
			return strconv.FormatUint(n, 10)
		}
	case json.Number:
		// A decimal or exponent may also encode significant digits or scale.
		if i, err := n.Int64(); err != nil || i > maxSafe || i < -maxSafe {
			return n.String()
		}
	}
	return ""
}

type SQLResult struct {
	Columns  []string
	Rows     []map[string]any
	Affected *int64
}
type SQLRunner interface {
	RunSQL(context.Context, User, string) (SQLResult, error)
}
type DatabaseRunner struct{ DB *sql.DB }

func (r DatabaseRunner) RunSQL(ctx context.Context, u User, query string) (SQLResult, error) {
	if commandWithoutRows(query) {
		res, err := r.DB.ExecContext(ctx, query)
		if err != nil {
			return SQLResult{}, err
		}
		result := SQLResult{}
		if n, err := res.RowsAffected(); err == nil {
			result.Affected = &n
		}
		return result, nil
	}
	rows, err := r.DB.QueryContext(ctx, query)
	if err != nil {
		return SQLResult{}, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return SQLResult{}, err
	}
	names = uniqueColumns(names)
	result := SQLResult{Columns: names, Rows: []map[string]any{}}
	values := make([]any, len(names))
	targets := make([]any, len(names))
	for i := range values {
		targets[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return SQLResult{}, err
		}
		record := make(map[string]any, len(names))
		for i, value := range values {
			switch v := value.(type) {
			case []byte:
				value = string(v)
			case time.Time:
				value = v.Format(time.RFC3339Nano)
			case float64:
				if math.IsInf(v, 0) || math.IsNaN(v) {
					value = fmt.Sprint(v)
				}
			}
			record[names[i]] = value
		}
		result.Rows = append(result.Rows, record)
	}
	return result, rows.Err()
}
func uniqueColumns(src []string) []string {
	out := make([]string, len(src))
	seen := map[string]bool{}
	reserved := map[string]bool{}
	for _, s := range src {
		reserved[s] = true
	}
	for i, s := range src {
		name := s
		if seen[name] {
			for n := 2; ; n++ {
				name = fmt.Sprintf("%s_%d", s, n)
				if !seen[name] && !reserved[name] {
					break
				}
			}
		}
		seen[name] = true
		out[i] = name
	}
	return out
}
