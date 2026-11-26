//go:build doolla_duckdb

package drivers_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"doolla.ai/sdk/cli"
	"doolla.ai/sdk/integrations/database"
	_ "doolla.ai/sdk/integrations/database/drivers"
)

func TestDuckDBInitSQL(t *testing.T) {
	db, err := database.OpenDuckDB("", "CREATE TEMP VIEW answer AS SELECT 42 AS value")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(2)
	// Keep both connections checked out. Temporary views are connection-local.
	for range 2 {
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		var got int
		if err := conn.QueryRowContext(t.Context(), "SELECT value FROM answer").Scan(&got); err != nil || got != 42 {
			t.Fatalf("initialized connection returned %d, error %v", got, err)
		}
	}
}

func TestDuckDBRejectsFailedInitSQL(t *testing.T) {
	db, err := database.OpenDuckDB("", "SELECT * FROM missing_init_table")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for range 2 {
		if err := db.PingContext(t.Context()); err == nil {
			t.Fatal("pool returned a connection after initialization failed")
		}
	}
}

func TestDuckDBCLIInitSQLUsesInMemoryDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := cli.Run(ctx, []string{"-database", "duckdb", "-duckdb-init-sql", "SELECT * FROM missing_init_table", "-provider", "mock", "-listen", "127.0.0.1:0", "-workdir", t.TempDir()}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "initialize DuckDB connection") {
		t.Fatalf("expected database initialization error before serving, got %v", err)
	}
}
