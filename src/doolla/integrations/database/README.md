# SQL databases in Go

The main Go module provides the SQLite driver. To deploy other databases, build the optional CLI from the repository root using Go 1.26+ and one or more driver tags. Its command-line flags and web API match the main CLI.

```sh
go -C src/doolla/integrations/database/drivers build -tags 'doolla_postgres doolla_presto' -o /tmp/doolla-db ./cmd/doolla-db
export DOOLLA_DATABASE=postgres
export DOOLLA_DSN='postgres://analyst@db.example/warehouse?sslmode=verify-full'
/tmp/doolla-db
```

The backend and DSN can also be passed using `-database` and `-dsn`. Use a database account with the grants required for the configured tools. Supply credentials through the driver's supported mechanisms. DSNs below show the expected format.

| Backend / tag suffix | Go driver | DSN example or setup |
| --- | --- | --- |
| bigquery | viant/bigquery | `bigquery://your-project/US/dataset`. Set `GOOGLE_APPLICATION_CREDENTIALS` or use the driver's credential options. |
| clickhouse | ClickHouse/clickhouse-go/v2 | `clickhouse://analyst@db.example:9000/warehouse` |
| duckdb | duckdb/duckdb-go/v2 | `/data/warehouse.duckdb?access_mode=read_only` for an existing file. The embedded engine needs CGO and a supported platform. |
| hive | sqlflow.org/gohive or alexbrainman/odbc | `hive://analyst@db.example:10000/default?auth=NOSASL` selects native HiveServer2 Thrift. Other DSNs, such as `DSN=HiveWarehouse`, use the installed ODBC driver and its configured authentication. Unix builds require unixODBC development headers and library. |
| mssql | microsoft/go-mssqldb | `sqlserver://analyst@db.example:1433?database=warehouse` or a Python ODBC connection string, e.g. `Driver={ODBC Driver 18 for SQL Server};Server=tcp:db.example,1433;Database=warehouse;UID=analyst;Encrypt=yes`. An optional `odbc:` prefix is accepted. |
| mysql | go-sql-driver/mysql | `analyst@tcp(db.example:3306)/warehouse?parseTime=true` |
| oracle | sijms/go-ora/v2 | `oracle://analyst:password@db.example:1521/ORCLPDB1` |
| postgres | jackc/pgx/v5/stdlib | `postgres://analyst@db.example/warehouse?sslmode=verify-full` |
| presto | prestodb/presto-go-client/presto | `https://analyst@db.example:8443?catalog=hive&schema=default` |
| snowflake | snowflakedb/gosnowflake | `analyst:password@account/database/schema?warehouse=compute_wh`. The driver also supports key-pair and external authentication. |

For Homebrew's unixODBC on Apple Silicon, provide `CGO_CFLAGS="-I$(brew --prefix unixodbc)/include"` and `CGO_LDFLAGS="-L$(brew --prefix unixodbc)/lib"` when building with `doolla_hive`.

A Go application can import its driver directly, open the pool with `database.Open(backend, dsn)`, and pass the returned `*sql.DB` to `doolla.SQLTool`. The application owns `db.Close()`. Driver settings, authentication, and TLS options are passed to the underlying driver in its native DSN format.

For Presto, import the optional `doolla.ai/sdk/integrations/database/drivers` package for its registration side effect, build with `-tags doolla_presto`, and call `database.Open("presto", dsn)`. This path translates the upstream v1 driver's query-ID end marker to the standard end of a result set, so callers can iterate every row and check `rows.Err()`. The upstream driver still handles the DSN, authentication, and HTTP client configuration.

DuckDB accepts connection initialization SQL. For example, run
`doolla-db -database duckdb -duckdb-init-sql 'CREATE TEMP VIEW answers AS SELECT 42 AS value'`
to open an in-memory database with a connection-local view. Supply `-dsn` for a file-backed
database. The SDK entry point is `database.OpenDuckDB(dsn, initSQL)`, with the tagged
drivers package imported. Initialization runs on each new physical connection, so use
temporary objects or idempotent SQL when creating shared objects. Initialization errors
are returned to the caller when connecting.

## Verification

Set `DOOLLA_TEST_DSN_<BACKEND>` to exercise the selected database with a real query. Each configured backend requires its build tag. To test an in-process DuckDB from the repository root, run the following command.

```sh
DOOLLA_TEST_DSN_DUCKDB='' go test -tags doolla_duckdb -run TestLiveBackends -v ./tests/drivers
```

The Presto protocol test exercises the driver's HTTP submission and pagination using an in-process HTTP server. A `DOOLLA_TEST_DSN_PRESTO` additionally exercises a real cluster. Building with all ten tags also verifies that every driver is registered in the same process.

```sh
go test -mod=readonly -tags 'doolla_bigquery doolla_clickhouse doolla_duckdb doolla_hive doolla_mssql doolla_mysql doolla_oracle doolla_postgres doolla_presto doolla_snowflake' ./tests/drivers
```
