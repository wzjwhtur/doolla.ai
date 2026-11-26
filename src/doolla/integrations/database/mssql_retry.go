package database

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	doolla "doolla.ai/sdk"
)

type retryContextKey struct{}

type tableColumn struct{ schema, table, column string }

func (c tableColumn) String() string {
	if c.schema != "" {
		return c.schema + "." + c.table + "." + c.column
	}
	return c.table + "." + c.column
}

type invalidColumns struct {
	known map[tableColumn]bool
	query string // Effective arguments after registry transformations, for the current tool call.
}

var invalidColumnMessage = regexp.MustCompile(`^mssql: Invalid column name '([^'\r\n]+)'\.$`)

const schemaHint = "Use inspect_mssql_schema to check the actual columns before retrying."

// ConfigureMSSQLRetry gives each Chat call its own SQL error history. A turn
// executes its tools in sequence; distinct conversations never share this state.
func ConfigureMSSQLRetry(cfg *doolla.AgentConfig) {
	cfg.Enrichers = append(cfg.Enrichers, func(ctx context.Context, _ doolla.Turn) (context.Context, error) {
		return context.WithValue(ctx, retryContextKey{}, &invalidColumns{known: make(map[tableColumn]bool)}), nil
	})
	cfg.Hooks = append(cfg.Hooks, doolla.Hooks{
		Name: "mssql_invalid_column",
		BeforeTool: func(ctx context.Context, _ doolla.Turn, tool doolla.Tool, raw json.RawMessage) (json.RawMessage, error) {
			state, _ := ctx.Value(retryContextKey{}).(*invalidColumns)
			if state == nil || tool.Spec().Name != "run_sql" {
				return raw, nil
			}
			var args struct{ SQL string }
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			state.query = args.SQL
			if len(state.known) > 0 && preservesColumnBindings(args.SQL) {
				stmt := inspectFlatSelect(args.SQL)
				for column := range state.known {
					owners, _ := stmt.resolve(column.column)
					if owners[column] {
						return nil, fmt.Errorf("skipped repeat query: SQL Server already rejected %s in this question. %s", column, schemaHint)
					}
				}
			}
			return raw, nil
		},
		AfterToolResult: func(ctx context.Context, _ doolla.Turn, call doolla.ToolCall, result doolla.ToolResult) (*doolla.ToolResult, error) {
			state, _ := ctx.Value(retryContextKey{}).(*invalidColumns)
			if state == nil || call.Function.Name != "run_sql" {
				return nil, nil
			}
			query := state.query
			state.query = ""
			// A batch can change a table or execution context even if a later
			// statement fails. Never retain the old binding across that call.
			if query != "" && len(state.known) > 0 && !preservesColumnBindings(query) {
				clear(state.known)
			}
			match := invalidColumnMessage.FindStringSubmatch(result.Error)
			if match == nil {
				return nil, nil
			}
			// SQL Server reports the column name without its owning table.
			// Remember a rejection only when every occurrence has one clear owner.
			owners, certain := inspectFlatSelect(query).resolve(match[1])
			if certain && len(owners) == 1 && preservesColumnBindings(query) {
				for column := range owners {
					state.known[column] = true
				}
			}
			result.Content += " " + schemaHint
			return &result, nil
		},
	})
}

// Only retain negative cache entries across reads. SQL Server permits multiple
// statements without semicolons, including DDL following a SELECT.
var readStatement = regexp.MustCompile(`(?i)^SELECT\b`)
var bindingChange = regexp.MustCompile(`(?i)\b(?:ALTER|CREATE|DROP|INTO|USE|EXEC|EXECUTE|REVERT|SET|BEGIN|COMMIT|ROLLBACK|GRANT|DENY|REVOKE)\b`)

func preservesColumnBindings(sql string) bool {
	sql = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
	return readStatement.MatchString(sql) && !strings.Contains(sql, ";") && !bindingChange.MatchString(sql)
}

// A deliberately narrow recognizer for single-scope SELECTs. Unrecognized
// syntax falls through to SQL Server, which remains authoritative.
var tsqlTokens = regexp.MustCompile(`\s+|'(?:''|[^'])*'|\[(?:\]\]|[^\]])+\]|[\pL_][\pL\pN_$]*|[0-9]+|[.,()=+*/<>%-]`)
var bareName = regexp.MustCompile(`^[\pL_][\pL\pN_$]*$`)

func identifier(token string) string {
	if strings.HasPrefix(token, "[") {
		return strings.ReplaceAll(token[1:len(token)-1], "]]", "]")
	}
	if bareName.MatchString(token) {
		return token
	}
	return ""
}

