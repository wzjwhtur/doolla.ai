package doolla

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"doolla.ai/sdk/internal/i18n"
)

// Only track receipts from this Chat invocation. History, catalog inspection and
// the assistant's description of a tool call cannot establish a new result.
type turnEvidence struct {
	scalar             scalarEvidence
	watchSQL, needRows bool
	metricContext      bool
	save               string
	inspected, retried bool
	receipt            string
	verbatim           string
	fileSaved          bool
	queries            []sqlReceipt
}

type sqlReceipt struct {
	callID, statement string
	failed, hasResult bool
	artifact          queryResultArtifact
}

type queryResultArtifact struct {
	id       string
	filename string
}

type queryResultsKey struct{}

func (e *turnEvidence) queryResults() []queryResultArtifact {
	var artifacts []queryResultArtifact
	for _, receipt := range e.queries {
		if receipt.artifact.id != "" {
			artifacts = append(artifacts, receipt.artifact)
		}
	}
	return artifacts
}

// Record what actually ran and only use result data a completion hook has
// allowed through. A hook cannot turn a failed SQL execution into evidence.
func (e *turnEvidence) recordSQL(call ToolCall, statement string, ran bool, rowCount int, returnedRows bool, file string, result *ToolResult) {
	r := sqlReceipt{callID: call.ID, statement: statement, failed: !ran || result.Error != ""}
	count, hasCount := result.Metadata["row_count"].(int)
	cols, _ := result.Metadata["columns"].([]string)
	rows, hasRows := result.Metadata["results"].([]map[string]any)
	r.hasResult = !r.failed && returnedRows && hasCount && rowCount == count && len(cols) != 0 && hasRows && len(rows) == count
	r.failed = r.failed || (returnedRows && !r.hasResult)
	if e.watchSQL {
		e.needRows = e.needRows || !commandWithoutRows(statement)
		if r.hasResult {
			e.scalar.record(*result)
		}
	}
	if output, _ := result.Metadata["output_file"].(string); r.hasResult && count > 0 && file != "" && output == file {
		id := fmt.Sprintf("result_%d", len(e.queryResults())+1)
		r.artifact = queryResultArtifact{id: id, filename: file}
		result.Content += "\nQuery result ID: " + id
	}
	if component := result.Component; r.hasResult && component != nil && component.Type == "dataframe" && component.Data != nil {
		published := *component
		published.Data = maps.Clone(component.Data)
		published.Data["tool_call_id"] = r.callID
		if r.artifact.id != "" {
			published.Data["result_id"] = r.artifact.id
		}
		result.Component = &published
	}
	e.queries = append(e.queries, r)
}

func (e turnEvidence) hasQueryResult() bool {
	return slices.ContainsFunc(e.queries, func(r sqlReceipt) bool { return r.hasResult })
}

func (e turnEvidence) queryAnswer(ctx context.Context) (string, bool) {
	answer := e.scalar.answer(ctx)
	pending, failures := make(map[string]int), 0
	for _, r := range e.queries {
		if r.failed {
			failures++
			if r.statement != "" {
				pending[r.statement]++
			}
		} else if r.hasResult {
			// A successful retry can close earlier failures for the same SQL.
			failures -= pending[r.statement]
			delete(pending, r.statement)
		}
	}
	if failures == 0 {
		return answer, false
	}
	if answer == "" {
		answer = i18n.T(ctx, "server.query.full_table")
	}
	return i18n.T(ctx, "server.query.partial", failures) + "\n\n" + answer, true
}

var dataRequest = regexp.MustCompile(`(?i)run_sql|\bselect\b.+\bfrom\b|查询|查一下|查出|统计|多少|总数|总量|总行数|有几|前[十百\d]+|排名|同比|环比|销量|产量|\b(?:how many|how much|count|sales|revenue|top [0-9]+|sum of|total orders|show (?:the )?(?:rows|data))\b`)
var catalogQuestion = regexp.MustCompile(`(?i)表结构|有哪些表|哪些字段|哪些列|外键|主键|表关系|\b(?:schema|foreign keys?|primary keys?|table structure)\b`)
var followupPeriod = regexp.MustCompile(`(?i)^(?:(?:那|那么)?(?:上|下|本|这|去|今|昨)(?:个)?(?:周|月|季度|年|天)(?:的)?(?:呢|怎么样)?[？?。！!]?|(?:what|how) about (?:last|this|next) (?:week|month|quarter|year)[?]?)$`)

