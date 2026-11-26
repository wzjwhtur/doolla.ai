package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	doolla "doolla.ai/sdk"
)

// MSSQLSchemaTool reads visible SQL Server catalog entries for named tables.
type MSSQLSchemaTool struct{ DB *sql.DB }

const schemaBatchSize = 64

func (MSSQLSchemaTool) Spec() doolla.ToolSpec {
	identifier := map[string]any{"type": "string", "minLength": 1, "maxLength": 128}
	table := map[string]any{
		"type": "object", "properties": map[string]any{"schema": identifier, "name": identifier},
		"required": []string{"schema", "name"}, "additionalProperties": false,
	}
	return doolla.ToolSpec{
		Name:        "inspect_mssql_schema",
		Description: "Inspect visible columns, ordered primary keys and incoming/outgoing declared foreign keys for a batch of schema-qualified SQL Server tables. Use this for table relationships and to resolve invalid column names.",
		Parameters: map[string]any{
			"type": "object", "properties": map[string]any{
				"tables": map[string]any{"type": "array", "items": table, "minItems": 1, "maxItems": schemaBatchSize},
			}, "required": []string{"tables"}, "additionalProperties": false,
		},
	}
}

type schemaTable struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

type tableDefinition struct {
	schemaTable
	Status      string            `json:"status"`
	Columns     []string          `json:"columns,omitempty"`
	ColumnTypes map[string]string `json:"column_types,omitempty"`
	PrimaryKey  []string          `json:"primary_key,omitempty"`
}

type columnPair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type declaredForeignKey struct {
	Name        string       `json:"name"`
	From        schemaTable  `json:"from"`
	To          schemaTable  `json:"to"`
	ColumnPairs []columnPair `json:"column_pairs"`
}

func (t MSSQLSchemaTool) Run(ctx context.Context, _ doolla.User, raw json.RawMessage) (doolla.ToolResult, error) {
	var input struct{ Tables []schemaTable }
	if err := json.Unmarshal(raw, &input); err != nil {
		return doolla.ToolResult{}, err
	}
	if len(input.Tables) == 0 || len(input.Tables) > schemaBatchSize {
		return doolla.ToolResult{}, fmt.Errorf("provide 1 to %d schema-qualified tables per call", schemaBatchSize)
	}
	report := struct {
		Scope                        string               `json:"scope"`
		OtherVisibleForeignKeysExist bool                 `json:"other_visible_foreign_keys_exist"`
		Tables                       []tableDefinition    `json:"tables"`
		ForeignKeys                  []declaredForeignKey `json:"declared_foreign_keys"`
	}{
		Scope:       "Listed foreign keys touch at least one inspected table. other_visible_foreign_keys_exist reports whether this account sees a foreign key outside this batch; all findings are limited to visible catalog entries.",
		ForeignKeys: []declaredForeignKey{},
	}
	var selects []string
	var args []any
	seen := make(map[schemaTable]bool)
	for _, table := range input.Tables {
		if strings.TrimSpace(table.Schema) == "" || strings.TrimSpace(table.Name) == "" {
			return doolla.ToolResult{}, errors.New("both schema and table name are required")
		}
		if utf8.RuneCountInString(table.Schema) > 128 || utf8.RuneCountInString(table.Name) > 128 {
			return doolla.ToolResult{}, errors.New("schema and table names must be at most 128 characters each")
		}
		if seen[table] {
			continue
		}
		seen[table] = true
		// 标识符作为值绑定，SQL 仅拼接本地生成的表序号和参数占位符。
		selects = append(selects, fmt.Sprintf("SELECT %d, @p%d, @p%d", len(report.Tables), len(args)+1, len(args)+2))
		args = append(args, table.Schema, table.Name)
		report.Tables = append(report.Tables, tableDefinition{schemaTable: table, Status: "missing_or_not_visible"})
	}
	if t.DB == nil {
		return doolla.ToolResult{}, errors.New("SQL Server connection is required")
	}
	query := "WITH requested (table_no, schema_name, table_name) AS (" + strings.Join(selects, " UNION ALL ") + "),\n" + mssqlCatalogSelect
	rows, err := t.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return doolla.ToolResult{}, err
	}
	defer rows.Close()
	lastFK := -1
	for rows.Next() {
		var kind, id int
		var ordinal, primaryOrdinal sql.NullInt64
		var schema, table, column, toSchema, toTable, toColumn, fk, baseType sql.NullString
		if err := rows.Scan(&kind, &id, &ordinal, &primaryOrdinal, &schema, &table, &column, &toSchema, &toTable, &toColumn, &fk, &baseType); err != nil {
			return doolla.ToolResult{}, err
		}
		if kind == 2 {
			report.OtherVisibleForeignKeysExist = true
			continue
		}
		if kind == 1 {
			if len(report.ForeignKeys) == 0 || id != lastFK {
				report.ForeignKeys = append(report.ForeignKeys, declaredForeignKey{
					Name: fk.String, From: schemaTable{schema.String, table.String}, To: schemaTable{toSchema.String, toTable.String},
				})
				lastFK = id
			}
			key := &report.ForeignKeys[len(report.ForeignKeys)-1]
			key.ColumnPairs = append(key.ColumnPairs, columnPair{column.String, toColumn.String})
			continue
		}
		if !table.Valid {
			continue
		}
		entry := &report.Tables[id]
		entry.Status = "inspected"
		entry.schemaTable = schemaTable{schema.String, table.String}
		if column.Valid {
			entry.Columns = append(entry.Columns, column.String)
			if baseType.Valid {
				if entry.ColumnTypes == nil {
					entry.ColumnTypes = make(map[string]string)
				}
				entry.ColumnTypes[column.String] = baseType.String
			}
		}
		if primaryOrdinal.Valid {
			// 主键列顺序独立于建表时的列顺序。
			for len(entry.PrimaryKey) < int(primaryOrdinal.Int64) {
				entry.PrimaryKey = append(entry.PrimaryKey, "")
			}
			entry.PrimaryKey[primaryOrdinal.Int64-1] = column.String
		}
	}
	if err := rows.Err(); err != nil {
		return doolla.ToolResult{}, err
	}
	body, err := json.Marshal(report)
	return doolla.ToolResult{Content: string(body)}, err
}

