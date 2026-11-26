package doolla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
)

type fileEdit struct {
	Start   int    `json:"start_line"`
	End     *int   `json:"end_line"`
	Content string `json:"new_content"`
}
type fileArgs struct {
	Filename, Directory, Query, Content string
	IncludeContent                      bool `json:"include_content"`
	MaxResults                          int  `json:"max_results"`
	Overwrite                           bool
	Edits                               []fileEdit
}

// FileToolStorage operates within the workspace of the supplied user. EditFile
// must apply the transformation to the current contents atomically, leaving
// the original file intact if the transformation fails.
type FileToolStorage interface {
	ListFiles(context.Context, User, string) ([]string, error)
	ReadFile(context.Context, User, string) (string, error)
	SearchFiles(context.Context, User, string, FileSearchOptions) ([]FileSearchMatch, error)
	WriteFile(ctx context.Context, user User, name, content string, overwrite bool) error
	EditFile(ctx context.Context, user User, name string, transform func(string) (string, error)) error
}

type FileSearchOptions struct {
	IncludeContent bool
	MaxResults     int
}

type FileSearchMatch struct {
	Path    string `json:"path"`
	Snippet string `json:"snippet,omitempty"`
}

func FileTools(storage FileToolStorage) []Registration {
	if storage == nil {
		storage = Artifacts{Directory: "."}
	}
	fields := map[string]any{
		"filename": strField("Relative file name"), "directory": strField("Relative directory"),
		"query": strField("Text to find"), "content": strField("File contents"),
		"include_content": map[string]any{"type": "boolean", "default": true}, "max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 20},
		"overwrite": map[string]any{"type": "boolean", "default": false},
		"edits":     map[string]any{"type": "array", "minItems": 1, "items": objectSchema(map[string]any{"start_line": map[string]any{"type": "integer", "minimum": 1}, "end_line": map[string]any{"type": []string{"integer", "null"}}, "new_content": strField("Replacement lines")}, "start_line")},
	}
	var out []Registration
	for _, def := range []struct {
		name, desc         string
		required, optional []string
	}{
		{"list_files", "List files in a directory.", nil, []string{"directory"}},
		{"read_file", "Read a file.", []string{"filename"}, nil},
		{"search_files", "Search names and file content.", []string{"query"}, []string{"include_content", "max_results"}},
		{"write_file", "Write a workspace file.", []string{"filename", "content"}, []string{"overwrite"}},
		{"edit_file", "Apply line edits to a workspace file.", []string{"filename", "edits"}, nil},
	} {
		props := map[string]any{}
		for _, field := range append(append([]string{}, def.required...), def.optional...) {
			props[field] = fields[field]
		}
		tool := functionTool{ToolSpec{def.name, def.desc, objectSchema(props, def.required...)}, func(ctx context.Context, u User, raw json.RawMessage) (ToolResult, error) {
			args := fileArgs{Directory: ".", IncludeContent: true, MaxResults: 20}
			if err := json.Unmarshal(raw, &args); err != nil {
				return ToolResult{}, err
			}
			switch def.name {
			case "list_files":
				names, err := storage.ListFiles(ctx, u, args.Directory)
				if err != nil {
					return ToolResult{}, err
				}
				return fileListResult("Files", names)
			case "read_file":
				text, err := storage.ReadFile(ctx, u, args.Filename)
				if err != nil {
					return ToolResult{}, err
				}
				return ToolResult{Content: text, Component: &Component{Type: "card", Data: map[string]any{"title": "Contents of " + args.Filename, "content": text}, Simple: plainToolText(text)}}, nil
			case "search_files":
				query := strings.TrimSpace(args.Query)
				if query == "" || args.MaxResults < 1 || args.MaxResults > 100 {
					return ToolResult{}, errors.New("query and max_results from 1 to 100 required")
				}
				matches, err := storage.SearchFiles(ctx, u, query, FileSearchOptions{IncludeContent: args.IncludeContent, MaxResults: args.MaxResults})
				if err != nil {
					return ToolResult{}, err
				}
				return fileListResult("Search results", matches)
			case "write_file":
				if err := storage.WriteFile(ctx, u, args.Filename, args.Content, args.Overwrite); err != nil {
					return ToolResult{}, err
				}
				return fileSaved(args.Filename), nil
			case "edit_file":
				return applyFileEdits(ctx, storage, u, args)
			}
			return ToolResult{}, fmt.Errorf("unknown file tool %q", def.name)
		}}
		out = append(out, Registration{Tool: tool})
	}
	return out
}

func applyFileEdits(ctx context.Context, storage FileToolStorage, u User, args fileArgs) (ToolResult, error) {
	var before, after string
	err := storage.EditFile(ctx, u, args.Filename, func(current string) (string, error) {
		updated, err := editLines(current, args.Edits)
		before, after = current, updated
		return updated, err
	})
	if err != nil {
		return ToolResult{}, err
	}
	if before == after {
		msg := fmt.Sprintf("No changes applied to '%s' (content already up to date).", args.Filename)
		return ToolResult{Content: msg, Component: &Component{Type: "notification", Data: map[string]any{"level": "info", "message": msg}, Simple: plainToolText(msg)}}, nil
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: difflib.SplitLines(before), B: difflib.SplitLines(after), FromFile: "a/" + args.Filename, ToFile: "b/" + args.Filename, Context: 3})
	if err != nil {
		return ToolResult{}, err
	}
	summary := fmt.Sprintf("Updated '%s' with %d edit(s).", args.Filename, len(args.Edits))
	return ToolResult{Content: summary + "\n\n" + diff, Component: &Component{Type: "card", Data: map[string]any{"title": "Edited " + args.Filename, "content": diff}, Simple: plainToolText(summary)}}, nil
}

func editLines(text string, edits []fileEdit) (string, error) {
	if len(edits) == 0 {
		return "", errors.New("edits must contain at least one change")
	}
	lines := splitFileLines(text)
	edits = slices.Clone(edits)
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].Start > edits[j].Start })
	for _, edit := range edits {
		first := edit.Start - 1
		last := edit.Start
		if edit.End != nil {
			last = *edit.End
		}
		if first < 0 || first > len(lines) || last < first || last > len(lines) {
			return "", fmt.Errorf("invalid lines %d-%d", edit.Start, last)
		}
		lines = append(lines[:first:first], append(splitFileLines(edit.Content), lines[last:]...)...)
	}
	return strings.Join(lines, ""), nil
}

// Keep line terminators so edits preserve the original bytes outside the range.
// This recognizes the boundaries accepted by Python str.splitlines(keepends=True).
func splitFileLines(text string) []string {
	var lines []string
	for len(text) > 0 {
		i := strings.IndexAny(text, "\r\n\v\f\x1c\x1d\x1e\u0085\u2028\u2029")
		if i < 0 {
			return append(lines, text)
		}
		_, width := utf8.DecodeRuneInString(text[i:])
		if strings.HasPrefix(text[i:], "\r\n") {
			width = 2
		}
		lines = append(lines, text[:i+width])
		text = text[i+width:]
	}
	return lines
}

func plainToolText(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

func fileListResult(title string, v any) (ToolResult, error) {
	result, err := jsonResult(v)
	if err != nil {
		return ToolResult{}, err
	}
	result.Component = &Component{Type: "card", Data: map[string]any{"title": title, "content": result.Content}, Simple: plainToolText(result.Content)}
	return result, nil
}
func fileSaved(name string) ToolResult {
	return ToolResult{Content: "File saved.", Component: &Component{Type: "notification", Data: map[string]any{"level": "success", "message": "File saved."}, Simple: plainToolText("Saved " + name)}}
}
