package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestMixedPackedTextMemories(t *testing.T) {
	for _, tc := range []struct{ provider, writePath, writeReply, listPath, listReply string }{
		{"milvus", "/v2/vectordb/entities/upsert", `{"code":0}`, "/v2/vectordb/entities/query",
			`{"code":0,"data":[
				{"id":"legacy","question":"legacy fact","tool_name":"","args_json":"","metadata_json":"{\"is_text_memory\": true}"},
				{"id":"unmarked","question":"old note","tool_name":"","args_json":"","metadata_json":""},
				{"id":"new","question":"new fact","tool_name":"","args_json":"{}","metadata_json":"{\"is_text_memory\":true}"},
				{"id":"tool","question":"count","tool_name":"run_sql","args_json":"{\"sql\":\"SELECT 1\"}"}
			]}`},
		{"weaviate", "/v1/batch/objects", `[{"result":{"status":"SUCCESS"}}]`, "/v1/objects",
			`{"objects":[
				{"id":"legacy","properties":{"question":"legacy fact","tool_name":"","args_json":"","metadata_json":"{\"is_text_memory\": true}"}},
				{"id":"unmarked","properties":{"question":"old note","tool_name":"","args_json":"","metadata_json":""}},
				{"id":"new","properties":{"question":"new fact","tool_name":"","args_json":"{}","metadata_json":"{\"is_text_memory\":true}"}},
				{"id":"tool","properties":{"question":"count","tool_name":"run_sql","args_json":"{\"sql\":\"SELECT 1\"}"}}
			]}`},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			var wroteText bool
			client := &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case tc.writePath:
					var body json.RawMessage
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(body), `"args_json":"{}"`) {
						t.Errorf("new text memory must store an empty JSON object: %s", body)
					}
					wroteText = true
					return response(200, tc.writeReply), nil
				case tc.listPath:
					return response(200, tc.listReply), nil
				default:
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
					return nil, nil
				}
			})}
			ctx := t.Context()
			store, err := Open(ctx, Config{Provider: tc.provider, Endpoint: "https://test.invalid", HTTPClient: client, Dimension: 2})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save(ctx, entry{ID: "new", Kind: "text", Content: "new fact"}); err != nil {
				t.Fatal(err)
			}
			if !wroteText {
				t.Fatal("text memory was not written")
			}
			texts, err := store.Recent(ctx, "text", 10)
			if err != nil || len(texts) != 3 || texts[0].Content != "legacy fact" || texts[1].Content != "new fact" || texts[2].Content != "old note" {
				t.Fatalf("recent text memories: %+v, error: %v", texts, err)
			}
			tools, err := store.Recent(ctx, "tool", 10)
			if err != nil || len(tools) != 1 || tools[0].Args["sql"] != "SELECT 1" {
				t.Fatalf("recent tool memories: %+v, error: %v", tools, err)
			}
		})
	}
}

