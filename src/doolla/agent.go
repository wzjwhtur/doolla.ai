// Package doolla runs conversational data agents with model-selected tools.
package doolla

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"doolla.ai/sdk/internal/i18n"
)

type User struct {
	ID       string         `json:"id"`
	Groups   []string       `json:"group_memberships,omitempty"`
	Email    string         `json:"email,omitempty"`
	Username string         `json:"username,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type TokenUsage struct {
	Input  int `json:"input_tokens"`
	Output int `json:"output_tokens"`
}

type Message struct {
	Timestamp time.Time       `json:"timestamp,omitzero"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	Usage     *TokenUsage     `json:"usage,omitempty"`
	Role      string          `json:"role"`
	Content   string          `json:"content,omitempty"`
	// FinishReason records the provider's completion reason. It is empty when
	// the provider did not supply one or the message did not come from a model.
	FinishReason string          `json:"finish_reason,omitempty"`
	ToolCalls    []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID   string          `json:"tool_call_id,omitempty"`
	ModelState   json.RawMessage `json:"model_state,omitempty"`
}

type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type Model interface {
	Reply(context.Context, []Message, []ToolSpec) (Message, error)
}

type Component struct {
	Type          string
	Data          map[string]any
	ID, Lifecycle string
	Children      []string
	Visible       *bool
	Interactive   bool
	Simple        map[string]any
}

func textComponent(text string) Component {
	return Component{Type: "text", Data: map[string]any{"content": text, "markdown": true}}
}

type ToolResult struct {
	// Error is the failure detail exposed to completion hooks. An empty string
	// indicates success. Content is the text that will be sent to the model.
	Error     string
	Metadata  map[string]any
	Content   string
	Component *Component
}

type Tool interface {
	Spec() ToolSpec
	Run(context.Context, User, json.RawMessage) (ToolResult, error)
}

type Registration struct {
	Tool   Tool
	Groups []string
}

func (r Registration) allows(user User) bool {
	if len(r.Groups) == 0 {
		return true
	}
	for _, group := range user.Groups {
		if slices.Contains(r.Groups, group) {
			return true
		}
	}
	return false
}

type conversationKey struct{ user, id string }
type turnLock struct {
	slot  chan struct{}
	users int
}

type Agent struct {
	confirmations *sqlConfirmations
	registry      *ToolRegistry
	model         Model
	config        AgentConfig
	mu            sync.Mutex
	turns         map[conversationKey]*turnLock
}

func NewAgent(model Model, system string, tools ...Registration) (*Agent, error) {
	return NewConfiguredAgent(model, AgentConfig{SystemPrompt: system}, tools...)
}

func NewConfiguredAgent(model Model, config AgentConfig, tools ...Registration) (*Agent, error) {
	if model == nil {
		return nil, errors.New("model is required")
	}
	if config.TextMemoryRecall == (MemoryRecall{}) {
		config.TextMemoryRecall = DefaultTextRecall()
	}
	if err := config.TextMemoryRecall.Validate(); err != nil {
		return nil, fmt.Errorf("text memory: %w", err)
	}
	if config.MaxToolIterations == 0 {
		config.MaxToolIterations = 10
	}
	if config.MaxToolIterations < 1 {
		return nil, errors.New("max tool iterations must be positive")
	}
	if config.ConversationWorkflow != nil && config.Workflow != nil {
		return nil, errors.New("configure one workflow handler")
	}
	config.UIFeatures = uiFeatureGroups(config.UIFeatures, config.ReplaceDefaultUIFeatures)
	if config.Conversations == nil {
		config.Conversations = NewMemoryConversations()
	}
	registry := config.Registry
	if registry == nil {
		registry = &ToolRegistry{}
	}
	if config.Audit == nil {
		config.Audit = registry.Audit
	}
	if err := registry.Register(tools...); err != nil {
		return nil, err
	}
	a := &Agent{registry: registry, model: model, config: config, turns: make(map[conversationKey]*turnLock)}
	if config.ConfirmSQLMemory {
		if config.Memory == nil {
			return nil, errors.New("SQL memory confirmation requires a memory store")
		}
		a.confirmations = new(sqlConfirmations)
	}
	return a, nil
}

func newID() string { return rand.Text() }

