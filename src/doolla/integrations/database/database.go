// Package database opens configured SQL backends for use with doolla.SQLTool.
package database

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// Open returns a connection pool. Link the selected database/sql driver in
// the application or use the tagged drivers module. The caller owns Close.
// Use PingContext or execute a query to verify server access.
func Open(backend, dsn string) (*sql.DB, error) {
	backend = strings.ToLower(strings.TrimSpace(backend))
	driver := backend
	switch backend {
	case "postgresql", "postgres":
		backend, driver = "postgres", "pgx"
	case "sqlserver", "mssql":
		backend, driver = "mssql", "sqlserver"
		var err error
		dsn, err = sqlServerDSN(dsn)
		if err != nil {
			return nil, err
		}
	case "hive":
		driver = "odbc"
		if address, native := strings.CutPrefix(dsn, "hive://"); native {
			driver, dsn = "hive", address
		}
	case "presto":
		if slices.Contains(sql.Drivers(), "doolla-presto") {
			driver = "doolla-presto"
		}
	case "oracle":
		if slices.Contains(sql.Drivers(), "doolla-oracle") {
			driver = "doolla-oracle"
		}
	case "bigquery", "clickhouse", "duckdb", "mysql", "snowflake":
	default:
		return nil, fmt.Errorf("unsupported database backend %q", backend)
	}
	if !slices.Contains(sql.Drivers(), driver) {
		return nil, fmt.Errorf("%s driver is unavailable: link database/sql driver %q or build doolla-db with -tags doolla_%s", backend, driver, backend)
	}
	return sql.Open(driver, dsn)
}
