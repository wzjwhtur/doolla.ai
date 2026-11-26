package doolla

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Only track receipts from this Chat invocation. History, catalog inspection and
// the assistant's description of a tool call cannot establish a new result.
type turnEvidence struct {
	scalar                           scalarEvidence
	watchSQL, needRows, attemptedSQL bool
	metricContext                    bool
	save                             string
	hasRows, inspected, retried      bool
	receipt                          string
	verbatim                         string
	fileSaved                        bool
}

const noQueryResult = "本轮未取得查询结果，无法确认数据。"
const noMemoryWrite = "本轮未确认记忆写入成功。"
const noMemoryAccess = "当前身份没有保存记忆的权限，本次未写入。"

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
	if e.watchSQL {
		switch call.Function.Name {
		case "inspect_mssql_schema":
			e.inspected = err == nil && result.Error == ""
			e.needRows = true
		case "run_sql":
			// Observe the actual executor result before a completion hook can
			// replace its error text or presentation component.
			e.attemptedSQL = true
			var args struct{ SQL string }
			if json.Unmarshal([]byte(call.Function.Arguments), &args) == nil && !commandWithoutRows(args.SQL) {
				e.needRows = true
			}
			_, rows := result.Metadata["row_count"]
			e.hasRows = err == nil && result.Error == "" && rows
			if e.hasRows {
				e.scalar.record(result)
			}
		}
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
func (a *Agent) finishVerified(ctx context.Context, t Turn, c Conversation, messages []Message, emit func(Component) error, content string, ok bool) error {
	level := "warning"
	if ok {
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

func (e turnEvidence) pending() bool { return e.save != "" || (e.needRows && !e.hasRows) }

func (e turnEvidence) directSave() Message {
	args, _ := json.Marshal(map[string]string{"content": e.verbatim})
	return Message{ToolCalls: []ToolCall{{ID: newID(), Type: "function", Function: FunctionCall{Name: e.save, Arguments: string(args)}}}}
}

func visibleTool(specs []ToolSpec, name string) bool {
	return slices.ContainsFunc(specs, func(s ToolSpec) bool { return s.Name == name })
}
