package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	doolla "doolla.ai/sdk"
)

const inspectSchema = "inspect_mssql_schema"

// T-SQL also permits a write after a SELECT without a semicolon. Preserve
// these batches and the ordering of any sibling calls in the same response.
var dataChange = regexp.MustCompile(`(?i)\b(?:INSERT|UPDATE|DELETE|MERGE|TRUNCATE|BULK|DBCC)\b`)

type schemaFirstKey struct{}
type schemaFirstTurn struct {
	inspected map[schemaTable]tableDefinition
	resolved  map[string]schemaTable
	deferred  map[string]deferredRead
}

type deferredRead struct {
	SQL                   string          `json:"sql"`
	UnknownColumns        []string        `json:"unknown_columns,omitempty"`
	InvalidIntegerFilters []integerFilter `json:"invalid_integer_filters,omitempty"`
	Instruction           string          `json:"instruction"`
}

type integerFilter struct {
	Column  string `json:"column"`
	SQLType string `json:"sql_type"`
	Literal string `json:"literal"`
}

type schemaRequest struct {
	Tables []schemaTable `json:"tables"`
}

// ConfigureMSSQLSchemaFirst defers a simple read of an unfamiliar table until
// the model has seen its columns. Each Chat has a separate catalog snapshot.
func ConfigureMSSQLSchemaFirst(cfg *doolla.AgentConfig, db *sql.DB) {
	cfg.Enrichers = append(cfg.Enrichers, func(ctx context.Context, _ doolla.Turn) (context.Context, error) {
		state := &schemaFirstTurn{make(map[schemaTable]tableDefinition), make(map[string]schemaTable), make(map[string]deferredRead)}
		return context.WithValue(ctx, schemaFirstKey{}, state), nil
	})
	cfg.Hooks = append(cfg.Hooks, doolla.Hooks{
		Name: "mssql_schema_first",
		AfterToolResult: func(ctx context.Context, _ doolla.Turn, call doolla.ToolCall, result doolla.ToolResult) (*doolla.ToolResult, error) {
			state, _ := ctx.Value(schemaFirstKey{}).(*schemaFirstTurn)
			if state == nil {
				return nil, nil
			}
			switch call.Function.Name {
			case "run_sql":
				if !preservesColumnBindings(sqlArgument(call)) {
					clear(state.inspected)
					clear(state.resolved)
				}
			case inspectSchema:
				note, deferred := state.deferred[call.ID]
				delete(state.deferred, call.ID)
				var report struct{ Tables []tableDefinition }
				var requested schemaRequest
				if result.Error != "" || json.Unmarshal([]byte(result.Content), &report) != nil ||
					json.Unmarshal([]byte(call.Function.Arguments), &requested) != nil {
					break
				}
				// The catalog tool deduplicates exact inputs, preserves their order,
				// and returns the database's spelling on case-insensitive collations.
				seen := make(map[schemaTable]bool)
				i := 0
				for _, table := range requested.Tables {
					if seen[table] || i >= len(report.Tables) {
						continue
					}
					seen[table] = true
					row := report.Tables[i]
					i++
					if row.Status == "inspected" || row.Status == "missing_or_not_visible" {
						state.inspected[table] = row
						state.inspected[row.schemaTable] = row
					}
				}
				if deferred {
					// Preserve the proposed SQL and the exact reason it was deferred
					// in both the conversation and the next model request.
					var body map[string]json.RawMessage
					if err := json.Unmarshal([]byte(result.Content), &body); err != nil {
						return nil, err
					}
					body["deferred_sql"], _ = json.Marshal(note)
					raw, err := json.Marshal(body)
					if err != nil {
						return nil, err
					}
					result.Content = string(raw)
					return &result, nil
				}
			}
			return nil, nil
		},
	})
	cfg.LLMMiddlewares = append(cfg.LLMMiddlewares, doolla.LLMMiddleware{
		Name: "mssql_schema_first",
		AfterResponse: func(ctx context.Context, req doolla.ModelRequest, reply doolla.Message) (doolla.Message, error) {
			state, _ := ctx.Value(schemaFirstKey{}).(*schemaFirstTurn)
			// Some providers replay a signed copy of the original calls. Editing
			// their names would leave the subsequent tool results unmatched.
			if state == nil || len(reply.ToolCalls) == 0 || len(reply.ModelState) != 0 {
				return reply, nil
			}
			if !slices.ContainsFunc(req.Tools, func(t doolla.ToolSpec) bool { return t.Name == inspectSchema }) ||
				!slices.ContainsFunc(req.Tools, func(t doolla.ToolSpec) bool { return t.Name == "run_sql" }) {
				return reply, nil
			}
			scheduled := make(map[schemaTable]bool)
			ids := make(map[string]bool)
			for _, call := range reply.ToolCalls {
				if call.ID == "" || ids[call.ID] {
					return reply, nil // Preserve the agent's normal protocol validation.
				}
				ids[call.ID] = true
				switch call.Function.Name {
				case "run_sql":
					query := sqlArgument(call)
					if !preservesColumnBindings(query) || dataChange.MatchString(query) {
						return reply, nil // Preserve the order of writes and dependent reads.
					}
				case inspectSchema:
					var request schemaRequest
					if json.Unmarshal([]byte(call.Function.Arguments), &request) == nil {
						for _, t := range request.Tables {
							scheduled[t] = true
						}
					}
				}
			}
			calls := make([]doolla.ToolCall, 0, len(reply.ToolCalls))
			for _, call := range reply.ToolCalls {
				if call.Function.Name == "run_sql" {
					var next schemaRequest
					deferred := false
					query := sqlArgument(call)
					if len(query) > 64*1024 {
						calls = append(calls, call)
						continue
					}
					stmt := inspectFlatSelect(query)
					note := deferredRead{SQL: query, Instruction: "The SQL was not executed. Use the inspected column names and types to correct the query. If this table has no suitable column for the requested meaning, omit that dimension and explain why."}
					probes := 8 // Maximum number of confirmation queries for the entire SQL statement.
					for _, t := range state.businessTables(ctx, db, stmt) {
						if definition, ok := state.inspected[t]; ok {
							if definition.Status == "inspected" {
								aliases := state.aliasesFor(stmt, t)
								missing := confirmedMissingColumns(ctx, db, definition, stmt.directColumns(aliases), &probes)
								var invalid []integerFilter
								if len(stmt.tables) == 1 {
									invalid = stmt.integerFilters(definition.ColumnTypes, aliases)
								}
								if len(missing) > 0 || len(invalid) > 0 {
									deferred = true
									next.Tables = append(next.Tables, t)
									note.UnknownColumns = append(note.UnknownColumns, missing...)
									note.InvalidIntegerFilters = append(note.InvalidIntegerFilters, invalid...)
									state.deferred[call.ID] = note
								}
							}
							continue
						}
						deferred = true
						if !scheduled[t] {
							next.Tables = append(next.Tables, t)
							scheduled[t] = true
						}
					}
					if deferred {
						if len(next.Tables) == 0 {
							continue // Another call in this batch will supply this schema.
						}
						raw, err := json.Marshal(next)
						if err != nil {
							return reply, err
						}
						call.Function = doolla.FunctionCall{Name: inspectSchema, Arguments: string(raw)}
					}
				}
				calls = append(calls, call)
			}
			reply.ToolCalls = calls
			return reply, ctx.Err()
		},
	})
}

