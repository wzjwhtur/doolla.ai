// Package memory connects Doolla's memory tools to existing vector indexes.
package memory

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	doolla "doolla.ai/sdk"
)

type Config struct {
	Provider string
	// Provision creates a missing collection. Existing schemas are never replaced.
	Provision bool
	// ControlEndpoint and Region are used for Pinecone index discovery/creation.
	ControlEndpoint, Region string
	// Endpoint is the data-plane URL. For Pinecone use the index host.
	Endpoint, Collection, APIKey              string
	Tenant, Database, Namespace, Organization string
	// Directory selects local storage. Metric selects the FAISS distance metric.
	Directory, Metric string
	Dimension         int
	// MarqoModel selects the server-side embedding model for a new Marqo index.
	// An empty value uses the server default. Existing indexes keep their model.
	MarqoModel string
	// Embed must use the model that was used to build an existing index.
	Embed func(context.Context, string) ([]float32, error)
	// HTTPClient can supply TLS, AWS request signing or custom authentication.
	HTTPClient *http.Client
}

type fields = map[string]any
type entry = doolla.MemoryEntry
type query = doolla.MemoryQuery
type match = doolla.MemoryMatch

type driver interface {
	put(context.Context, entry) error
	search(context.Context, query) ([]match, error)
	scan(context.Context, func(entry) error) error
	get(context.Context, string) (*entry, error)
	remove(context.Context, string) error
}
type store struct{ driver }
type remote struct {
	Config
	path string
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Open connects to a provisioned index using the selected provider's native API.
// Opening a Chroma collection resolves its name to its stable UUID.
func Open(ctx context.Context, c Config) (doolla.MemoryStore, error) {
	if c.Provider == "qdrant" {
		if c.Endpoint == "" {
			return openQdrantLocal(ctx, c)
		}
		if c.Directory != "" {
			return nil, errors.New("select a Qdrant directory or an HTTP endpoint")
		}
	}
	if c.Provider == "faiss" {
		return openFlat(c)
	}
	if (c.Provider == "chroma" || c.Provider == "chromadb") && c.Directory != "" && c.Endpoint == "" {
		return nil, errors.New("local Chroma directories require the optional chromalocal runtime")
	}
	if c.Endpoint == "" {
		if c.Provider == "premium" {
			return nil, errors.New("premium memory requires an explicit endpoint")
		}
		defaults := map[string]string{"chroma": "http://localhost:8000", "chromadb": "http://localhost:8000", "milvus": "http://localhost:19530", "weaviate": "http://localhost:8080", "marqo": "http://localhost:8882", "opensearch": "http://localhost:9200"}
		c.Endpoint = defaults[c.Provider]
		if c.Provider == "pinecone" && c.Provision {
			c.Endpoint = c.ControlEndpoint
			if c.Endpoint == "" {
				c.Endpoint = "https://api.pinecone.io"
			}
		}
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("memory endpoint must be an absolute HTTP(S) URL")
	}
	c.Endpoint = strings.TrimRight(c.Endpoint, "/")
	if c.Dimension == 0 {
		c.Dimension = 384
	}
	if c.Dimension < 1 {
		return nil, errors.New("dimension must be positive")
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if c.Collection == "" {
		switch c.Provider {
		case "weaviate":
			c.Collection = "ToolMemory"
		case "chroma", "chromadb", "milvus", "qdrant":
			c.Collection = "tool_memories"
		default:
			c.Collection = "tool-memories"
		}
	}
	if !namePattern.MatchString(c.Collection) {
		return nil, errors.New("invalid collection name")
	}
	r := &remote{Config: c}
	var d driver
	switch c.Provider {
	case "qdrant":
		r.path = "/collections/" + c.Collection + "/points"
		d = qdrant{r}
	case "pinecone":
		d = pinecone{r}
	case "milvus":
		d = milvus{r}
	case "opensearch":
		r.path = "/" + c.Collection
		d = openSearch{r}
	case "azuresearch":
		r.path = "/indexes/" + c.Collection + "/docs"
		d = azure{r}
	case "marqo":
		r.path = "/indexes/" + c.Collection
		d = marqo{r}
	case "weaviate":
		if !regexp.MustCompile(`^[A-Z][_0-9A-Za-z]*$`).MatchString(c.Collection) {
			return nil, errors.New("invalid Weaviate class name")
		}
		d = weaviate{r}
	case "chroma", "chromadb":
		if c.Embed == nil {
			return nil, errors.New("Chroma requires the embedding model used by the collection")
		}
		if c.Tenant == "" {
			c.Tenant = "default_tenant"
		}
		if c.Database == "" {
			c.Database = "default_database"
		}
		base := "/api/v2/tenants/" + url.PathEscape(c.Tenant) + "/databases/" + url.PathEscape(c.Database) + "/collections/"
		var col struct{ ID string }
		err := r.call(ctx, "GET", base+url.PathEscape(c.Collection), nil, &col)
		if errors.Is(err, errMissing) && c.Provision {
			err = r.call(ctx, "POST", strings.TrimSuffix(base, "/"), fields{"name": c.Collection, "get_or_create": true}, &col)
		}
		if err != nil {
			return nil, err
		}
		if col.ID == "" {
			return nil, errors.New("Chroma collection has no id")
		}
		r.path = base + url.PathEscape(col.ID)
		d = chroma{r}
	case "premium":
		return premium{r}, nil
	default:
		return nil, fmt.Errorf("unknown memory provider %q", c.Provider)
	}
	if c.Provision {
		if err := r.provision(ctx); err != nil {
			return nil, err
		}
	}
	return &store{d}, nil
}
func prepare(e entry) (entry, error) {
	if e.Kind != "tool" && e.Kind != "text" {
		return e, fmt.Errorf("unknown memory kind %q", e.Kind)
	}
	if e.Kind == "tool" && e.ToolName == "" {
		return e, errors.New("tool memory requires tool name")
	}
	if e.ID == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return e, err
		}
		b[6] = b[6]&0x0f | 0x40
		b[8] = b[8]&0x3f | 0x80
		e.ID = fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	return e, nil
}
func (s *store) Save(ctx context.Context, e entry) error {
	e, err := prepare(e)
	if err != nil {
		return err
	}
	return s.put(ctx, e)
}
func (s *store) Search(ctx context.Context, q query) ([]match, error) {
	if q.Limit <= 0 {
		return nil, nil
	}
	m, err := s.search(ctx, q)
	if err != nil {
		return nil, err
	}
	valid := m[:0]
	for _, v := range m {
		if v.Memory.Kind == q.Kind && (q.Kind != "tool" || v.Memory.Success) && (q.ToolName == "" || v.Memory.ToolName == q.ToolName) && v.Score >= q.Threshold {
			valid = append(valid, v)
		}
	}
	// Python 的 rank 从 1 开始，表示本次查询最终返回的顺序。先过滤和排序，
	// 再赋排名，这样调用方看到的名次与召回列表一致。
	sort.SliceStable(valid, func(i, j int) bool { return valid[i].Score > valid[j].Score })
	valid = valid[:min(len(valid), q.Limit)]
	for i := range valid {
		valid[i].Rank = i + 1
	}
	return valid, nil
}
func (s *store) Recent(ctx context.Context, kind string, limit int) ([]entry, error) {
	if limit <= 0 {
		return nil, nil
	}
	var all []entry
	err := s.scan(ctx, func(e entry) error {
		if e.Kind == kind {
			all = append(all, e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Timestamp.Equal(all[j].Timestamp) {
			return all[i].ID < all[j].ID
		}
		return all[i].Timestamp.After(all[j].Timestamp)
	})
	return all[:min(limit, len(all))], nil
}
func (s *store) Delete(ctx context.Context, kind, id string) (bool, error) {
	e, err := s.get(ctx, id)
	if errors.Is(err, errMissing) || (err == nil && (e == nil || e.Kind != kind)) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = s.remove(ctx, id)
	if errors.Is(err, errMissing) {
		return false, nil
	}
	return err == nil, err
}
func (s *store) Clear(ctx context.Context, tool string, before time.Time) (int, error) {
	var ids []string
	err := s.scan(ctx, func(e entry) error {
		if (tool == "" || e.ToolName == tool) && (before.IsZero() || (!e.Timestamp.IsZero() && e.Timestamp.Before(before))) {
			ids = append(ids, e.ID)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		if err := s.remove(ctx, id); err != nil {
			if errors.Is(err, errMissing) {
				continue
			}
			return n, err
		}
		n++
	}
	return n, nil
}

var errMissing = errors.New("memory resource not found")

func (r *remote) call(ctx context.Context, method, path string, body, out any) error {
	var in io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		in = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.Endpoint+path, in)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.APIKey != "" {
		switch r.Provider {
		case "chroma", "chromadb":
			req.Header.Set("x-chroma-token", r.APIKey)
		case "pinecone", "azuresearch", "qdrant":
			req.Header.Set("api-key", r.APIKey)
		case "marqo":
			req.Header.Set("x-api-key", r.APIKey)
		default:
			req.Header.Set("Authorization", "Bearer "+r.APIKey)
		}
	}
	if r.Provider == "marqo" {
		req.Header.Set("x-marqo-index-name", r.Collection)
	}
	if r.Organization != "" && r.Provider == "premium" {
		req.Header.Set("X-Organization-ID", r.Organization)
	}
	if r.Provider == "pinecone" {
		req.Header.Set("X-Pinecone-Api-Version", "2025-10")
	}
	res, err := r.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return errMissing
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("%s %s: HTTP %d: %s", r.Provider, method, res.StatusCode, b)
	}
	if out != nil {
		decoder := json.NewDecoder(res.Body)
		decoder.UseNumber()
		return decoder.Decode(out)
	}
	return nil
}
func (r *remote) vector(ctx context.Context, text string) ([]float32, error) {
	if r.Embed != nil {
		v, err := r.Embed(ctx, text)
		if err == nil && len(v) != r.Dimension {
			return nil, fmt.Errorf("embedding dimension %d, expected %d", len(v), r.Dimension)
		}
		return v, err
	}
	return legacyVector(text, r.Dimension), nil
}

// legacyVector reproduces the MD5 input vectors used by Doolla's Python adapters.
func legacyVector(s string, n int) []float32 {
	h := md5.Sum([]byte(s))
	x := new(big.Int).SetBytes(h[:])
	hundred := big.NewInt(100)
	var rem big.Int
	v := make([]float32, n)
	for i := range v {
		v[i] = float32(rem.Mod(x, hundred).Int64()) / 100
		x.Rsh(x, 1)
	}
	return v
}
func textOf(e entry) string {
	if e.Kind == "text" {
		return e.Content
	}
	return e.Question
}
func document(e entry, packed bool) (fields, error) {
	d := fields{"timestamp": e.Timestamp.UTC().Format(time.RFC3339Nano)}
	if e.Kind == "text" {
		d["content"] = e.Content
		d["is_text_memory"] = true
		return d, nil
	}
	d["question"] = e.Question
	d["tool_name"] = e.ToolName
	d["success"] = e.Success
	for k, m := range map[string]map[string]any{"args": e.Args, "metadata": e.Metadata} {
		if m == nil {
			m = fields{}
		}
		if packed {
			b, err := json.Marshal(m)
			if err != nil {
				return nil, err
			}
			d[k+"_json"] = string(b)
		} else {
			d[k] = m
		}
	}
	return d, nil
}
func stringField(d fields, k string) string { s, _ := d[k].(string); return s }
func readDocument(id string, d fields) (entry, error) {
	e := entry{ID: id, Kind: "tool", Question: stringField(d, "question"), ToolName: stringField(d, "tool_name"), Content: stringField(d, "content"), Success: true}
	if e.ID == "" {
		for _, k := range []string{"memory_id", "_id", "id"} {
			if e.ID = stringField(d, k); e.ID != "" {
				break
			}
		}
	}
	if e.ID == "" {
		return e, errors.New("memory record missing id")
	}
	if b, ok := d["success"].(bool); ok {
		e.Success = b
	}
	if s := stringField(d, "timestamp"); s != "" {
		var err error
		e.Timestamp, err = time.Parse(time.RFC3339Nano, s)
		if err != nil {
			e.Timestamp, err = time.Parse("2006-01-02T15:04:05", s)
		}
		if err != nil {
			return e, fmt.Errorf("memory %s timestamp: %w", e.ID, err)
		}
	}
	for k, target := range map[string]*map[string]any{"args": &e.Args, "metadata": &e.Metadata} {
		value := d[k]
		if value == nil {
			value = d[k+"_json"]
		}
		switch v := value.(type) {
		case map[string]any:
			*target = v
		case string:
			if v != "" {
				decoder := json.NewDecoder(strings.NewReader(v))
				decoder.UseNumber()
				if err := decoder.Decode(target); err != nil {
					return e, err
				}
			}
		}
	}
	if _, has := d["content"].(string); has || d["is_text_memory"] == true || (e.ToolName == "" && e.Metadata["is_text_memory"] == true) {
		e.Kind = "text"
		if !has {
			e.Content = e.Question
		}
		e.Question = ""
	}
	return e, nil
}
func numeric(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	case float64:
		return v, true
	default:
		return 0, false
	}
}
func collect(out *[]match, id string, d fields, score float64) error {
	e, err := readDocument(id, d)
	if err != nil {
		return err
	}
	if !math.IsNaN(score) {
		*out = append(*out, match{Memory: e, Score: score})
	}
	return nil
}
func visit(id string, d fields, fn func(entry) error) error {
	e, err := readDocument(id, d)
	if err != nil {
		return err
	}
	return fn(e)
}
func first(d fields, id string) (*entry, error) {
	if d == nil {
		return nil, nil
	}
	e, err := readDocument(id, d)
	return &e, err
}