// The fixtures include both storage schemas used by the Python adapters:
// nested JSON objects and flat metadata with string-encoded JSON columns.
func TestRemoteMutationAndEnumeration(t *testing.T) {
	type step struct {
		method, path, want, reply string
		status                    int
	}
	const packed = `"args_json":"{\"nested\":[1,2]}"`
	const metaPacked = `"metadata_json":"{\"source\":\"doolla\"}"`
	const date = "2025-01-01T01:02:03.123456"
	const plain = `{"memory_id":"old","question":"q","tool_name":"sql","success":true,"args":{"nested":[1,2]},"metadata":{"source":"doolla"},"timestamp":"` + date + `"}`
	const flat = `{"memory_id":"old","question":"q","tool_name":"sql","success":true,` + packed + `,` + metaPacked + `,"timestamp":"` + date + `"}`
	const marqoDoc = `{"_id":"old","question":"q","tool_name":"sql","success":true,"args":"{\"nested\":[1,2]}","metadata":"{\"source\":\"doolla\"}","timestamp":"` + date + `"}`
	cases := map[string][]step{
		"pinecone": {
			{"POST", "/vectors/upsert", packed, `{"upsertedCount":1}`, 200},
			{"GET", "/vectors/list", "", `{"vectors":[{"id":"old"}],"pagination":{"next":"page2"}}`, 200},
			{"GET", "/vectors/fetch", "", `{"vectors":{"old":{"metadata":` + flat + `}}}`, 200},
			{"GET", "/vectors/list", "", `{"vectors":[]}`, 200},
			{"GET", "/vectors/fetch", "", `{"vectors":{"old":{"metadata":` + flat + `}}}`, 200},
			{"POST", "/vectors/delete", `"ids":["old"]`, "{}", 200},
		},
		"opensearch": {
			{"PUT", "/idx/_doc/old", `"args":{"nested":[1,2]}`, "{}", 200},
			{"POST", "/idx/_search", `"_doc"`, `{"_scroll_id":"cursor","hits":{"hits":[{"_id":"old","_source":` + plain + `}]}}`, 200},
			{"POST", "/_search/scroll", `"scroll_id":"cursor"`, `{"_scroll_id":"cursor","hits":{"hits":[]}}`, 200},
			{"DELETE", "/_search/scroll", `"scroll_id":["cursor"]`, "{}", 200},
			{"GET", "/idx/_doc/old", "", `{"_source":` + plain + `}`, 200},
			{"DELETE", "/idx/_doc/old", "", "{}", 200},
		},
		"azuresearch": {
			{"POST", "/indexes/idx/docs/index", packed, `{"value":[{"status":true}]}`, 200},
			{"POST", "/indexes/idx/docs/search", `"orderby":"timestamp desc"`, `{"value":[` + flat + `],"@search.nextPageParameters":{"search":"*","skip":1}}`, 200},
			{"POST", "/indexes/idx/docs/search", `"skip":1`, `{"value":[]}`, 200},
			{"GET", "/indexes/idx/docs/old", "", flat, 200},
			{"POST", "/indexes/idx/docs/index", `"@search.action":"delete"`, `{"value":[{"status":true}]}`, 200},
		},
		"marqo": {
			{"POST", "/indexes/idx/documents", `"tensorFields":["question"]`, `{"errors":false}`, 200},
			{"GET", "/indexes/idx/stats", "", `{"numberOfDocuments":1}`, 200},
			{"POST", "/indexes/idx/search", `"approximate":false`, `{"hits":[` + marqoDoc + `]}`, 200},
			{"GET", "/indexes/idx/documents/old", "", marqoDoc, 200},
			{"POST", "/indexes/idx/documents/delete-batch", `["old"]`, "{}", 200},
		},
		"weaviate": {
			{"POST", "/v1/batch/objects", packed, `[{"result":{"status":"SUCCESS"}}]`, 200},
			{"GET", "/v1/objects", "", `{"objects":[{"id":"old","properties":` + flat + `}]}`, 200},
			{"GET", "/v1/objects/Idx/old", "", `{"id":"old","properties":` + flat + `}`, 200},
			{"DELETE", "/v1/objects/Idx/old", "", "", 204},
		},
		"qdrant": {
			{"PUT", "/collections/idx/points", `"args":{"nested":[1,2]}`, "{}", 200},
			{"POST", "/collections/idx/points/scroll", `"with_payload":true`, `{"result":{"points":[{"id":"old","payload":` + plain + `}],"next_page_offset":"next"}}`, 200},
			{"POST", "/collections/idx/points/scroll", `"offset":"next"`, `{"result":{"points":[]}}`, 200},
			{"GET", "/collections/idx/points/old", "", `{"result":{"id":"old","payload":` + plain + `}}`, 200},
			{"POST", "/collections/idx/points/delete", `"points":["old"]`, "{}", 200},
		},
	}
	for provider, steps := range cases {
		t.Run(provider, func(t *testing.T) {
			i := 0
			cfg := Config{Provider: provider, Endpoint: "https://data.invalid", APIKey: "key", Collection: "idx", Dimension: 3}
			if provider == "weaviate" {
				cfg.Collection = "Idx"
			}
			cfg.HTTPClient = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
				if i >= len(steps) {
					t.Fatalf("unexpected call %s %s", req.Method, req.URL)
				}
				step := steps[i]
				i++
				if req.Method != step.method || req.URL.Path != step.path {
					t.Fatalf("step %d: got %s %s", i, req.Method, req.URL)
				}
				if provider == "pinecone" && i == 4 && req.URL.Query().Get("paginationToken") != "page2" {
					t.Fatal("missing page token")
				}
				if step.want != "" {
					var body json.RawMessage
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(body), step.want) {
						t.Fatalf("missing %s in %s", step.want, body)
					}
					if i == 1 {
						switch provider {
						case "marqo":
							if !strings.Contains(string(body), `"args":"{\"nested\":[1,2]}"`) || !strings.Contains(string(body), `"metadata":"{\"source\":\"doolla\"}"`) {
								t.Fatal(string(body))
							}
						case "qdrant", "opensearch":
							if !strings.Contains(string(body), `"metadata":{"source":"doolla"}`) {
								t.Fatal(string(body))
							}
						default:
							if !strings.Contains(string(body), metaPacked) {
								t.Fatal(string(body))
							}
						}
					}
				}
				return response(step.status, step.reply), nil
			})}
			s, err := Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			err = s.Save(t.Context(), entry{ID: "old", Kind: "tool", Question: "q", ToolName: "sql", Args: fields{"nested": []int{1, 2}}, Metadata: fields{"source": "doolla"}, Success: true})
			if err != nil {
				t.Fatal(err)
			}
			hits, err := s.Recent(t.Context(), "tool", 2)
			if err != nil || len(hits) != 1 || hits[0].Args["nested"] == nil || hits[0].Metadata["source"] != "doolla" || hits[0].ID != "old" || hits[0].Timestamp.IsZero() {
				t.Fatalf("recent %+v %v", hits, err)
			}
			ok, err := s.Delete(t.Context(), "tool", "old")
			if err != nil || !ok || i != len(steps) {
				t.Fatalf("delete %v %v, calls %d", ok, err, i)
			}
		})
	}
}

