package doolla

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"doolla.ai/sdk/internal/i18n"
)

// Keep exact driver values. SQL decimals commonly arrive as strings, and
// converting them through float64 would lose scale or significant digits.
var sqlNumber = regexp.MustCompile(`^[+-]?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

type scalarLine struct {
	alias, value string
	empty        bool
}

type scalarEvidence struct {
	lines   []scalarLine
	tabular bool
}

// A final scalar remains authoritative after a preliminary sample query.
// Preserve consecutive scalar results in their execution order. A later
// detail table starts a new sequence and keeps the normal tabular summary.
func (e *scalarEvidence) record(result ToolResult) {
	cols, _ := result.Metadata["columns"].([]string)
	rows, ok := result.Metadata["results"].([]map[string]any)
	if !ok || len(cols) != 1 || len(rows) > 1 {
		e.tabular = true
		e.lines = nil
		return
	}
	label := scalarColumn(cols[0])
	if len(rows) == 0 {
		e.lines = append(e.lines, scalarLine{alias: label, empty: true})
		return
	}
	raw, present := rows[0][cols[0]]
	value := ""
	switch v := raw.(type) {
	case nil:
		value = "NULL"
	case json.Number:
		value = v.String()
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, string:
		value = fmt.Sprint(v)
	}
	if !present || (raw != nil && !sqlNumber.MatchString(value)) {
		e.tabular = true
		e.lines = nil
		return
	}
	e.lines = append(e.lines, scalarLine{alias: label, value: value})
}

func (e scalarEvidence) answer(ctx context.Context) string {
	lines := make([]string, 0, len(e.lines))
	for _, line := range e.lines {
		alias := line.alias
		if alias != "" {
			alias = i18n.T(ctx, "server.query.scalar_alias", line.alias)
		}
		if line.empty {
			lines = append(lines, i18n.T(ctx, "server.query.scalar_empty", alias))
		} else {
			lines = append(lines, i18n.T(ctx, "server.query.scalar_value", alias, line.value))
		}
	}
	summary := strings.Join(lines, "\n\n")
	if e.tabular && summary != "" {
		return summary + "\n\n" + i18n.T(ctx, "server.query.full_table")
	}
	return summary
}

// Render aliases as Markdown code so a database-supplied label stays literal,
// including SQL aliases that contain embedded backticks.
func scalarColumn(name string) string {
	if name == "" {
		return ""
	}
	longest, run := 0, 0
	for _, c := range name {
		if c == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	return fence + " " + name + " " + fence
}
