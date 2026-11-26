package doolla

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Distinguish requests to recall a fact from commands that store it. In particular,
// “记住的质检代码是什么” describes a previously remembered fact.
var recallRequest = regexp.MustCompile(`(?i)^(?:(?:请问|请告诉我|你|还|你还)*(?:记住|记下|保存|存储)(?:过的|了的|的).*(?:什么|哪|多少|吗|[?？])|(?:你还|还|你)?记得.*(?:什么|哪|多少|吗|[?？])|(?:do you (?:still )?remember|what (?:did (?:i|you)|do you|was the|is the).*(?:saved?|stored?|remember(?:ed)?|memory)|recall the|remind me (?:of|what))\b)`)

func memoryReadRequest(q string) bool {
	return recallRequest.MatchString(strings.TrimSpace(q))
}

var memoryCommand = regexp.MustCompile(`(?i)^(?:(?:请(?:帮我)?|帮我)?(?:记住|记下|保存(?:这段|这条|以下)?记忆)|(?:请(?:帮我)?|帮我)?(?:(?:把|将).+)?(?:存进|存入|存到|写入|保存至|保存到|保存进)(?:我的|你的)?(?:长期)?记忆|(?:(?:could|can|would) you\s+)?(?:please\s+)?(?:remember\b|save (?:this|the following) (?:fact|memory|tool invocation)\b|save\b.*\b(?:to|in) (?:long.term )?memory\b|(?:use|call) (?:the )?save_(?:text_memory|question_tool_args)\b))`)

// A verbatim payload is an argument to the normal permission-checked tool, even
// when it contains another command, a colon or a question mark.
func memoryWriteRequest(question string) (tool, verbatim string) {
	if memoryReadRequest(question) || !memoryCommand.MatchString(question) {
		return "", ""
	}
	command := question
	if i := strings.IndexAny(command, "：:"); i >= 0 {
		command = question[:i]
		_, width := utf8.DecodeRuneInString(question[i:])
		verbatim = strings.TrimSpace(question[i+width:])
	}
	command = strings.ToLower(strings.TrimSpace(command))
	if strings.Contains(command, "save_question_tool_args") || strings.Contains(command, "tool invocation") {
		return "save_question_tool_args", ""
	}
	switch command {
	case "记住", "请记住", "记下", "请记下", "remember", "please remember":
		return "save_text_memory", verbatim
	}
	if strings.HasSuffix(command, "verbatim") {
		return "save_text_memory", verbatim
	}
	return "save_text_memory", ""
}

func isMemoryWriteTool(name string) bool {
	return name == "save_text_memory" || name == "save_question_tool_args"
}

// Check the proposed answer independently of the intent recognizer. A model's
// own confirmation must never trigger an implicit/retried write.
var memoryConfirmation = regexp.MustCompile(`(?i)(?:已(?:经)?(?:成功)?(?:记住|记下)|(?:已(?:经)?|成功)(?:把|将)?[^。！？\n]{0,60}(?:存入|存进|存到|写入|保存到|保存至|保存进)(?:了)?(?:我的|你的)?(?:长期)?记忆|(?:长期)?记忆(?:已(?:经)?|成功)(?:成功)?(?:写入|保存)|(?:^|[.!?]\s*|\bI(?:'ve| have)?\s+)(?:successfully\s+)?(?:saved|stored|added)\b[^.!?\n]{0,90}\b(?:to|in) (?:my |your |long.term )*memory\b|\bI(?:'ve| have) (?:successfully )?memorized\b)`)
var bareConfirmation = regexp.MustCompile(`(?i)^(?:(?:好的|好了|ok(?:ay)?|done)[，,。.!\s]*)?(?:已(?:经)?(?:成功)?保存(?:了)?|保存成功|saved(?: successfully)?|successfully saved)[。.!！\s]*$`)
var fileContext = regexp.MustCompile(`(?i)文件|附件|报表|导出|下载|\b(?:csv|xlsx?|pdf|files?|artifact|export|download)\b`)

func (e turnEvidence) unconfirmedMemoryClaim(question, text string) bool {
	if e.receipt != "" {
		return false
	}
	for _, match := range memoryConfirmation.FindAllStringIndex(text, -1) {
		// “已记住的质检代码” describes an existing record in a recall answer.
		// A standalone confirmation elsewhere in the reply still needs a receipt.
		rest := text[match[1]:]
		if !strings.HasPrefix(rest, "的") && !strings.HasPrefix(rest, "了的") {
			return true
		}
	}
	return bareConfirmation.MatchString(strings.TrimSpace(text)) && !e.fileSaved && !fileContext.MatchString(question)
}

// General chat still searches with the entire user message. For an explicit
// lookup the embedding should describe the subject (e.g. “质检代码”), using
// the same collection, similarity threshold and context as normal recall.
var rememberedSubject = regexp.MustCompile(`^(?:请问|请告诉我|你|还)*(?:记住|记下|保存|存储)(?:过的|了的|的)(.+?)(?:是什么|是多少|有哪些|是哪一个|是哪个|是哪些|是什么内容)[？?。！!\s]*$`)

func (a *Agent) directMemoryQuery(question string, specs []ToolSpec) string {
	cfg := &a.config
	if !cfg.VerifyToolAnswers || cfg.Memory == nil || !memoryReadRequest(question) || !visibleTool(specs, "search_saved_correct_tool_uses") {
		return ""
	}
	if cfg.PromptBuilder != nil || cfg.EnhancePrompt != nil || cfg.EnhanceUserMessages != nil || len(cfg.Filters) > 0 {
		return ""
	}
	if !cfg.DirectTextRecall && (cfg.SystemPrompt != "" || len(cfg.Middleware) > 0 || len(cfg.LLMMiddlewares) > 0) {
		return ""
	}
	if match := rememberedSubject.FindStringSubmatch(strings.TrimSpace(question)); match != nil {
		if subject := strings.TrimSpace(match[1]); subject != "" {
			return subject
		}
	}
	return question
}

func (a *Agent) searchTextMemory(ctx context.Context, t Turn, query string, limit int) ([]MemoryMatch, error) {
	child, trace := a.startSpan(ctx, "agent.llm_context.enhance_system_prompt", "agent.llm_context.enhance_system_prompt.duration", nil, map[string]string{"enhancer": "memory"})
	found, err := a.config.Memory.Search(child, MemoryQuery{Kind: "text", Text: query, Limit: limit, Threshold: a.config.TextMemoryRecall.Threshold})
	trace.set("result_count", len(found))
	trace.end(err)
	if err != nil {
		a.observe(ctx, Event{Operation: "memory.search", Turn: t, Err: err})
	}
	return found, err
}

func (a *Agent) answerFromMemory(ctx context.Context, t Turn, c Conversation, messages []Message, emit func(Component) error, query string) error {
	found, err := a.searchTextMemory(ctx, t, query, 1)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	content := "未找到达到相似度要求的匹配记忆。"
	progress := status("warning", "未找到匹配记忆")
	if err != nil {
		content = "记忆检索失败，请稍后重试。"
		progress = status("error", "记忆检索失败")
	} else if len(found) > 0 && found[0].Score >= a.config.TextMemoryRecall.Threshold && found[0].Memory.Kind == "text" && strings.TrimSpace(found[0].Memory.Content) != "" {
		content = found[0].Memory.Content
		progress = status("success", "已读取匹配记忆")
	}
	return a.finishVerifiedText(ctx, t, c, messages, emit, content, progress, false)
}
