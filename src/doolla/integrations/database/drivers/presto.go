//go:build doolla_presto

package drivers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"

	"github.com/prestodb/presto-go-client/presto"
)

// Presto v1 returns *presto.EOF with a query ID instead of io.EOF. Adapt
// Rows.Next at the database/sql boundary, keeping the original DSN and auth.
func init() {
	db, err := sql.Open("presto", "")
	if err != nil {
		panic(err)
	}
	sql.Register("doolla-presto", prestoDriver{db.Driver()})
	db.Close()
}

type prestoDriver struct{ driver.Driver }

func (d prestoDriver) Open(dsn string) (driver.Conn, error) {
	conn, err := d.Driver.Open(dsn)
	if err != nil {
		return nil, err
	}
	return prestoConn{conn.(*presto.Conn)}, nil
}

type prestoConn struct{ *presto.Conn }

func (c prestoConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	// The Presto HTTP endpoint expects a statement without a terminal delimiter.
	query = strings.TrimSuffix(strings.TrimSpace(query), ";")
	stmt, err := c.Conn.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return prestoStmt{stmt}, nil
}

type prestoStmt struct{ driver.Stmt }

func (s prestoStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := s.Stmt.(driver.StmtQueryContext).QueryContext(ctx, args)
	if err != nil {
		return nil, err
	}
	return prestoRows{rows.(driver.RowsColumnTypeDatabaseTypeName)}, nil
}

type prestoRows struct {
	driver.RowsColumnTypeDatabaseTypeName
}

func (r prestoRows) Next(dest []driver.Value) error {
	err := r.RowsColumnTypeDatabaseTypeName.Next(dest)
	if _, end := err.(*presto.EOF); end {
		return io.EOF
	}
	return err
}