func sqlArgument(call doolla.ToolCall) string {
	var args struct{ SQL string }
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return ""
	}
	return args.SQL
}

func (s *schemaFirstTurn) businessTables(ctx context.Context, db *sql.DB, stmt flatSelect) []schemaTable {
	sources := stmt.tables
	if len(sources) > schemaBatchSize {
		return nil
	}
	var tables []schemaTable
	for _, source := range sources {
		t := schemaTable{source.schema, source.table}
		if t.Schema == "" {
			t = s.resolved[source.table]
			if t == (schemaTable{}) {
				// Let SQL Server resolve the caller's default schema and dbo
				// fallback. QUOTENAME keeps the bound value a single identifier.
				const resolve = "SELECT OBJECT_SCHEMA_NAME(id), OBJECT_NAME(id) FROM (SELECT OBJECT_ID(QUOTENAME(@p1), 'U') AS id) AS resolved WHERE id IS NOT NULL"
				if db == nil || db.QueryRowContext(ctx, resolve, source.table).Scan(&t.Schema, &t.Name) != nil {
					continue
				}
				s.resolved[source.table] = t
			}
		}
		if strings.EqualFold(t.Schema, "sys") || strings.EqualFold(t.Schema, "INFORMATION_SCHEMA") ||
			strings.HasPrefix(t.Name, "#") || strings.HasPrefix(t.Name, "@") {
			continue
		}
		tables = append(tables, t)
	}
	sort.Slice(tables, func(i, j int) bool {
		if tables[i].Schema == tables[j].Schema {
			return tables[i].Name < tables[j].Name
		}
		return tables[i].Schema < tables[j].Schema
	})
	return slices.Compact(tables)
}

// A qualified projection in a JOIN can only be checked against its own table.
func (s *schemaFirstTurn) aliasesFor(stmt flatSelect, table schemaTable) map[string]bool {
	aliases := make(map[string]bool)
	for alias, source := range stmt.tables {
		key := schemaTable{source.schema, source.table}
		if key.Schema == "" {
			key = s.resolved[source.table]
		}
		if key == table {
			aliases[alias] = true
		}
	}
	return aliases
}

