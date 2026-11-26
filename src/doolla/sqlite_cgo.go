//go:build cgo

package doolla

import (
	"database/sql"
	"errors"
	"github.com/mattn/go-sqlite3"
	"net/url"
	"path/filepath"
	"strings"
)

// OpenSQLite opens the configured database for question answering. Apply the
// same policy on each pooled connection, including to attached databases.
func OpenSQLite(path string) (*sql.DB, error) {
	return openSQLiteFile(path, "doolla_sqlite", "ro")
}

// OpenWritableSQLite opens or creates a SQLite database for read/write use.
func OpenWritableSQLite(path string) (*sql.DB, error) { return openSQLiteFile(path, "sqlite3", "rwc") }

func openSQLiteFile(path, driver, mode string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("empty SQLite path")
	}
	if path == ":memory:" {
		db, err := sql.Open(driver, path)
		if err == nil {
			db.SetMaxOpenConns(1)
		}
		return db, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=" + mode}
	return sql.Open(driver, u.String())
}

func init() {
	sql.Register("doolla_sqlite", &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		conn.RegisterAuthorizer(func(action int, a, b, db string) int {
			switch action {
			case sqlite3.SQLITE_SELECT, sqlite3.SQLITE_READ, 33: // SQLITE_RECURSIVE
				return sqlite3.SQLITE_OK
			case sqlite3.SQLITE_PRAGMA:
				switch strings.ToLower(a) {
				case "table_info", "table_xinfo", "table_list", "index_info", "index_list", "index_xinfo", "foreign_key_list", "database_list":
					return sqlite3.SQLITE_OK
				}
			case sqlite3.SQLITE_FUNCTION:
				if !strings.EqualFold(b, "load_extension") {
					return sqlite3.SQLITE_OK
				}
			}
			return sqlite3.SQLITE_DENY
		})
		return nil
	}})
}
