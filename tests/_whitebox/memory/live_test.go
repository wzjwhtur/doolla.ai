package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	doolla "doolla.ai/sdk"
)

func TestLocalMemoryLifecycle(t *testing.T) {
	checkLifecycle(t, doolla.NewLocalMemory(20))
}

// Each endpoint opts into its own isolated acceptance run. Collections are
// created solely for the run. Pinecone uses an isolated namespace in the
// supplied existing index so acceptance never provisions a billable index.
func TestLiveMemoryBackends(t *testing.T) {
	for _, provider := range []string{"qdrant", "weaviate", "opensearch", "marqo", "azuresearch", "pinecone"} {
		t.Run(provider, func(t *testing.T) {
			prefix := "DOOLLA_TEST_" + strings.ToUpper(provider)
			endpoint := os.Getenv(prefix + "_URL")
			if endpoint == "" {
				t.Skipf("%s_URL unset", prefix)
			}
			var id [12]byte
			if _, err := rand.Read(id[:]); err != nil {
				t.Fatal(err)
			}
			name := "doolla" + hex.EncodeToString(id[:])
			cfg := Config{
				Provider: provider, Endpoint: endpoint, Collection: name,
				APIKey: os.Getenv(prefix + "_KEY"), Provision: true, Dimension: 12,
				HTTPClient: &http.Client{Timeout: 90 * time.Second},
			}
			var removePath string
			switch provider {
			case "weaviate":
				cfg.Collection = "Doolla" + hex.EncodeToString(id[:])
				removePath = "/v1/schema/" + cfg.Collection
			case "qdrant":
				removePath = "/collections/" + name
			case "opensearch":
				removePath = "/" + name
			case "marqo":
				cfg.MarqoModel = os.Getenv(prefix + "_MODEL")
				removePath = "/indexes/" + name
			case "azuresearch":
				removePath = "/indexes/" + name + azureVersion
			case "pinecone":
				cfg.Provision = false
				cfg.Namespace = name
				dimension, err := strconv.Atoi(os.Getenv(prefix + "_DIMENSION"))
				if err != nil || dimension < 1 {
					t.Fatalf("%s_DIMENSION must match the existing index", prefix)
				}
				cfg.Dimension = dimension
			}
			s, err := Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				client := remote{Config: cfg}
				if provider == "pinecone" {
					err = client.call(ctx, "POST", "/vectors/delete", fields{"namespace": name, "deleteAll": true}, nil)
				} else {
					err = client.call(ctx, "DELETE", removePath, nil, nil)
				}
				if err != nil {
					t.Errorf("cleanup %s: %v", name, err)
				}
			})
			t.Logf("collection=%s", name)
			checkLifecycle(t, s)
		})
	}
}

// Premium exposes a tool-memory API scoped by organization. Limit all mutations
// (including cleanup) to a new unique tool and record in that organization.
func TestLivePremium(t *testing.T) {
	endpoint := os.Getenv("DOOLLA_TEST_PREMIUM_URL")
	if endpoint == "" {
		t.Skip("DOOLLA_TEST_PREMIUM_URL unset")
	}
	key, org := os.Getenv("DOOLLA_TEST_PREMIUM_KEY"), os.Getenv("DOOLLA_TEST_PREMIUM_ORG")
	if key == "" || org == "" {
		t.Fatal("set DOOLLA_TEST_PREMIUM_KEY and DOOLLA_TEST_PREMIUM_ORG for the test organization")
	}
	s, err := Open(t.Context(), Config{Provider: "premium", Endpoint: endpoint, APIKey: key, Organization: org})
	if err != nil {
		t.Fatal(err)
	}
	var randomID [16]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%x-%x-%x-%x-%x", randomID[:4], randomID[4:6], randomID[6:8], randomID[8:10], randomID[10:])
	tool := "doolla_live_" + hex.EncodeToString(randomID[:])
	ctx := t.Context()
	e := entry{ID: id, Kind: "tool", Question: "Count the orders in acceptance run " + id, ToolName: tool, Args: fields{"sql": "SELECT 42"}, Success: true, Timestamp: time.Now().UTC()}
	if err := s.Save(ctx, e); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := s.Clear(cleanupCtx, tool, time.Time{}); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	hits, err := s.Search(ctx, query{Kind: "tool", Text: e.Question, ToolName: tool, Limit: 5})
	if err != nil || len(hits) == 0 || hits[0].Memory.ID != id {
		t.Fatalf("search did not return saved test record: matches=%d err=%v", len(hits), err)
	}
	recent, err := s.Recent(ctx, "tool", 20)
	found := false
	for _, m := range recent {
		found = found || m.ID == id
	}
	if err != nil || !found {
		t.Fatalf("saved test record missing from recent memories: %v", err)
	}
	if ok, err := s.Delete(ctx, "tool", id); err != nil || !ok {
		t.Fatalf("delete saved record: %v %v", ok, err)
	}
}