func TestProvisionProtocol(t *testing.T) {
	cases := []struct{ provider, lookup, create, method, want string }{
		{"qdrant", "/collections/idx", "/collections/idx", "PUT", `"distance":"Cosine"`}, {"azuresearch", "/indexes/idx", "/indexes", "POST", `"name":"content"`}, {"opensearch", "/idx", "/idx", "PUT", `"type":"knn_vector"`}, {"weaviate", "/v1/schema/Idx", "/v1/schema", "POST", `"vectorizer":"none"`}, {"marqo", "/indexes/idx/settings", "/indexes/idx", "POST", `"type":"unstructured"`}, {"chroma", "/api/v2/tenants/example/databases/db/collections/idx", "/api/v2/tenants/example/databases/db/collections", "POST", `"get_or_create":true`}, {"pinecone", "/indexes/idx", "/indexes", "POST", `"region":"us-east-1"`},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			created := false
			cfg := Config{Provider: tc.provider, Endpoint: "https://data.invalid", ControlEndpoint: "https://admin.invalid", Collection: "idx", Dimension: 8, Tenant: "example", Database: "db", Provision: true, Embed: func(context.Context, string) ([]float32, error) { return nil, nil }}
			if tc.provider == "weaviate" {
				cfg.Collection = "Idx"
			}
			if tc.provider == "marqo" {
				cfg.MarqoModel = "hf/bge-small-zh-v1.5"
			}
			cfg.HTTPClient = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if tc.provider == "pinecone" && r.URL.Host != "admin.invalid" {
					t.Fatal("incorrect control endpoint")
				}
				if r.Method == "GET" && r.URL.Path == tc.lookup {
					if !created {
						return response(404, "missing"), nil
					}
					return response(200, `{"id":"uuid","status":{"ready":true},"host":"data.invalid"}`), nil
				}
				if r.Method != tc.method || r.URL.Path != tc.create || created {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL)
				}
				created = true
				var body json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !strings.Contains(string(body), tc.want) {
					t.Fatalf("schema %s %v", body, err)
				}
				if tc.provider == "marqo" && !strings.Contains(string(body), `"model":"hf/bge-small-zh-v1.5"`) {
					t.Fatalf("missing Marqo embedding model in %s", body)
				}
				return response(200, `{"id":"uuid"}`), nil
			})}
			if _, err := Open(t.Context(), cfg); err != nil || !created {
				t.Fatal(created, err)
			}
			if _, err := Open(t.Context(), cfg); err != nil {
				t.Fatal("reopen", err)
			}
		})
	}
}

