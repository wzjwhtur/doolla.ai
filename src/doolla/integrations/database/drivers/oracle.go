//go:build doolla_oracle

package drivers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"

	go_ora "github.com/sijms/go-ora/v2"
)

func init() {
	sql.Register("doolla-oracle", oracleDriver{go_ora.GetDefaultDriver()})
}

type oracleDriver struct{ *go_ora.OracleDriver }

func (d oracleDriver) OpenConnector(dsn string) (driver.Connector, error) {
	c, err := d.OracleDriver.OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return oracleConnector{c}, nil
}

type oracleConnector struct{ driver.Connector }

func (c oracleConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return oracleConn{conn.(*go_ora.Connection)}, nil
}

type oracleConn struct{ *go_ora.Connection }

func (c oracleConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Connection.QueryContext(ctx, oracleQuery(query), args)
}

// oracleQuery 只去掉 SELECT 或 WITH 查询的末尾分号。SQL 客户端使用它划分语句，
// 通过 Oracle 驱动发送时则会报错；引号、注释和 PL/SQL 块里的分号必须保留。
func oracleQuery(query string) string {
	if !strings.Contains(query, ";") {
		return query
	}
	first, last := "", -1
	for i := 0; i < len(query); {
		start := i
		switch {
		case strings.HasPrefix(query[i:], "--"):
			n := strings.IndexAny(query[i:], "\r\n")
			if n < 0 {
				i = len(query)
			} else {
				i += n
			}
			continue
		case strings.HasPrefix(query[i:], "/*"):
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				return query
			}
			i += end + 4
			continue
		case strings.ContainsRune(" \t\r\n\v\f", rune(query[i])):
			i++
			continue
		case i+2 < len(query) && (query[i] == 'q' || query[i] == 'Q') && query[i+1] == '\'':
			delimiter := query[i+2]
			if n := strings.IndexByte("[({<", delimiter); n >= 0 {
				delimiter = "])}>"[n]
			}
			end := strings.Index(query[i+3:], string([]byte{delimiter, '\''}))
			if end < 0 {
				return query
			}
			i += end + 5
		case query[i] == '\'' || query[i] == '"':
			quote := query[i]
			i++
			for i < len(query) {
				pos := strings.IndexByte(query[i:], quote)
				if pos < 0 {
					return query
				}
				i += pos + 1
				if i >= len(query) || query[i] != quote {
					break
				}
				i++
			}
		default:
			i++
			if first == "" {
				for i < len(query) && (query[i] >= 'a' && query[i] <= 'z' || query[i] >= 'A' && query[i] <= 'Z') {
					i++
				}
			}
		}
		if first == "" {
			first = strings.ToUpper(query[start:i])
			if first != "SELECT" && first != "WITH" {
				return query
			}
		}
		last = start
	}
	if last >= 0 && query[last] == ';' {
		return query[:last] + query[last+1:]
	}
	return query
}
