package drivers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/integrations/database"
	_ "doolla.ai/sdk/integrations/database/drivers"
)

// Set each DOOLLA_TEST_DSN_<BACKEND> and compile its tag to verify a live
// server using exactly the opener the CLI uses. Each probe is read-only.
func TestLiveBackends(t *testing.T) {
	for _, backend := range []string{"bigquery", "clickhouse", "duckdb", "hive", "mssql", "mysql", "oracle", "postgres", "presto", "snowflake"} {
		t.Run(backend, func(t *testing.T) {
			dsn, ok := os.LookupEnv("DOOLLA_TEST_DSN_" + strings.ToUpper(backend))
			if !ok {
				t.Skip("test DSN unset")
			}
			db, err := database.Open(backend, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			query := "SELECT 42 AS answer"
			if backend == "oracle" {
				query += " FROM DUAL; -- terminal delimiter"
			}
			args, err := json.Marshal(map[string]string{"sql": query})
			if err != nil {
				t.Fatal(err)
			}
			result, err := (doolla.SQLTool{DB: db, DisableArtifacts: true}).Run(ctx, doolla.User{ID: "live-test"}, args)
			if err != nil {
				t.Fatal(err)
			}
			if result.Component == nil || result.Component.Type != "dataframe" {
				t.Fatal("SQL tool did not return a dataframe")
			}
			rows, ok := result.Metadata["results"].([]map[string]any)
			if !ok || len(rows) != 1 || len(rows[0]) != 1 {
				t.Fatalf("unexpected rows: %v", rows)
			}
			for _, answer := range rows[0] {
				if fmt.Sprint(answer) != "42" {
					t.Fatalf("got %v, want 42", answer)
				}
			}
		})
	}
}
