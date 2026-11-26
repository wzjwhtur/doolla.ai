package doolla

import (
	"regexp"
	"strings"
)

var (
	questionPart   = regexp.MustCompile(`(?i)[,，。；;、]|以及|同时|并且|并(?:(?:给出|查询|列出|统计))|和|\band\b`)
	metricContext  = regexp.MustCompile(`(?i)(?:本|上|下|当|这)(?:个)?(?:月|季度|周|年度)|今年|去年|本期|[\p{Han}]{1,8}(?:率|产值|金额)|\b(?:this|current|last|previous)\s+(?:month|week|quarter|year|period)\b`)
	formulaRequest = regexp.MustCompile(`(?i)(?:公式|如何计算|怎么算|怎么计算|是什么意思|的定义|^什么是)|\b(?:formula|definition)\b|^(?:how (?:do (?:you|we|i)|to) (?:calculate|compute)|how is .+ calculated)\b`)
	// Standalone arithmetic has its operands in the user message. Keep this
	// whole-question match narrow: "25% of today's sales" still needs live data.
	arithmeticRequest = regexp.MustCompile(`(?i)^(?:请)?(?:计算|算一下|what is|calculate|compute)?\s*[-+]?\d[\d.,\s()%％]*(?:[+*/÷×−-]|of|的)\s*[-+]?\d[\d.,\s()%％+*/÷×−-]*(?:等于多少|是多少|as a (?:percentage|percent|decimal))?\s*[?？。.!！]?$`)
	measuredValue     = regexp.MustCompile(`(?i)(?:[0-9０-９][0-9０-９.,，]*\s*(?:[%％]|个?百分点|\bpercent(?:age points?)?\b)|百分之[零一二三四五六七八九十百千万点\d]+)`)
	reportedNumber    = regexp.MustCompile(`(?i)(?:^\s*|[=:：]|为|是|达(?:到)?|约|\b(?:is|was|were|at|of)\s+)\s*[+-]?[0-9０-９]`)
	// A single request for scope can contain numeric identifiers (line 1 or
	// line 2). A statement followed by a question must still be checked.
	scopeQuestion = regexp.MustCompile(`(?i)^(?:请问(?:您|你)?(?:要(?:看|查|选)|是指|指的|需要看|哪)|(?:您|你)指的是|which\b|do you mean\b|(?:can|could) you (?:clarify|specify)\b)[^.。！？?!;；:：]*[?？]$`)
)

// SQL help and catalog counts can be answered without business rows. Examine
// each clause independently so a schema clause doesn't exempt a second KPI.
func (e *turnEvidence) inspectSQLRequest(question string) {
	if arithmeticRequest.MatchString(question) || writeStatement.MatchString(question) {
		return
	}
	for _, part := range questionPart.Split(question, -1) {
		part = strings.TrimSpace(part)
		if part == "" || explanatoryRequest.MatchString(part) || formulaRequest.MatchString(part) {
			continue
		}
		catalog := catalogQuestion.MatchString(part)
		explicitSQL := strings.Contains(strings.ToLower(part), "run_sql") || strings.Contains(strings.ToLower(part), " from ")
		if catalog && !explicitSQL {
			continue
		}
		e.watchSQL = true
		e.needRows = e.needRows || dataRequest.MatchString(part)
		e.metricContext = e.metricContext || metricContext.MatchString(part)
	}
}

func (e *turnEvidence) observeSQLAnswer(content string) bool {
	if e.watchSQL && scopeQuestion.MatchString(strings.TrimSpace(content)) && !queryClaim.MatchString(content) && !measuredValue.MatchString(content) {
		return true
	}
	if !e.watchSQL || e.hasRows {
		return false
	}
	// Inspect the proposed answer too. KPI names and abbreviations are open
	// ended, but percentages and numeric answers to period questions are not.
	if queryClaim.MatchString(content) || measuredValue.MatchString(content) || (e.metricContext && reportedNumber.MatchString(content)) {
		e.needRows = true
	}
	return false
}