type flatSelect struct {
	expressions   []string
	projectionEnd int
	tables        map[string]tableColumn // SQL qualifier to physical table (column is filled on resolution).
}

func inspectFlatSelect(sql string) flatSelect {
	sql = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
	if strings.Contains(sql, "--") || strings.Contains(sql, "/*") {
		return flatSelect{}
	}
	var tokens []string
	end := 0
	for _, span := range tsqlTokens.FindAllStringIndex(sql, -1) {
		if span[0] != end {
			return flatSelect{} // Includes unsupported quoting, variables and multiple statements.
		}
		end = span[1]
		token := sql[span[0]:end]
		if strings.TrimSpace(token) != "" {
			tokens = append(tokens, token)
		}
	}
	if end != len(sql) || len(tokens) < 4 || !strings.EqualFold(tokens[0], "select") {
		return flatSelect{}
	}
	stmt := flatSelect{tables: make(map[string]tableColumn)}
	depth, from := 0, false
	afterSources := false
	for i := 1; i < len(tokens); i++ {
		token := tokens[i]
		switch strings.ToUpper(token) {
		case "(", ")":
			if token == "(" {
				depth++
			} else {
				depth--
			}
		case "SELECT", "WITH", "UNION", "EXCEPT", "INTERSECT", "APPLY", "PIVOT", "UNPIVOT", "INTO":
			return flatSelect{}
		case ",":
			if from && !afterSources && depth == 0 {
				return flatSelect{} // A comma can introduce another table.
			}
		case "WHERE", "HAVING", "GROUP", "ORDER":
			if depth == 0 && from {
				afterSources = true
			}
		case "FROM", "JOIN":
			if depth != 0 || (strings.EqualFold(token, "from") && from) || (strings.EqualFold(token, "join") && !from) {
				return flatSelect{}
			}
			if !from {
				stmt.projectionEnd = len(stmt.expressions)
			}
			from = true
			i++
			if i >= len(tokens) || identifier(tokens[i]) == "" {
				return flatSelect{}
			}
			table := tableColumn{table: identifier(tokens[i])}
			if i+2 < len(tokens) && tokens[i+1] == "." {
				i += 2
				table.schema, table.table = table.table, identifier(tokens[i])
				if table.table == "" {
					return flatSelect{}
				}
			}
			alias := table.table
			if i+1 < len(tokens) && strings.EqualFold(tokens[i+1], "as") {
				i++
			}
			if i+1 < len(tokens) && !sourceBoundary(tokens[i+1]) {
				i++
				alias = identifier(tokens[i])
				if alias == "" {
					return flatSelect{}
				}
			}
			if i+1 < len(tokens) && !sourceBoundary(tokens[i+1]) {
				return flatSelect{} // Function, derived table, multipart name or other unsupported source.
			}
			if _, exists := stmt.tables[alias]; exists {
				return flatSelect{}
			}
			stmt.tables[alias] = table
			// Keep expression tokens on either side of the declaration separated.
			stmt.expressions = append(stmt.expressions, ",")
			continue
		}
		stmt.expressions = append(stmt.expressions, token)
	}
	return stmt
}

func sourceBoundary(token string) bool {
	switch strings.ToUpper(token) {
	case "JOIN", "INNER", "LEFT", "RIGHT", "FULL", "CROSS", "ON", "WHERE", "ORDER", "GROUP", "HAVING":
		return true
	}
	return false
}

// resolve preserves identifier case for databases with case-sensitive collation.
// Unqualified references in a join and output aliases cannot establish ownership.
func (s flatSelect) resolve(name string) (map[tableColumn]bool, bool) {
	owners := make(map[tableColumn]bool)
	certain := true
	for i, token := range s.expressions {
		if identifier(token) != name {
			continue
		}
		var table tableColumn
		switch {
		case i >= 2 && s.expressions[i-1] == "." && (i < 3 || s.expressions[i-3] != "."):
			table = s.tables[identifier(s.expressions[i-2])]
		case len(s.tables) == 1 && len(s.expressions) == 2 && i == 0:
			// SELECT single_column FROM schema.table
			for _, only := range s.tables {
				table = only
			}
		}
		if table.table == "" || (i+1 < len(s.expressions) && (s.expressions[i+1] == "." || s.expressions[i+1] == "(")) {
			certain = false
			continue
		}
		table.column = name
		owners[table] = true
	}
	return owners, certain
}
