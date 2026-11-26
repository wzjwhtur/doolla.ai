package memory

import (
	"context"
	doolla "doolla.ai/sdk"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func checkLifecycle(t *testing.T, s doolla.MemoryStore) {
	t.Helper()
	ctx := t.Context()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	one := entry{ID: "b42330d1-a338-4428-a5ef-6cc627753bb1", Kind: "tool", Question: "sum revenue", ToolName: "run_sql", Args: fields{"sql": "SELECT 42"}, Success: true, Timestamp: stamp}
	two := entry{ID: "b42330d1-a338-4428-a5ef-6cc627753bb2", Kind: "text", Content: "Revenue is net of tax", Timestamp: stamp.Add(time.Minute)}
	for _, e := range []entry{one, two, two} {
		if err := s.Save(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []entry{one, two} {
		hits, err := s.Search(ctx, query{Kind: e.Kind, Text: textOf(e), ToolName: e.ToolName, Threshold: 0.69, Limit: 2})
		if err != nil || len(hits) != 1 || hits[0].Memory.ID != e.ID || hits[0].Rank != 1 {
			t.Fatalf("search %s: %+v %v", e.Kind, hits, err)
		}
		recent, err := s.Recent(ctx, e.Kind, 10)
		if err != nil || len(recent) != 1 || recent[0].ID != e.ID || textOf(recent[0]) != textOf(e) {
			t.Fatalf("recent %s: %+v %v", e.Kind, recent, err)
		}
	}
	if ok, err := s.Delete(ctx, "tool", two.ID); err != nil || ok {
		t.Fatalf("wrong-kind delete: %v, %v", ok, err)
	}
	if n, err := s.Clear(ctx, "run_sql", stamp.Add(time.Second)); err != nil || n != 1 {
		t.Fatalf("clear: %d, %v", n, err)
	}
	if ok, err := s.Delete(ctx, "text", two.ID); err != nil || !ok {
		t.Fatalf("delete: %v, %v", ok, err)
	}
	if ok, err := s.Delete(ctx, "text", two.ID); err != nil || ok {
		t.Fatalf("delete missing: %v, %v", ok, err)
	}
}
func TestFlatLifecycle(t *testing.T) {
	for _, metric := range []string{"cosine", "l2"} {
		t.Run(metric, func(t *testing.T) {
			s, err := Open(t.Context(), Config{Provider: "faiss", Directory: t.TempDir(), Metric: metric, Dimension: 12})
			if err != nil {
				t.Fatal(err)
			}
			checkLifecycle(t, s)
		})
	}
}
func TestFlatParallelWriters(t *testing.T) {
	dir := t.TempDir()
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			s, err := Open(t.Context(), Config{Provider: "faiss", Directory: dir})
			if err == nil {
				err = s.Save(t.Context(), entry{Kind: "text", Content: "hello"})
			}
			if err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	s, _ := Open(t.Context(), Config{Provider: "faiss", Directory: dir})
	records, err := s.Recent(t.Context(), "text", 20)
	if err != nil || len(records) != 8 {
		t.Fatalf("got %d, %v", len(records), err)
	}
}
func TestFAISSPythonInterop(t *testing.T) {
	if os.Getenv("DOOLLA_TEST_FAISS") == "" {
		t.Skip("set DOOLLA_TEST_FAISS to validate against native Faiss and Python pickle")
	}
	dir := t.TempDir()
	run := func(script string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "python3", "-B", "-c", script, dir)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", output, err)
		}
	}
	run(`
import faiss,numpy as np,pickle,os,sys
d=sys.argv[1]; idx=faiss.IndexFlatIP(4)
idx.add(np.array([[1,0,0,0],[0,1,0,0]], dtype=np.float32))
faiss.write_index(idx,os.path.join(d,'index.faiss'))
doc={"memory_id":"legacy-id","question":"legacy question","tool_name":"sql","args":{"nested":[42,"你好",None]},"metadata":{"boolean":True},"success":True,"timestamp":"2024-08-07T12:30:12.123"}
with open(os.path.join(d,'metadata.pkl'),'wb') as f:pickle.dump({1:doc},f)
`)
	s, err := Open(t.Context(), Config{Provider: "faiss", Directory: dir, Dimension: 4})
	if err != nil {
		t.Fatal(err)
	}
	recent, err := s.Recent(t.Context(), "tool", 1)
	if err != nil || len(recent) != 1 || recent[0].ID != "legacy-id" {
		t.Fatal(recent, err)
	}
	if err = s.Save(t.Context(), entry{Kind: "text", ID: "new", Content: "Go 新条目"}); err != nil {
		t.Fatal(err)
	}
	run(`
import faiss,os,pickle,sys
d=sys.argv[1];idx=faiss.read_index(os.path.join(d,'index.faiss'))
with open(os.path.join(d,'metadata.pkl'),'rb') as f:rows=pickle.load(f)
assert idx.ntotal==3 and idx.d==4 and rows[1]['args']['nested']==[42,'你好',None] and rows[2]['content']=='Go 新条目',rows
`)
}

type roundTrip func(*http.Request) (*http.Response, error)

func (fn roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func response(status int, doc string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(doc)), Header: make(http.Header)}
}
func TestWireSearch(t *testing.T) {
	cases := []struct{ provider, path, field, body string }{
		{"qdrant", "/collections/tool_memories/points/query", "query", `{"result":{"points":[{"id":"old","score":0.9,"payload":{"content":"hello","is_text_memory":true}}]}}`},
		{"chroma", "/api/v2/tenants/default_tenant/databases/default_database/collections/uuid/query", "query_embeddings", `{"ids":[["old"]],"distances":[[0.1]],"metadatas":[[{"content":"hello","is_text_memory":true}]]}`},
		{"pinecone", "/query", "vector", `{"matches":[{"id":"old","score":0.9,"metadata":{"content":"hello","is_text_memory":true}}]}`},
		{"milvus", "/v2/vectordb/entities/search", "annsField", `{"code":0,"data":[{"id":"old","distance":0.9,"question":"hello","tool_name":"","metadata_json":"{\"is_text_memory\":true}"}]}`},
		{"weaviate", "/v1/graphql", "query", `{"data":{"Get":{"ToolMemory":[{"_additional":{"id":"old","distance":0.1},"question":"hello","tool_name":"","metadata_json":"{\"is_text_memory\":true}"}]}}}`},
		{"azuresearch", "/indexes/tool-memories/docs/search", "vectorQueries", `{"value":[{"memory_id":"old","content":"hello","@search.score":0.9}]}`},
		{"opensearch", "/tool-memories/_search", "query", `{"hits":{"hits":[{"_id":"old","_score":1.9,"_source":{"content":"hello","is_text_memory":true}}]}}`},
		{"marqo", "/indexes/tool-memories/search", "q", `{"hits":[{"_id":"old","_score":0.9,"content":"hello","is_text_memory":true}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			client := &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
				if tc.provider == "chroma" && req.Method == "GET" {
					return response(200, `{"id":"uuid"}`), nil
				}
				if req.URL.Path != tc.path || req.Method != "POST" {
					t.Fatalf("unexpected %s %s", req.Method, req.URL)
				}
				var d fields
				if err := json.NewDecoder(req.Body).Decode(&d); err != nil || d[tc.field] == nil {
					t.Fatalf("unexpected body %v: %v", d, err)
				}
				return response(200, tc.body), nil
			})}
			s, err := Open(t.Context(), Config{Provider: tc.provider, Endpoint: "https://test.invalid", Dimension: 2, Embed: func(context.Context, string) ([]float32, error) { return []float32{1, 2}, nil }, HTTPClient: client})
			if err != nil {
				t.Fatal(err)
			}
			hits, err := s.Search(t.Context(), query{Kind: "text", Text: "hello", Limit: 2, Threshold: 0.8})
			if err != nil || len(hits) != 1 || hits[0].Memory.ID != "old" || hits[0].Memory.Content != "hello" {
				t.Fatalf("got %+v, %v", hits, err)
			}
		})
	}
}
func TestPremiumProtocol(t *testing.T) {
	if _, err := Open(t.Context(), Config{Provider: "premium"}); err == nil {
		t.Fatal("expected an error when the Premium endpoint is missing")
	}
	client := &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer test" || req.Header.Get("X-Organization-ID") != "org" {
			t.Fatal(req.Header)
		}
		switch req.URL.Path {
		case "/memory/tool-usage":
			var doc fields
			json.NewDecoder(req.Body).Decode(&doc)
			if doc["id"] != "old" {
				t.Fatal(doc)
			}
			return response(204, ""), nil
		case "/memory/search-similar":
			return response(200, `{"results":[{"memory":{"memory_id":"old","question":"q","tool_name":"sql","args":{"x":1},"success":true},"similarity_score":0.9,"rank":3}]}`), nil
		case "/memory/recent":
			return response(200, `{"memories":[{"memory_id":"old","question":"q","tool_name":"sql"}]}`), nil
		case "/memory/clear":
			return response(200, `{"deleted_count":1}`), nil
		case "/memory/old":
			return response(404, ""), nil
		}
		t.Fatalf("unexpected URL %s", req.URL)
		return nil, nil
	})}
	s, err := Open(t.Context(), Config{Provider: "premium", Endpoint: "https://premium.example", APIKey: "test", Organization: "org", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Save(t.Context(), entry{ID: "old", Kind: "tool", ToolName: "sql", Success: true, Question: "q"}); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Search(t.Context(), query{Kind: "tool", Text: "q", Limit: 1}); err != nil || len(m) != 1 || m[0].Rank != 3 {
		t.Fatal(m, err)
	}
	if m, err := s.Recent(t.Context(), "tool", 1); err != nil || len(m) != 1 {
		t.Fatal(m, err)
	}
	if ok, err := s.Delete(t.Context(), "tool", "old"); err != nil || ok {
		t.Fatal(ok, err)
	}
	if n, err := s.Clear(t.Context(), "sql", time.Time{}); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}
func TestLiveMilvus(t *testing.T) {
	endpoint := os.Getenv("DOOLLA_TEST_MILVUS_URL")
	if endpoint == "" {
		t.Skip("set DOOLLA_TEST_MILVUS_URL to run isolated Milvus collection lifecycle")
	}
	cfg := Config{Provider: "milvus", Endpoint: endpoint, Collection: fmt.Sprintf("doolla_test_%d", time.Now().UnixNano()), APIKey: os.Getenv("DOOLLA_TEST_MILVUS_KEY"), Dimension: 12, Provision: true}
	t.Cleanup(func() {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
		r := remote{Config: cfg}
		if _, err := r.milvusCollection(context.Background(), "drop", fields{}); err != nil {
			t.Error(err)
		}
	})
	s, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	checkLifecycle(t, s)
}
func TestLiveChroma(t *testing.T) {
	if os.Getenv("DOOLLA_TEST_CHROMA_URL") == "" {
		t.Skip("set DOOLLA_TEST_CHROMA_URL to run isolated Chroma collection lifecycle")
	}
	cfg := Config{Provider: "chroma", Endpoint: os.Getenv("DOOLLA_TEST_CHROMA_URL"), Collection: fmt.Sprintf("doolla_test_%d", time.Now().UnixNano()), Dimension: 12, Provision: true, Embed: func(ctx context.Context, s string) ([]float32, error) { return legacyVector(s, 12), nil }}
	s, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		r := remote{Config: cfg}
		r.HTTPClient = &http.Client{Timeout: 30 * time.Second}
		if err := r.call(context.Background(), "DELETE", "/api/v2/tenants/default_tenant/databases/default_database/collections/"+cfg.Collection, nil, nil); err != nil {
			t.Error(err)
		}
	})
	checkLifecycle(t, s)
}
func TestFlatMissingHalfDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metadata.pkl")
	os.WriteFile(path, []byte("existing"), 0600)
	s, _ := Open(t.Context(), Config{Provider: "faiss", Directory: dir})
	if err := s.Save(t.Context(), entry{Kind: "text", Content: "new"}); err == nil {
		t.Fatal("expected corrupt pair error")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "existing" {
		t.Fatal("metadata overwritten")
	}
}

func TestMilvusDeleteFilter(t *testing.T) {
	id := "legacy\"id"
	client := &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		var d fields
		if err := json.NewDecoder(req.Body).Decode(&d); err != nil {
			t.Fatal(err)
		}
		if req.URL.Path != "/v2/vectordb/entities/delete" || d["filter"] != `id == "legacy\"id"` || d["id"] != nil {
			t.Fatalf("invalid delete payload: %v", d)
		}
		return response(200, `{"code":0,"data":{}}`), nil
	})}
	m := milvus{&remote{Config: Config{Endpoint: "http://test.invalid", Collection: "existing", HTTPClient: client}}}
	if err := m.remove(t.Context(), id); err != nil {
		t.Fatal(err)
	}
}
func TestPythonVectorCompatibility(t *testing.T) {
	// Reference values from Python's hashlib.md5 implementation for this UTF-8 input.
	want := []float32{0.16, 0.58, 0.79, 0.39, 0.19, 0.09, 0.54, 0.27, 0.13, 0.56, 0.78, 0.39}
	got := legacyVector("revenue总额", len(want))
	for i, x := range got {
		if x != want[i] {
			t.Fatalf("component %d: got %v, want %v", i, x, want[i])
		}
	}
}

func TestMemoryRankAndIntegerPrecision(t *testing.T) {
	s := doolla.NewLocalMemory(10)
	for _, question := range []string{"find newer records", "find records"} {
		err := s.Save(t.Context(), entry{Kind: "tool", Question: question, ToolName: "lookup", Args: fields{"id": int64(9007199254740993)}, Success: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	hits, err := s.Search(t.Context(), query{Kind: "tool", Text: "find records", Limit: 2})
	if err != nil || len(hits) != 2 || hits[0].Memory.Question != "find records" || hits[0].Rank != 1 || hits[1].Rank != 2 {
		t.Fatalf("ordered hits: %+v, %v", hits, err)
	}
	recent, err := s.Recent(t.Context(), "tool", 2)
	if err != nil || len(recent) != 2 {
		t.Fatalf("recent: %+v, %v", recent, err)
	}
	for _, e := range []entry{hits[0].Memory, recent[0]} {
		raw, err := json.Marshal(e.Args)
		if err != nil || string(raw) != `{"id":9007199254740993}` {
			t.Fatalf("args: %s, %v", raw, err)
		}
	}
}