func (a *Agent) enter(ctx context.Context, key conversationKey) (func(), error) {
	a.mu.Lock()
	lock := a.turns[key]
	if lock == nil {
		lock = &turnLock{slot: make(chan struct{}, 1)}
		a.turns[key] = lock
	}
	lock.users++
	a.mu.Unlock()
	leave := func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		lock.users--
		if lock.users == 0 {
			delete(a.turns, key)
		}
	}
	select {
	case lock.slot <- struct{}{}:
		return func() { <-lock.slot; leave() }, nil
	case <-ctx.Done():
		leave()
		return nil, ctx.Err()
	}
}

// Chat publishes components as tools finish. Turns in one conversation are
// serialized, while separate users and conversations can proceed concurrently.
func (a *Agent) Chat(ctx context.Context, user User, id, question string, emit func(Component) error) error {
	return a.chat(ctx, user, id, question, emit, nil)
}

func (a *Agent) chat(ctx context.Context, user User, id, question string, emit func(Component) error, outcome *turnOutcome) (err error) {
	if user.ID == "" || id == "" || emit == nil {
		return errors.New("user, conversation ID and component sink are required")
	}
	if stores, ok := a.config.Memory.(*TenantMemoryStore); ok {
		if _, err := stores.ForUser(user); err != nil {
			return err
		}
	}
	ctx = i18n.WithLocale(ctx, i18n.Resolve(i18n.Requested(ctx), "", a.config.DefaultLocale))
	answerCtx := i18n.WithLocale(ctx, requestedAnswerLocale(question))
	turn := Turn{Memory: a.config.Memory, Observability: a.config.Observability, Metadata: RequestMetadata(ctx), User: user, ConversationID: id, RequestID: newID()}
	if requestID, _ := ctx.Value(requestIDKey{}).(string); requestID != "" {
		turn.RequestID = requestID
	}
	for _, feature := range slices.Sorted(maps.Keys(a.config.UIFeatures)) {
		groups := a.config.UIFeatures[feature]
		allowed := (Registration{Groups: groups}).allows(user)
		if allowed {
			turn.UIFeatures = append(turn.UIFeatures, feature)
		}
		if a.config.Audit != nil && a.config.Audit.LogUIFeatureChecks {
			a.config.Audit.feature(ctx, turn, feature, groups, allowed)
		}
	}
	ctx = context.WithValue(ctx, turnKey{}, turn)
	ctx, messageTrace := a.startSpan(ctx, "agent.send_message", "agent.message.duration", map[string]any{"conversation_id": id}, map[string]string{"user_id": user.ID})
	messageTrace.outcome("hit_tool_limit", false)
	defer func() {
		var limit *ToolIterationLimitError
		hitLimit := errors.As(err, &limit)
		if err != nil && !hitLimit && a.config.Observability != nil {
			errorCtx, failed := a.startSpan(ctx, "agent.send_message.error", "", map[string]any{"conversation_id": id, "user_id": user.ID}, nil)
			failed.end(err)
			a.config.Observability.RecordMetric(errorCtx, "agent.error.count", 1, "count", nil)
		}
		if hitLimit {
			messageTrace.end(nil)
		} else {
			messageTrace.end(err)
		}
	}()
	enriched, err := a.enrich(ctx, turn)
	if err != nil {
		return err
	}
	ctx = enriched
	if outcome != nil {
		ctx = context.WithValue(ctx, turnOutcomeKey{}, outcome)
	}
	start := time.Now()
	defer func() { a.observe(ctx, Event{Operation: "message", Turn: turn, Duration: time.Since(start), Err: err}) }()
	_, waitTrace := a.startSpan(ctx, "agent.conversation.wait", "agent.conversation.wait.duration", nil, nil)
	leave, err := a.enter(ctx, conversationKey{user.ID, id})
	waitTrace.end(err)
	if err != nil {
		return err
	}
	defer leave()
	originalQuestion := question
	starterRequest := strings.TrimSpace(question) == "" || turn.Metadata["starter_ui_request"] == true
	if starterRequest {
		question = ""
	} else {
		question, err = a.beforeMessage(ctx, turn, question)
		if err != nil {
			return err
		}
	}
	if a.confirmations != nil && question == originalQuestion && strings.HasPrefix(question, "/confirm-sql ") {
		return a.confirmSQL(ctx, turn, strings.TrimSpace(strings.TrimPrefix(question, "/confirm-sql ")), emit)
	}
	_, schemaTrace := a.startSpan(ctx, "agent.tool_schemas.fetch", "agent.tool_schemas.duration", map[string]any{"user_id": user.ID}, nil)
	specs := a.registry.Schemas(user)
	schemaTrace.set("schema_count", len(specs))
	schemaTrace.end(nil)
	if a.config.ConversationWorkflow == nil {
		child, trace := a.workflowSpan(ctx, turn, question)
		handled, err := a.workflow(child, turn, question, specs, emit)
		trace.set("handled", handled)
		trace.end(err)
		if handled || starterRequest || err != nil {
			return err
		}
	}
	contextTask := newID()
	if !a.config.HideProgress {
		if err := emit(startTask(contextTask, i18n.T(ctx, "server.task.context"), i18n.T(ctx, "server.task.context_detail"))); err != nil {
			return err
		}
	}
	conversation, err := a.loadConversation(ctx, turn)
	history := conversation.Messages
	if err != nil {
		if !a.config.HideProgress {
			err = errors.Join(err, emit(endTask(contextTask, false)))
		}
		return err
	}
	if workflow := a.config.ConversationWorkflow; workflow != nil {
		child, trace := a.workflowSpan(ctx, turn, question)
		decision, err := workflow(child, turn, cloneMessages(history), question, emit)
		trace.set("handled", decision.Handled)
		trace.end(err)
		if err == nil && decision.Handled && decision.Replacement != nil && !a.config.DisableAutoSave {
			err = a.saveMessages(ctx, turn, decision.Replacement)
		}
		if decision.Handled || starterRequest || err != nil {
			if !a.config.HideProgress {
				err = errors.Join(err, emit(endTask(contextTask, err == nil)))
			}
			return err
		}
	}
	messages := append(slices.Clone(history), Message{Role: "user", Content: question, Timestamp: time.Now().UTC()})
	if query := a.directMemoryQuery(question, specs); query != "" {
		if !a.config.HideProgress {
			if err = emit(endTask(contextTask, true)); err != nil {
				return err
			}
		}
		return a.answerFromMemory(ctx, turn, conversation, messages, emit, query)
	}
	promptCtx, promptTrace := a.startSpan(ctx, "agent.system_prompt.build", "agent.system_prompt.duration", map[string]any{"tool_count": len(specs)}, nil)
	system, err := a.systemPrompt(promptCtx, turn, question, specs)
	promptTrace.set("system_prompt_length", len([]rune(system)))
	promptTrace.end(err)
	if !a.config.HideProgress {
		err = errors.Join(err, emit(endTask(contextTask, err == nil)))
	}
	if err != nil {
		return err
	}
	if !a.config.HideProgress {
		if err := emit(status("working", i18n.T(ctx, "server.status.thinking"))); err != nil {
			return err
		}
	}
	evidence := a.requiredEvidence(question, history)
	var explicitSQL string
	if a.config.DirectSQLResult && evidence.needRows {
		explicitSQL = explicitSQLStatement(question)
	}
	if evidence.save != "" && !visibleTool(specs, evidence.save) {
		return a.finishVerified(ctx, turn, conversation, messages, emit, i18n.T(answerCtx, "server.memory.no_access"), failedOutcome("memory_access_denied"))
	}
	memoryRead := a.config.VerifyToolAnswers && memoryReadRequest(question)
	if memoryRead {
		// A recall request may use retrieval tools, but cannot mutate shared memory.
		specs = slices.DeleteFunc(slices.Clone(specs), func(s ToolSpec) bool { return isMemoryWriteTool(s.Name) })
	}
	// Preview fragments are negotiated separately from the authoritative text
	// event. Never publish a draft that evidence checks or middleware must review.
	var onText func(string) error
	if turn.Metadata["answer_deltas"] == true && a.canPreviewAnswer() {
		onText = func(fragment string) error {
			return emit(Component{Type: "answer_delta", Data: map[string]any{"text": fragment}})
		}
	}
	for round := 0; round < a.config.MaxToolIterations; round++ {
		messageTrace.set("tool_iterations", round)
		input := append([]Message{{Role: "system", Content: system}}, messages...)
		var reply Message
		focus := evidence.save
		if evidence.needRows && !evidence.hasQueryResult() && (evidence.inspected || evidence.retried) {
			focus = "run_sql"
			input = append(input, Message{Role: "user", Content: "The original question still needs live data. Call run_sql with the read query now, using the inspected columns. Do not answer from the catalog."})
		}
		if evidence.verbatim != "" {
			reply = evidence.directSave()
		} else {
			reply, err = a.reply(ctx, turn, input, specs, focus, onText)
		}
		if err != nil {
			return err
		}
		reply.Role = "assistant"
		reply.Timestamp = time.Now().UTC()
		truncated := reply.FinishReason == "length" || reply.FinishReason == "max_tokens" || reply.FinishReason == "incomplete"
		if truncated {
			// A truncated call can still contain syntactically valid but incomplete arguments.
			reply.ToolCalls = nil
			reply.ModelState = nil
		}
		a.config.Audit.response(ctx, turn, reply, a.model)
		clarification := evidence.observeSQLAnswer(reply.Content)
		if memoryRead && slices.ContainsFunc(reply.ToolCalls, func(c ToolCall) bool { return isMemoryWriteTool(c.Function.Name) }) {
			return a.finishVerified(ctx, turn, conversation, messages, emit, i18n.T(answerCtx, "server.memory.incomplete"), failedOutcome("memory_recall_incomplete"))
		}
		if evidence.save != "" && len(reply.ToolCalls) > 0 && (len(reply.ToolCalls) != 1 || reply.ToolCalls[0].Function.Name != evidence.save) {
			return a.finishVerified(ctx, turn, conversation, messages, emit, i18n.T(answerCtx, "server.memory.no_write"), failedOutcome("memory_write_unconfirmed"))
		}
		if len(reply.ToolCalls) == 0 && evidence.pending() && !clarification {
			if evidence.save != "" {
				return a.finishVerified(ctx, turn, conversation, messages, emit, i18n.T(answerCtx, "server.memory.no_write"), failedOutcome("memory_write_unconfirmed"))
			}
			if !evidence.retried && len(evidence.queries) == 0 && focus == "" && round+1 < a.config.MaxToolIterations && visibleTool(specs, "run_sql") {
				evidence.retried = true
				continue
			}
			return a.finishVerified(ctx, turn, conversation, messages, emit, i18n.T(answerCtx, "server.query.no_result"), failedOutcome("query_result_unavailable"))
		}
		if len(reply.ToolCalls) == 0 && evidence.needRows && evidence.hasQueryResult() && !clarification {
			if answer, partial := evidence.queryAnswer(answerCtx); answer != "" {
				var outcome turnOutcome
				if partial {
					outcome = partialOutcome("query_partial")
				}
				return a.finishVerified(ctx, turn, conversation, messages, emit, answer, outcome)
			}
		}
		unconfirmedSave := a.config.VerifyToolAnswers && evidence.unconfirmedMemoryClaim(question, reply.Content)
		if len(reply.ToolCalls) == 0 && unconfirmedSave {
			return a.finishVerified(ctx, turn, conversation, messages, emit, i18n.T(answerCtx, "server.memory.no_write"), failedOutcome("memory_write_unconfirmed"))
		}
		// Tool-call preambles reach both the UI and persisted history. Only
		// publish SQL numbers and memory confirmations from completed tools.
		if len(reply.ToolCalls) > 0 && (evidence.needRows || evidence.pending() || unconfirmedSave) {
			reply.Content = ""
		}
		messages = append(messages, reply)
		if len(reply.ToolCalls) == 0 {
			if strings.TrimSpace(reply.Content) == "" && !truncated {
				return errors.New("model returned an empty reply")
			}
			if err := a.finishConversation(ctx, turn, conversation, messages, &reply); err != nil {
				return err
			}
			if truncated {
				outcome := failedOutcome("model_response_incomplete")
				if reply.Content != "" || evidence.hasQueryResult() {
					outcome = partialOutcome("model_response_incomplete")
				}
				recordTurnOutcome(ctx, outcome)
				notice := i18n.T(ctx, "server.status.model_limit_detail")
				title := i18n.T(ctx, "server.status.model_limit")
				if reply.FinishReason == "incomplete" {
					title = i18n.T(ctx, "server.status.model_incomplete")
					notice = i18n.T(ctx, "server.status.model_incomplete_detail")
				}
				if !a.config.HideProgress {
					if err := emit(status("warning", title)); err != nil {
						return err
					}
				}
				if reply.Content != "" {
					if err := emit(textComponent(reply.Content)); err != nil {
						return err
					}
				}
				return emit(Component{Type: "notification", Data: map[string]any{"level": "warning", "message": notice, "finish_reason": reply.FinishReason}, Simple: map[string]any{"type": "text", "text": notice}})
			}
			if !a.config.HideProgress {
				if err := emit(status("success", i18n.T(ctx, "server.status.ready"))); err != nil {
					return err
				}
			}
			return emit(textComponent(reply.Content))
		}
		if reply.Content != "" {
			c := status("working", reply.Content)
			if slices.Contains(turn.UIFeatures, "tool_invocation_message_in_chat") {
				c = textComponent(reply.Content)
				c.Data["chat_section"] = "activity"
			}
			if err := emit(c); err != nil {
				return err
			}
		}
		start := len(messages)
		ids := make(map[string]bool, len(reply.ToolCalls))
		for _, call := range reply.ToolCalls {
			if call.ID == "" || ids[call.ID] {
				return errors.New("model returned a missing or duplicate tool call ID")
			}
			ids[call.ID] = true
			// 同一轮的每个工具调用都要有配对结果。先占位，再按调用 ID 的顺序回填；
			// 即使客户端在批次中途断开，下次加载的历史也符合模型协议。
			messages = append(messages, Message{Role: "tool", ToolCallID: call.ID, Content: "Tool was not executed"})
		}
		for i, call := range reply.ToolCalls {
			if err := ctx.Err(); err != nil {
				return err
			}
			result, completedTask, err := a.executeCall(ctx, turn, call, emit, &evidence)
			if err != nil {
				return err
			}
			messages[start+i].Content = result.Content
			messages[start+i].Timestamp = time.Now().UTC()
			if !a.config.DisableAutoSave {
				// 数据库可能已经提交。客户端此时断开，仍给存储最多五秒写入已执行结果，
				// 让下轮对话知道哪些工具已完成。
				saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				err := a.saveMessages(saveCtx, turn, messages)
				stop()
				if err != nil {
					return err
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if completedTask != nil {
				if err := emit(*completedTask); err != nil {
					return err
				}
			}
			if result.Component != nil {
				if err := emit(*result.Component); err != nil {
					return err
				}
			}
			if a.confirmations != nil && call.Function.Name == "run_sql" && a.canConfirmSQL(user) && len(evidence.queries) > 0 {
				r := evidence.queries[len(evidence.queries)-1]
				if r.callID == call.ID && r.hasResult && r.statement != "" {
					if err := emit(a.confirmations.offer(ctx, turn, originalQuestion, r)); err != nil {
						return err
					}
				}
			}
		}
		// An exact, single-statement request already has its final answer. The
		// receipt uses the effective tool arguments after registry policy and hooks.
		if explicitSQL != "" && len(reply.ToolCalls) == 1 && len(evidence.queries) == 1 &&
			evidence.queries[0].hasResult && trimSQLTerminator(evidence.queries[0].statement) == explicitSQL {
			if answer := evidence.scalar.answer(answerCtx); answer != "" {
				return a.finishVerified(ctx, turn, conversation, messages, emit, answer, turnOutcome{})
			}
		}
		if evidence.save != "" {
			if evidence.receipt != "" {
				return a.finishVerified(ctx, turn, conversation, messages, emit, i18n.T(answerCtx, "server.memory.saved"), turnOutcome{})
			}
			return a.finishVerified(ctx, turn, conversation, messages, emit, i18n.T(answerCtx, "server.memory.no_write"), failedOutcome("memory_write_unconfirmed"))
		}
	}
	if evidence.needRows && !evidence.hasQueryResult() {
		return a.finishVerified(ctx, turn, conversation, messages, emit, i18n.T(answerCtx, "server.query.no_result"), failedOutcome("query_result_unavailable"))
	}
	messageTrace.outcome("hit_tool_limit", true)
	messageTrace.set("tool_iterations", a.config.MaxToolIterations)
	messageTrace.set("incomplete_response", true)
	if evidence.hasQueryResult() {
		recordTurnOutcome(ctx, partialOutcome("tool_iteration_limit"))
	}
	if err := a.finishConversation(ctx, turn, conversation, messages, nil); err != nil {
		return err
	}
	return &ToolIterationLimitError{Limit: a.config.MaxToolIterations}
}

type ToolIterationLimitError struct{ Limit int }

func (e *ToolIterationLimitError) Error() string {
	return fmt.Sprintf("model exceeded %d tool iterations", e.Limit)
}

func (a *Agent) executeCall(ctx context.Context, turn Turn, call ToolCall, emit func(Component) error, evidence *turnEvidence) (ToolResult, *Component, error) {
	user := turn.User
	tool, allowed := a.registry.lookup(user, call.Function.Name)
	a.config.Audit.access(ctx, turn, call, tool.Groups, allowed)
	if !allowed {
		const reason = "Tool unavailable"
		_, trace := a.toolSpan(ctx, call)
		trace.outcome("success", false)
		trace.end(errors.New(reason))
		result, err := a.afterToolResult(ctx, turn, call, tool.Tool, ToolResult{Content: reason, Error: reason}, false)
		if call.Function.Name == "run_sql" {
			evidence.recordSQL(call, "", false, 0, false, "", &result)
		}
		return result, nil, err
	}
	var taskID string
	if !a.config.HideProgress && slices.Contains(turn.UIFeatures, "tool_names") {
		taskID = newID()
		if err := emit(startTask(taskID, i18n.T(ctx, "server.task.execute", call.Function.Name), "")); err != nil {
			return ToolResult{}, nil, err
		}
		if err := emit(status("working", i18n.T(ctx, "server.task.executing", call.Function.Name))); err != nil {
			return ToolResult{}, nil, err
		}
	}
	var isSQL bool
	switch tool.Tool.(type) {
	case SQLTool, *SQLTool:
		isSQL = true
	}
	showSQL := isSQL && !a.config.HideProgress && slices.Contains(turn.UIFeatures, "sql_query")
	if !showSQL && !a.config.HideProgress && slices.Contains(turn.UIFeatures, "tool_arguments") {
		var args map[string]any
		if json.Unmarshal([]byte(call.Function.Arguments), &args) == nil {
			if err := emit(Component{Type: "status_card", Data: map[string]any{
				"title": i18n.T(ctx, "server.task.executing", call.Function.Name), "status": "running", "metadata": args,
				"chat_section": "activity",
			}}); err != nil {
				return ToolResult{}, nil, err
			}
		}
	}
	var result ToolResult
	args, err := a.prepareTool(ctx, turn, tool, call)
	var statement string
	if err == nil && a.confirmations != nil && call.Function.Name == "save_question_tool_args" && sqlMemoryArgs(args) && ctx.Value(sqlApprovalKey{}) == nil {
		err = errors.New(i18n.T(ctx, "server.memory.confirm_required"))
	}
	if err == nil {
		if isSQL || call.Function.Name == "run_sql" {
			statement, _ = sqlStatement(args)
		}
		if showSQL {
			if statement != "" {
				// Publish the statement passed to the SQL tool after argument transforms and hooks.
				if err := emit(Component{Type: "sql_statement", Data: map[string]any{
					"title": i18n.T(ctx, "server.query.generated_sql"), "content": statement, "code_language": "sql", "chat_section": "activity", "tool_call_id": call.ID,
				}}); err != nil {
					return ToolResult{}, nil, err
				}
			}
		}
		runCtx := ctx
		if call.Function.Name == "visualize_data" {
			runCtx = context.WithValue(ctx, queryResultsKey{}, evidence.queryResults())
		}
		result, err = a.runTool(runCtx, turn, tool.Tool, call, args)
	}
	evidence.record(call, result, err)
	ran := err == nil && result.Error == ""
	rowCount, returnedRows := result.Metadata["row_count"].(int)
	var queryFile string
	if isSQL && ran {
		queryFile, _ = result.Metadata["output_file"].(string)
	}
	if err != nil {
		result = failedToolResult(err)
	}
	result, err = a.afterToolResult(ctx, turn, call, tool.Tool, result, err == nil)
	if err != nil {
		return ToolResult{}, nil, err
	}
	if isSQL || call.Function.Name == "run_sql" {
		evidence.recordSQL(call, statement, ran, rowCount, returnedRows, queryFile, &result)
	}
	// 工具失败后模型可以继续重试。即使用户无权查看错误详情，聊天区也应保留本次调用的失败状态。
	if result.Error != "" && result.Component == nil && slices.Contains(turn.UIFeatures, "tool_names") {
		message := i18n.T(ctx, "server.tool.failed", call.Function.Name)
		result.Component = &Component{Type: "notification",
			Data:   map[string]any{"level": "error", "message": message},
			Simple: map[string]any{"type": "text", "text": message},
		}
	}
	if taskID != "" {
		completion := endTask(taskID, result.Error == "")
		return result, &completion, nil
	}
	return result, nil, nil
}

func memoryID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
