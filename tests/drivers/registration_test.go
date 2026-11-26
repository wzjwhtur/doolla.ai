//go:build doolla_bigquery && doolla_clickhouse && doolla_duckdb && doolla_hive && doolla_mssql && doolla_mysql && doolla_oracle && doolla_postgres && doolla_presto && doolla_snowflake

package drivers_test

import (
	"database/sql"
	"slices"
	"testing"

	_ "doolla.ai/sdk/integrations/database/drivers"
)

func TestAllDriversRegistered(t *testing.T) {
	registered := sql.Drivers()
	for _, name := range []string{"bigquery", "clickhouse", "duckdb", "odbc", "hive", "sqlserver", "mysql", "oracle", "pgx", "presto", "snowflake"} {
		if !slices.Contains(registered, name) {
			t.Errorf("driver %q missing from %v", name, registered)
		}
	}
}
