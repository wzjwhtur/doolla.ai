// Package i18n resolves the language of a request and formats product messages.
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	"golang.org/x/text/language"
)

const (
	English = "en"
	Chinese = "zh-CN"
)

//go:embed catalog/*.json
var files embed.FS

var catalogs = loadCatalogs()
var placeholder = regexp.MustCompile(`\{([0-9]+)\}`)

func loadCatalogs() map[string]map[string]string {
	catalogs := make(map[string]map[string]string)
	for _, locale := range []string{English, Chinese} {
		data, err := files.ReadFile("catalog/" + locale + ".json")
		if err != nil {
			panic(err)
		}
		var catalog map[string]string
		if err := json.Unmarshal(data, &catalog); err != nil {
			panic(err)
		}
		catalogs[locale] = catalog
	}
	return catalogs
}

func Normalize(locale string) string {
	tag, err := language.Parse(locale)
	if err != nil {
		return ""
	}
	base, _ := tag.Base()
	switch base.String() {
	case "en":
		return English
	case "zh":
		script, _ := tag.Script()
		if script.String() == "Hans" {
			return Chinese
		}
	}
	return ""
}

func Resolve(explicit, acceptLanguage, fallback string) string {
	if locale := Normalize(explicit); locale != "" {
		return locale
	}
	if tags, _, err := language.ParseAcceptLanguage(acceptLanguage); err == nil {
		for _, tag := range tags {
			if locale := Normalize(tag.String()); locale != "" {
				return locale
			}
		}
	}
	if locale := Normalize(fallback); locale != "" {
		return locale
	}
	return English
}

type localeKey struct{}

func WithLocale(ctx context.Context, locale string) context.Context {
	if locale := Normalize(locale); locale != "" {
		return context.WithValue(ctx, localeKey{}, locale)
	}
	return ctx
}

func Requested(ctx context.Context) string {
	locale, _ := ctx.Value(localeKey{}).(string)
	return locale
}

func T(ctx context.Context, key string, args ...any) string { return Format(Requested(ctx), key, args...) }

func Format(locale, key string, args ...any) string {
	message, ok := catalogs[locale][key]
	if !ok {
		message, ok = catalogs[English][key]
	}
	if !ok {
		return key
	}
	return placeholder.ReplaceAllStringFunc(message, func(token string) string {
		index, err := strconv.Atoi(token[1 : len(token)-1])
		if err != nil || index >= len(args) {
			return token
		}
		return fmt.Sprint(args[index])
	})
}
