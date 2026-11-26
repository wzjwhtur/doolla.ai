//go:build doolla_snowflake

package drivers_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/integrations/database"
	_ "doolla.ai/sdk/integrations/database/drivers"
)

// This exercises the Snowflake Go driver's login, query, decoding and session
// close on loopback. The fixture accepts only its own throwaway credentials.
func TestSnowflakeSQLToolLoopbackProtocol(t *testing.T) {
	t.Setenv("SNOWFLAKE_DISABLE_PLATFORM_DETECTION", "true")
	const query = "SELECT 40 + 2 AS ANSWER"
	var mu sync.Mutex
	var events []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		events = append(events, r.URL.Path)
		mu.Unlock()
		if r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			stream, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Errorf("gzip: %v", err)
				http.Error(w, "invalid gzip", 400)
				return
			}
			defer stream.Close()
			body = stream
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/session/v1/login-request":
			var login struct {
				Data struct {
					Name     string `json:"LOGIN_NAME"`
					Password string `json:"PASSWORD"`
				}
			}
			if err := json.NewDecoder(body).Decode(&login); err != nil || login.Data.Name != "fixture" || login.Data.Password != "fixture" {
				t.Error("unexpected login body")
				http.Error(w, "bad login", 400)
				return
			}
			io.WriteString(w, `{"success":true,"data":{"token":"test-session","masterToken":"test-master","sessionId":1,"validityInSeconds":3600,"masterValidityInSeconds":3600,"sessionInfo":{"databaseName":"DATA","schemaName":"PUBLIC","warehouseName":"TEST","roleName":"TEST"}}}`)
		case "/queries/v1/query-request":
			var payload struct {
				SQLText string `json:"sqlText"`
			}
			if err := json.NewDecoder(body).Decode(&payload); err != nil || payload.SQLText != query || r.Header.Get("Authorization") != `Snowflake Token="test-session"` {
				t.Errorf("unexpected query %q (%v)", payload.SQLText, err)
				http.Error(w, "bad query", 400)
				return
			}
			io.WriteString(w, `{"success":true,"data":{"queryId":"00000000-0000-0000-0000-000000000001","queryResultFormat":"json","statementTypeId":4096,"total":1,"returned":1,"rowtype":[{"name":"ANSWER","type":"fixed","precision":38,"scale":0,"nullable":false}],"rowset":[["42"]]}}`)
		case "/session":
			io.WriteString(w, `{"success":true}`)
		default:
			t.Errorf("unexpected Snowflake request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	db, err := database.Open("snowflake", "fixture:fixture@"+strings.TrimPrefix(server.URL, "http://")+"/DATA/PUBLIC?account=test&protocol=http&disableTelemetry=true&loginTimeout=5&requestTimeout=5&maxRetryCount=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result, err := (doolla.SQLTool{DB: db, DisableArtifacts: true}).Run(ctx, doolla.User{ID: "tester"}, json.RawMessage(`{"sql":"SELECT 40 + 2 AS ANSWER"}`))
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := result.Component.Data["data"].([]map[string]any)
	if result.Component.Type != "dataframe" || !ok || len(rows) != 1 || rows[0]["ANSWER"] != "42" || result.Metadata["row_count"] != 1 {
		t.Fatalf("unexpected dataframe %+v", result)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(events, ","); got != "/session/v1/login-request,/queries/v1/query-request,/session" {
		t.Fatalf("unexpected sequence %s", got)
	}
}
