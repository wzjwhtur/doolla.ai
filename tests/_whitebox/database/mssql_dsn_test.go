package database

import (
	"strings"
	"testing"
)

func TestSQLServerODBC(t *testing.T) {
	for _, prefix := range []string{"", "odbc:", "ODBC:"} {
		input := prefix + `Driver={ODBC Driver 18 for SQL Server};Server=tcp:db,1438;UID=reader;PWD={ x;"}}};Encrypt=yes;TrustServerCertificate={YES};MultiSubnetFailover=no;`
		got, err := sqlServerDSN(input)
		want := `server="tcp:db,1438";uid="reader";pwd=" x;""}";encrypt="yes";trustservercertificate="true";multisubnetfailover="false"`
		if err != nil || got != want {
			t.Fatalf("ODBC profile mismatch: %q, %v", got, err)
		}
	}
	for _, unchanged := range []string{`sqlserver://user:pwd@host?database=demo`, `server=host;pwd="x;Driver=embedded field";database=demo`} {
		got, err := sqlServerDSN(unchanged)
		if err != nil || got != unchanged {
			t.Fatal("changed native connection string", err)
		}
	}
	if _, err := sqlServerDSN(`Driver={ODBC};PWD={secret`); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("expected redacted invalid ODBC parameter error")
	}
}

func TestSQLServerODBCEncryption(t *testing.T) {
	for _, tt := range []struct {
		dsn      string
		required bool
	}{
		{"Driver={ODBC Driver 18 for SQL Server};Server=db", true},
		{"Driver={ODBC Driver 17 for SQL Server};Server=db", false},
		{"Driver={ODBC Driver 17 for SQL Server};Server=db;Authentication=SqlPassword", true},
		{"Driver={ODBC Driver 18 for SQL Server};Server=db;Encrypt=no", false},
	} {
		got, err := sqlServerDSN(tt.dsn)
		if err != nil || strings.Contains(got, "encrypt=yes") != tt.required {
			t.Errorf("normalized %q, err %v", got, err)
		}
	}
}
