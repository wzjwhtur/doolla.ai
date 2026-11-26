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
