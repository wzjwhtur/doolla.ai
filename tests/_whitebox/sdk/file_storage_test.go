package doolla

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type recordingFileStorage struct {
	t       *testing.T
	ctx     context.Context
	user    User
	content string
	calls   []string
}

func (s *recordingFileStorage) record(ctx context.Context, u User, op string) {
	s.t.Helper()
	if ctx != s.ctx || !reflect.DeepEqual(u, s.user) {
		s.t.Fatalf("lost caller context or user in %s", op)
	}
	s.calls = append(s.calls, op)
}
func (s *recordingFileStorage) ListFiles(ctx context.Context, u User, dir string) ([]string, error) {
	s.record(ctx, u, "list "+dir)
	return []string{"x"}, nil
}
func (s *recordingFileStorage) ReadFile(ctx context.Context, u User, path string) (string, error) {
	s.record(ctx, u, "read "+path)
	return s.content, nil
}
func (s *recordingFileStorage) SearchFiles(ctx context.Context, u User, q string, o FileSearchOptions) ([]FileSearchMatch, error) {
	s.record(ctx, u, fmt.Sprintf("search %s %t %d", q, o.IncludeContent, o.MaxResults))
	return []FileSearchMatch{{Path: "x", Snippet: "found"}}, nil
}
func (s *recordingFileStorage) WriteFile(ctx context.Context, u User, path, data string, overwrite bool) error {
	s.record(ctx, u, fmt.Sprintf("write %s %t", path, overwrite))
	s.content = data
	return nil
}
func (s *recordingFileStorage) EditFile(ctx context.Context, u User, path string, transform func(string) (string, error)) error {
	s.record(ctx, u, "edit "+path)
	next, err := transform(s.content)
	if err == nil {
		s.content = next
	}
	return err
}

func TestFileToolStorageInjection(t *testing.T) {
	store := &recordingFileStorage{t: t, ctx: t.Context(), user: User{ID: "editor", Groups: []string{"a"}}}
	var reg ToolRegistry
	if err := reg.Register(FileTools(store)...); err != nil {
		t.Fatal(err)
	}
	call := func(name, args string) (ToolResult, error) {
		return reg.Execute(store.ctx, store.user, ToolCall{Function: FunctionCall{Name: name, Arguments: args}})
	}
	for _, tc := range []struct{ name, args, want string }{
		{"list_files", `{}`, "x"},
		{"write_file", `{"filename":"x","content":"one"}`, "saved"},
		{"write_file", `{"filename":"x","content":"one\rtwo\u2028","overwrite":true}`, "saved"},
		{"edit_file", `{"filename":"x","edits":[{"start_line":2,"end_line":null,"new_content":"three\n"}]}`, "+++ b/x"},
		{"read_file", `{"filename":"x"}`, "one\rthree\n"},
		{"edit_file", `{"filename":"x","edits":[{"start_line":2,"new_content":"three\n"}]}`, "No changes"},
		{"search_files", `{"query":" found "}`, "found"},
		{"search_files", `{"query":"x","max_results":1,"include_content":false}`, "found"},
	} {
		result, err := call(tc.name, tc.args)
		if err != nil || !strings.Contains(result.Content, tc.want) {
			t.Fatalf("%s => %q, %v", tc.name, result.Content, err)
		}
	}
	want := []string{"list .", "write x false", "write x true", "edit x", "read x", "edit x", "search found true 20", "search x false 1"}
	if !reflect.DeepEqual(store.calls, want) {
		t.Fatal(store.calls)
	}
	for _, args := range []string{`{"query":"a","max_results":0}`, `{"query":"a","max_results":101}`, `{"query":"  "}`} {
		if _, err := call("search_files", args); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
	if _, err := call("edit_file", `{"filename":"x","edits":[]}`); err == nil {
		t.Fatal("empty edits accepted")
	}
	if !reflect.DeepEqual(store.calls, want) {
		t.Fatal("invalid input reached storage", store.calls)
	}
}

func TestArtifactsConcurrentEdits(t *testing.T) {
	dir := t.TempDir()
	files := Artifacts{dir}
	u := User{ID: "u"}
	if err := files.Write(u, "sub/count", []byte("0")); err != nil {
		t.Fatal(err)
	}
	const workers = 24
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			err := (Artifacts{dir}).EditFile(t.Context(), u, "sub/count", func(text string) (string, error) {
				n, err := strconv.Atoi(text)
				return strconv.Itoa(n + 1), err
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	expect := strconv.Itoa(workers)
	check := func() {
		t.Helper()
		got, err := files.Read(u, "sub/count")
		if string(got) != expect || err != nil {
			t.Fatalf("got %q, %v; expected %q", got, err, expect)
		}
	}
	check()
	if err := files.Write(u, "sub/count", []byte("replace")); !errors.Is(err, fs.ErrExist) {
		t.Fatal(err)
	}
	bad := errors.New("bad edit")
	if err := files.EditFile(t.Context(), u, "sub/count", func(string) (string, error) { return "", bad }); !errors.Is(err, bad) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := files.EditFile(ctx, u, "sub/count", func(string) (string, error) { cancel(); return "changed", nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	check()
	if _, err := files.ReadFile(t.Context(), User{ID: "other"}, "sub/count"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestArtifactsSearchOptions(t *testing.T) {
	files := Artifacts{t.TempDir()}
	u := User{ID: "u"}
	for name, data := range map[string][]byte{
		"x/one.txt": []byte(strings.Repeat("类", 75) + "QUERY\nabc" + strings.Repeat("類", 70)),
		"query.dat": {0xff},
		"oversized": []byte(strings.Repeat("q", 1_000_001) + "query"),
	} {
		if err := files.Write(u, name, data); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := files.ReadFile(t.Context(), u, "query.dat"); err == nil {
		t.Fatal("accepted invalid UTF-8")
	}
	found, err := files.SearchFiles(t.Context(), u, "Query", FileSearchOptions{true, 20})
	if err != nil || len(found) != 2 || found[0] != (FileSearchMatch{"query.dat", "[filename match]"}) || !strings.HasPrefix(found[1].Snippet, "…") || !strings.HasSuffix(found[1].Snippet, "…") || !strings.Contains(found[1].Snippet, "QUERY abc") {
		t.Fatalf("%+v %v", found, err)
	}
	found, err = files.SearchFiles(t.Context(), u, "Query\nabc", FileSearchOptions{true, 1})
	if err != nil || len(found) != 1 || found[0].Path != "x/one.txt" {
		t.Fatalf("%+v %v", found, err)
	}
	found, err = files.SearchFiles(t.Context(), u, "Query", FileSearchOptions{false, 20})
	if err != nil || len(found) != 1 || found[0].Path != "query.dat" {
		t.Fatalf("%+v %v", found, err)
	}
}
