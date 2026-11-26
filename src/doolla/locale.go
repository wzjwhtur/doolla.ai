package doolla

import (
	"context"
	"regexp"

	"doolla.ai/sdk/internal/i18n"
)

// WithLocale selects a language for SDK calls. Unsupported tags inherit the
// existing preference, or use the agent default when there is none.
func WithLocale(ctx context.Context, locale string) context.Context {
	return i18n.WithLocale(ctx, locale)
}

// CurrentLocale returns the effective language of the current request.
func CurrentLocale(ctx context.Context) string {
	return i18n.Resolve(i18n.Requested(ctx), "", "")
}

var answerLanguage = regexp.MustCompile(`(?i)(中文回答|用(?:简体)?中文(?:回答|回复)|(?:answer|respond|reply) in (?:simplified )?chinese|英文回答|用英文(?:回答|回复)|用英语(?:回答|回复)|(?:answer|respond|reply) in english)`)

func requestedAnswerLocale(question string) string {
	match := answerLanguage.FindStringSubmatchIndex(question)
	if match == nil {
		return ""
	}
	phrase := question[match[0]:match[1]]
	if englishAnswer.MatchString(phrase) {
		return i18n.English
	}
	return i18n.Chinese
}

var englishAnswer = regexp.MustCompile(`(?i)英文|英语|english`)