// Legacy Python classes use word tokenization; Go provisions field tokenization.
// Both store the record type in tool_name, independently of user metadata.
func TestLiveWeaviatePackedKinds(t *testing.T) {
	endpoint := os.Getenv("DOOLLA_TEST_WEAVIATE_URL")
	if endpoint == "" {
		t.Skip("set DOOLLA_TEST_WEAVIATE_URL to test both schemas")
	}
	for _, tokenizer := range []string{"word", "field"} {
		t.Run(tokenizer, func(t *testing.T) {
			ctx := t.Context()
			cfg := Config{Provider: "weaviate", Endpoint: endpoint, APIKey: os.Getenv("DOOLLA_TEST_WEAVIATE_KEY"), Collection: fmt.Sprintf("Packed%d", time.Now().UnixNano()), Dimension: 2, HTTPClient: &http.Client{Timeout: 30 * time.Second}, Provision: tokenizer == "field"}
			cfg.Embed = func(_ context.Context, _ string) ([]float32, error) { return []float32{1, 0}, nil }
			r := remote{Config: cfg}
			if tokenizer == "word" {
				props := []fields{{"name": "success", "dataType": []string{"boolean"}}}
				for _, name := range []string{"question", "tool_name", "args_json", "metadata_json", "timestamp"} {
					props = append(props, fields{"name": name, "dataType": []string{"text"}, "tokenization": tokenizer})
				}
				if err := r.call(ctx, "POST", "/v1/schema", fields{"class": cfg.Collection, "vectorizer": "none", "properties": props}, nil); err != nil {
					t.Fatal(err)
				}
			}
			s, err := Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := r.call(context.Background(), "DELETE", "/v1/schema/"+cfg.Collection, nil, nil); err != nil {
					t.Error(err)
				}
			})
			if err := s.Save(ctx, entry{Kind: "tool", ToolName: "run_sql", Question: "tool", Success: true, Metadata: fields{"is_text_memory": true}}); err != nil {
				t.Fatal(err)
			}
			for _, meta := range []string{`{"source":"import","is_text_memory":true}`, ""} {
				if err := r.call(ctx, "POST", "/v1/objects", fields{"class": cfg.Collection, "properties": fields{"tool_name": "", "question": "imported", "args_json": "", "metadata_json": meta, "success": true}, "vector": []float32{0.9, 0.1}}, nil); err != nil {
					t.Fatal(err)
				}
			}
			for _, tc := range []struct {
				kind string
				n    int
			}{{"tool", 1}, {"text", 2}} {
				hits, err := s.Search(ctx, query{Kind: tc.kind, Text: "probe", Limit: tc.n})
				if err != nil || len(hits) != tc.n {
					t.Errorf("search %s: %+v %v", tc.kind, hits, err)
				}
				recent, err := s.Recent(ctx, tc.kind, 10)
				if err != nil || len(recent) != tc.n {
					t.Errorf("recent %s: %+v %v", tc.kind, recent, err)
				}
			}
		})
	}
}