var queryClaim = regexp.MustCompile(`(?i)(?:查询结果|查得|共计|数据库中共有|条记录|query returned|query result|rows? returned|there (?:are|were) [0-9]+ (?:orders|rows|records))`)
var writeStatement = regexp.MustCompile(`(?i)\b(?:insert\s+into|update\s+\S+\s+set|delete\s+from|create\s+table|drop\s+table)\b`)
var explanatoryRequest = regexp.MustCompile(`(?i)^(?:请)?(?:解释|编写|生成\s*SQL|如何使用|如何编写|怎么写|如何查询)|^(?:please )?(?:explain|write (?:a |the )?(?:sql|query)|how (?:do i|to) (?:write|use|query))\b`)

func (a *Agent) requiredEvidence(q string, history []Message) turnEvidence {
	var e turnEvidence
	if !a.config.VerifyToolAnswers {
		return e
	}
	q = strings.TrimSpace(q)
	e.save, e.verbatim = memoryWriteRequest(q)
	if e.save != "" {
		return e
	}
	if _, ok := a.registry.GetTool("run_sql"); ok && !memoryReadRequest(q) {
		e.inspectSQLRequest(q)
		if e.watchSQL && followupPeriod.MatchString(q) {
			// Earlier calls can identify a follow-up's intent, never its result.
			for i := len(history) - 1; i >= 0; i-- {
				if slices.ContainsFunc(history[i].ToolCalls, func(c ToolCall) bool { return c.Function.Name == "run_sql" }) {
					e.needRows = true
					break
				}
				if history[i].Role == "user" {
					break
				}
			}
		}
	}
	return e
}

func (e *turnEvidence) record(call ToolCall, result ToolResult, err error) {
	if e.watchSQL && call.Function.Name == "inspect_mssql_schema" {
		e.inspected = err == nil && result.Error == ""
		e.needRows = true
	}
	if err == nil && result.Error == "" {
		if isMemoryWriteTool(call.Function.Name) {
			e.receipt, _ = result.Metadata["memory_id"].(string)
		}
		if output, _ := result.Metadata["output_file"].(string); output != "" || call.Function.Name == "write_file" {
			e.fileSaved = true
		}
	}
}

// Finish through the same persistence and lifecycle hooks as a model reply.
// The text is also emitted when progress or tool details are hidden.
func (a *Agent) finishVerified(ctx context.Context, t Turn, c Conversation, messages []Message, emit func(Component) error, content string, outcome turnOutcome) error {
	recordTurnOutcome(ctx, outcome)
	level := "warning"
	if outcome.status == "" {
		level = "success"
	}
	return a.finishVerifiedText(ctx, t, c, messages, emit, content, status(level, content), true)
}

func (a *Agent) finishVerifiedText(ctx context.Context, t Turn, c Conversation, messages []Message, emit func(Component) error, content string, progress Component, markdown bool) error {
	reply := Message{Role: "assistant", Timestamp: time.Now().UTC(), Content: content}
	if err := a.finishConversation(ctx, t, c, append(messages, reply), &reply); err != nil {
		return err
	}
	if !a.config.HideProgress {
		if err := emit(progress); err != nil {
			return err
		}
	}
	return emit(Component{Type: "text", Data: map[string]any{"content": content, "markdown": markdown}})
}

func (e turnEvidence) pending() bool { return e.save != "" || (e.needRows && !e.hasQueryResult()) }

func (e turnEvidence) directSave() Message {
	args, _ := json.Marshal(map[string]string{"content": e.verbatim})
	return Message{ToolCalls: []ToolCall{{ID: newID(), Type: "function", Function: FunctionCall{Name: e.save, Arguments: string(args)}}}}
}

func visibleTool(specs []ToolSpec, name string) bool {
	return slices.ContainsFunc(specs, func(s ToolSpec) bool { return s.Name == name })
}
