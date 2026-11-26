//go:build doolla_duckdb

package drivers

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/duckdb/duckdb-go/v2"
	"doolla.ai/sdk/integrations/database"
)

// Embedding the native connector also forwards Close, releasing the database
// handle when the sql.DB is closed.
type initializedDuckDB struct {
	*duckdb.Connector
	initSQL string
}

func (c initializedDuckDB) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.(driver.ExecerContext).ExecContext(ctx, c.initSQL, nil); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize DuckDB connection: %w", err), conn.Close())
	}
	return conn, nil
}

func init() {
	database.RegisterDuckDBConnector(func(dsn, initSQL string) (driver.Connector, error) {
		connector, err := duckdb.NewConnector(dsn, nil)
		if err != nil {
			return nil, err
		}
		return initializedDuckDB{connector, initSQL}, nil
	})
}
