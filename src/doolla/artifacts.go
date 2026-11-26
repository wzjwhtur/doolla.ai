package doolla

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

type Artifacts struct{ Directory string }

// All Artifacts instances in the process share lock stripes. An edit holds its
// user's lock over both the read and the publication of the replacement.
var artifactLocks [256]sync.RWMutex

func artifactLock(user User) *sync.RWMutex {
	return &artifactLocks[sha256.Sum256([]byte(user.ID))[0]]
}

// User-specific roots keep model-provided filenames inside the owning workspace.
// os.Root also prevents escaping through symlinks.
func (a Artifacts) root(user User) (*os.Root, error) {
	dir := filepath.Join(a.Directory, fmt.Sprintf("%x", sha256.Sum256([]byte(user.ID)))[:16])
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return os.OpenRoot(dir)
}

type ResultFiles interface {
	Write(User, string, []byte) error
	Read(User, string) ([]byte, error)
}

func (a Artifacts) Write(user User, name string, data []byte) error {
	return a.write(context.Background(), user, name, data, false)
}

func (a Artifacts) WriteFile(ctx context.Context, user User, name, content string, overwrite bool) error {
	return a.write(ctx, user, name, []byte(content), overwrite)
}

func (a Artifacts) write(ctx context.Context, user User, name string, data []byte, overwrite bool) error {
	lock := artifactLock(user)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := a.root(user)
	if err != nil {
		return err
	}
	defer root.Close()
	return publishFile(ctx, root, name, data, overwrite)
}

func publishFile(ctx context.Context, root *os.Root, name string, data []byte, overwrite bool) error {
	dir := filepath.Dir(name)
	if err := root.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".write-"+newID())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if overwrite {
		return root.Rename(tmp, name)
	}
	// Link publishes the finished bytes without replacing a concurrent create.
	return root.Link(tmp, name)
}

func (a Artifacts) Read(u User, name string) ([]byte, error) {
	root, err := a.root(u)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return fs.ReadFile(root.FS(), name)
}

func readText(root *os.Root, name string) (string, error) {
	data, err := fs.ReadFile(root.FS(), name)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("file %q contains invalid UTF-8", name)
	}
	return string(data), nil
}

func (a Artifacts) ReadFile(ctx context.Context, u User, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	root, err := a.root(u)
	if err != nil {
		return "", err
	}
	defer root.Close()
	return readText(root, name)
}

func (a Artifacts) EditFile(ctx context.Context, u User, name string, transform func(string) (string, error)) error {
	lock := artifactLock(u)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := a.root(u)
	if err != nil {
		return err
	}
	defer root.Close()
	before, err := readText(root, name)
	if err != nil {
		return err
	}
	after, err := transform(before)
	if err != nil {
		return err
	}
	if after == before {
		return ctx.Err()
	}
	return publishFile(ctx, root, name, []byte(after), true)
}

func (a Artifacts) ListFiles(ctx context.Context, u User, dir string) ([]string, error) {
	lock := artifactLock(u)
	lock.RLock()
	defer lock.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := a.root(u)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

func (a Artifacts) SearchFiles(ctx context.Context, u User, query string, opts FileSearchOptions) ([]FileSearchMatch, error) {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" || opts.MaxResults < 1 || opts.MaxResults > 100 {
		return nil, fmt.Errorf("query and max_results from 1 to 100 required")
	}
	lock := artifactLock(u)
	lock.RLock()
	defer lock.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := a.root(u)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	matches := []FileSearchMatch{}
	err = fs.WalkDir(root.FS(), ".", func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !e.Type().IsRegular() {
			return nil
		}
		snippet := ""
		if strings.Contains(strings.ToLower(e.Name()), needle) {
			snippet = "[filename match]"
		}
		if opts.IncludeContent {
			if info, err := e.Info(); err == nil && info.Size() <= 1_000_000 {
				if content, err := readText(root, path); err == nil {
					if found := fileSnippet(content, needle); found != "" {
						snippet = found
					}
				}
			}
		}
		if snippet != "" {
			matches = append(matches, FileSearchMatch{Path: path, Snippet: snippet})
		}
		if len(matches) == opts.MaxResults {
			return fs.SkipAll
		}
		return nil
	})
	return matches, err
}

func fileSnippet(content, needle string) string {
	lower := strings.ToLower(content)
	idx := strings.Index(lower, needle)
	if idx < 0 {
		return ""
	}
	// Byte lengths may change under Unicode lowercasing. Map the match
	// back to the original text by rune position.
	pos := utf8.RuneCountInString(lower[:idx])
	chars := []rune(content)
	start := max(0, pos-60)
	end := min(len(chars), pos+utf8.RuneCountInString(needle)+60)
	snippet := strings.TrimSpace(strings.ReplaceAll(string(chars[start:end]), "\n", " "))
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(chars) {
		snippet += "…"
	}
	return snippet
}
