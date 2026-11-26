package database

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
)

// DuckDBConnector opens a native DuckDB connector with a per-connection initializer.
type DuckDBConnector func(dsn, initSQL string) (driver.Connector, error)

var duckDBConnector atomic.Pointer[DuckDBConnector]

// RegisterDuckDBConnector is called at startup by the optional doolla_duckdb driver.
func RegisterDuckDBConnector(open DuckDBConnector) {
	if open == nil || !duckDBConnector.CompareAndSwap(nil, &open) {
		panic("duplicate or nil DuckDB connector")
	}
}

// OpenDuckDB runs initSQL once on every new physical connection in the pool.
// Use connection-local SQL or idempotent statements when modifying shared state.
func OpenDuckDB(dsn, initSQL string) (*sql.DB, error) {
	if initSQL == "" {
		return Open("duckdb", dsn)
	}
	open := duckDBConnector.Load()
	if open == nil {
		return nil, errors.New("DuckDB initialization requires the doolla_duckdb driver")
	}
	connector, err := (*open)(dsn, initSQL)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}