func TestAzureLegacySchema(t *testing.T) {
	calls := 0
	s, err := Open(t.Context(), Config{Provider: "azuresearch", Endpoint: "https://test.invalid", Dimension: 2, HTTPClient: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return response(400, `{"error":{"message":"The property 'content' does not exist on type 'search.documentFields'."}}`), nil
		case 2:
			var batch struct{ Value []fields }
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
				t.Fatal(err)
			}
			doc := batch.Value[0]
			if doc["content"] != nil || doc["question"] != "note" || doc["success"] != nil || doc["metadata_json"] != `{"is_text_memory":true}` {
				t.Fatal(doc)
			}
			return response(200, `{"value":[{"status":true}]}`), nil
		default:
			return response(200, `{"value":[{"memory_id":"old","content":null,"question":"note","metadata_json":"{\"is_text_memory\":true}","@search.score":0.95}]}`), nil
		}
	})}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), entry{ID: "old", Kind: "text", Content: "note"}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Search(t.Context(), query{Kind: "text", Text: "note", Limit: 1, Threshold: .7})
	if err != nil || len(m) != 1 || m[0].Memory.Content != "note" {
		t.Fatal(m, err)
	}
}
func TestWeaviateCursor(t *testing.T) {
	var calls int
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("class") != "ToolMemory" {
			t.Fatal("missing Weaviate class")
		}
		calls++
		if calls == 1 {
			docs := make([]fields, 100)
			for i := range docs {
				docs[i] = fields{"id": strconv.Itoa(i), "properties": fields{"question": "test", "tool_name": "sql"}}
			}
			b, _ := json.Marshal(fields{"objects": docs})
			return response(200, string(b)), nil
		}
		if calls != 2 || r.URL.Query().Get("after") != "99" {
			t.Fatalf("bad cursor %s", r.URL)
		}
		return response(200, `{"objects":[]}`), nil
	})}
	s, err := Open(t.Context(), Config{Provider: "weaviate", Endpoint: "https://test.invalid", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.Recent(t.Context(), "tool", 200)
	if err != nil || len(rows) != 100 {
		t.Fatal(len(rows), err)
	}
}

func TestWeaviateSearchContinuesPastOtherKinds(t *testing.T) {
	const target = "target"
	pages := 0
	client := &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		var body struct{ Query string }
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var docs []fields
		switch pages {
		case 0:
			if !strings.Contains(body.Query, "offset:0") {
				t.Fatal(body.Query)
			}
			for i := 0; i < 32; i++ {
				docs = append(docs, fields{"tool_name": "", "question": "note", "_additional": fields{"id": strconv.Itoa(i), "distance": 0}})
			}
		case 1:
			if !strings.Contains(body.Query, "offset:32") {
				t.Fatal(body.Query)
			}
			docs = []fields{{"question": "count", "tool_name": "run_sql", "success": true, "metadata_json": `{"is_text_memory":true}`, "_additional": fields{"id": target, "distance": 0.1}}}
		default:
			t.Fatal("unexpected page")
		}
		pages++
		b, err := json.Marshal(fields{"data": fields{"Get": fields{"ToolMemory": docs}}})
		if err != nil {
			t.Fatal(err)
		}
		return response(200, string(b)), nil
	})}
	s, err := Open(t.Context(), Config{Provider: "weaviate", Endpoint: "http://test.invalid", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search(t.Context(), query{Kind: "tool", Text: "count", Limit: 1, Threshold: 0.8})
	if err != nil || len(hits) != 1 || hits[0].Memory.ID != target || hits[0].Rank != 1 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
}
