//go:build doolla_oracle

package drivers

import "testing"

func TestOracleQueryDelimiter(t *testing.T) {
	for _, tt := range []struct{ query, want string }{
		{"SELECT 42 FROM DUAL;  ", "SELECT 42 FROM DUAL  "},
		{"/* ; */ WITH c AS (SELECT 1 FROM dual) SELECT * FROM c; -- ;", "/* ; */ WITH c AS (SELECT 1 FROM dual) SELECT * FROM c -- ;"},
		{"SELECT ';'';' AS \"id;\" FROM dual /* ; */", "SELECT ';'';' AS \"id;\" FROM dual /* ; */"},
		{"SELECT q'[it's; -- /* ]' FROM dual; /* ; */", "SELECT q'[it's; -- /* ]' FROM dual /* ; */"},
		{"SELECT 1 FROM dual -- ;", "SELECT 1 FROM dual -- ;"},
		{"BEGIN NULL; END;", "BEGIN NULL; END;"},
	} {
		if got := oracleQuery(tt.query); got != tt.want {
			t.Errorf("oracleQuery(%q) = %q, want %q", tt.query, got, tt.want)
		}
	}
}