// Confirm apparent misses in the database catalog: Go string comparison cannot
// reproduce the database's case/accent rules and the schema may have changed.
func confirmedMissingColumns(ctx context.Context, db *sql.DB, table tableDefinition, names []string, remaining *int) []string {
	if db == nil {
		return nil
	}
	const lookup = `SELECT COUNT(c.column_id) FROM sys.tables t
JOIN sys.schemas s ON s.schema_id = t.schema_id
LEFT JOIN sys.columns c ON c.object_id = t.object_id AND c.name = @p3
WHERE s.name = @p1 AND t.name = @p2 GROUP BY t.object_id`
	var missing []string
	for _, name := range names {
		if slices.Contains(table.Columns, name) {
			continue
		}
		if *remaining == 0 {
			break
		}
		*remaining--
		var count int
		if err := db.QueryRowContext(ctx, lookup, sql.Named("p1", table.Schema), sql.Named("p2", table.Name), sql.Named("p3", name)).Scan(&count); err == nil && count == 0 {
			missing = append(missing, name)
		}
	}
	return missing
}

// Limit static recognition to plain projection columns and LIKE predicates on
// a single physical table. Aliases, functions and expressions belong to SQL Server.
func (s flatSelect) directColumns(aliases map[string]bool) []string {
	tokens := s.expressions
	project := tokens[:s.projectionEnd]
	if len(project) > 0 && (strings.EqualFold(project[0], "DISTINCT") || strings.EqualFold(project[0], "ALL")) {
		project = project[1:]
	}
	if len(project) > 1 && strings.EqualFold(project[0], "TOP") && project[1][0] >= '0' && project[1][0] <= '9' {
		project = project[2:]
	}
	var names []string
	seen := make(map[string]bool)
	add := func(token string) {
		name := identifier(token)
		if name == "" || strings.HasPrefix(name, "$") || seen[name] {
			return
		}
		switch strings.ToUpper(name) {
		case "USER", "NULL", "DEFAULT", "CURRENT_TIMESTAMP", "CURRENT_DATE", "CURRENT_TIME", "CURRENT_USER", "SESSION_USER", "SYSTEM_USER", "IDENTITYCOL", "ROWGUIDCOL":
			return // Built-in expressions and pseudo-columns are not catalog columns.
		}
		names = append(names, name)
		seen[name] = true
	}
	depth, start := 0, 0
	for i := 0; i <= len(project); i++ {
		if i < len(project) {
			switch project[i] {
			case "(":
				depth++
			case ")":
				depth--
			}
			if project[i] != "," || depth != 0 {
				continue
			}
		}
		part := project[start:i]
		if len(part) == 1 && len(s.tables) == 1 {
			add(part[0])
		}
		if len(part) == 3 && part[1] == "." {
			if aliases[identifier(part[0])] {
				add(part[2])
			}
		}
		start = i + 1
	}
	if len(s.tables) != 1 {
		return names
	}
	for i := s.projectionEnd + 2; i < len(tokens); i++ {
		if !strings.EqualFold(tokens[i], "LIKE") {
			continue
		}
		switch strings.ToUpper(tokens[i-2]) {
		case "WHERE", "AND", "OR", "(":
			add(tokens[i-1])
		}
	}
	return names
}

// Recognize only a plain integer column tested against a list of string
// literals. SQL Server would implicitly convert each value to the integer type.
func (s flatSelect) integerFilters(types map[string]string, aliases map[string]bool) []integerFilter {
	t := s.expressions
	var invalid []integerFilter
	for i := s.projectionEnd + 2; i+2 < len(t); i++ {
		if !strings.EqualFold(t[i], "IN") || t[i+1] != "(" {
			continue
		}
		name := identifier(t[i-1])
		kind := types[name]
		switch kind {
		case "int", "smallint", "bigint", "tinyint":
		default:
			continue
		}
		left := i - 2
		if t[left] == "." {
			if left < 2 || !aliases[identifier(t[left-1])] {
				continue
			}
			left -= 2
		}
		switch strings.ToUpper(t[left]) {
		case "WHERE", "AND", "OR", "(":
		default:
			continue
		}
		var bad []integerFilter
		for j := i + 2; j < len(t); {
			if strings.EqualFold(t[j], "N") {
				j++
			}
			if j >= len(t) || !strings.HasPrefix(t[j], "'") {
				break
			}
			literal := strings.ReplaceAll(t[j][1:len(t[j])-1], "''", "'")
			// Only ordinary text labels are diagnosed. Let the database handle
			// numeric strings, signs, whitespace, NULL and conversion edge cases.
			word := strings.TrimSpace(literal)
			if word != "" && strings.IndexFunc(word, func(r rune) bool { return !unicode.IsLetter(r) }) < 0 {
				bad = append(bad, integerFilter{name, kind, literal})
			}
			j++
			if j >= len(t) {
				break
			}
			if t[j] == ")" {
				invalid = append(invalid, bad...)
				i = j
				break
			}
			if t[j] != "," {
				break
			}
			j++
		}
	}
	return invalid
}
