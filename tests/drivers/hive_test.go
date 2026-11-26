//go:build doolla_hive

package drivers_test

import (
	"database/sql"
	"testing"

	"doolla.ai/sdk/integrations/database"
	_ "doolla.ai/sdk/integrations/database/drivers"
)

func TestHiveDSNSelectsDriver(t *testing.T) {
	for _, tc := range []struct{ dsn, driver string }{
		{"DSN=HiveWarehouse", "odbc"},
		{"hive://analyst@localhost:10000/default?auth=NOSASL", "hive"},
	} {
		pool, err := database.Open("hive", tc.dsn)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := sql.Open(tc.driver, "")
		if err != nil {
			t.Fatal(err)
		}
		if pool.Driver() != expected.Driver() {
			t.Errorf("%q selected %T, want %T", tc.dsn, pool.Driver(), expected.Driver())
		}
		pool.Close()
		expected.Close()
	}
}
