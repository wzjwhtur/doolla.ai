//go:build !cgo

package doolla

import (
	"database/sql"
	"errors"
)

var errSQLiteUnavailable = errors.New("the SQLite integration requires a build with CGO_ENABLED=1")

func OpenSQLite(path string) (*sql.DB, error)         { return nil, errSQLiteUnavailable }
func OpenWritableSQLite(path string) (*sql.DB, error) { return nil, errSQLiteUnavailable }
