package database_test

import (
	"strings"
	"testing"

	"doolla.ai/sdk/integrations/database"
)

func TestOpenErrors(t *testing.T) {
	if _, err := database.Open("unknown", ""); err == nil || !strings.Contains(err.Error(), "unsupported database") {
		t.Fatalf("unknown backend error = %v", err)
	}
	// This package links no drivers; registration belongs to its caller.
	for _, tc := range []struct{ backend, tag string }{
		{" PostgreSQL ", "postgres"}, {"SQLSERVER", "mssql"}, {"Hive", "hive"},
	} {
		if _, err := database.Open(tc.backend, ""); err == nil || !strings.Contains(err.Error(), "doolla_"+tc.tag) {
			t.Errorf("Open(%q) error = %v", tc.backend, err)
		}
	}
}