// 一次往返读取目标表的列、主键及双向外键。按约束对象及键内序号排序，
// 同一复合外键只输出一个约束，即便它的两端都在请求范围内。
const mssqlCatalogSelect = `inspected AS (
	SELECT r.table_no, s.name AS schema_name, t.name AS table_name, t.object_id
	FROM requested r
	LEFT JOIN sys.schemas s ON s.name = r.schema_name
	LEFT JOIN sys.tables t ON t.schema_id = s.schema_id AND t.name = r.table_name
), pk_columns AS (
	SELECT ic.object_id, ic.column_id, ic.key_ordinal
	FROM sys.indexes i
	JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
	WHERE i.is_primary_key = 1 AND ic.key_ordinal > 0
)
SELECT 0 AS kind, i.table_no AS item, c.column_id AS seq, pk.key_ordinal,
	i.schema_name, i.table_name, c.name AS column_name,
	NULL AS to_schema, NULL AS to_table, NULL AS to_column, NULL AS fk_name, bt.name AS base_type
FROM inspected i
LEFT JOIN sys.columns c ON c.object_id = i.object_id
LEFT JOIN sys.types bt ON bt.user_type_id = c.system_type_id
LEFT JOIN pk_columns pk ON pk.object_id = c.object_id AND pk.column_id = c.column_id
UNION ALL
SELECT 1, f.object_id, fc.constraint_column_id, NULL, ps.name, p.name, pc.name, rs.name, r.name, rc.name, f.name, NULL
FROM sys.foreign_keys f
JOIN sys.foreign_key_columns fc ON fc.constraint_object_id = f.object_id
JOIN sys.tables p ON p.object_id = fc.parent_object_id
JOIN sys.schemas ps ON ps.schema_id = p.schema_id
JOIN sys.columns pc ON pc.object_id = p.object_id AND pc.column_id = fc.parent_column_id
JOIN sys.tables r ON r.object_id = fc.referenced_object_id
JOIN sys.schemas rs ON rs.schema_id = r.schema_id
JOIN sys.columns rc ON rc.object_id = r.object_id AND rc.column_id = fc.referenced_column_id
WHERE EXISTS (SELECT 1 FROM inspected i WHERE i.object_id IN (p.object_id, r.object_id))
UNION ALL
SELECT 2, 0, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL
WHERE EXISTS (
	SELECT 1 FROM sys.foreign_keys f
	WHERE NOT EXISTS (SELECT 1 FROM inspected i WHERE i.object_id IN (f.parent_object_id, f.referenced_object_id))
)
ORDER BY kind, item, seq`
