# doolla.ai 数据库验收

> 历史验收记录。文中的接口和命令按当前命名展示，原始测试日期、结果及二进制指纹对应更名前的构建。

2026-11-25 补验通过：发行版 SDK 通过 Argus ODBC 0.7.0、unixODBC 2.3.14 查询 Apache HiveServer2 4.0.1 的双行表。下列 11 月 23 日结果为初次验收记录。[最新部署与集成验收](Go-8089升级与本地集成验收.md)

2026 年 11 月 23 日在本机从 更名前的 Go 工作区运行。服务探针调用与 CLI 共用的 `database.Open`，将连接池传给 `doolla.SQLTool.Run`，并检查工具返回的 `dataframe`、行列数和值。SQLite 和 MES 另有业务数据断言。11 个后端中有 9 个已连接真实引擎并核对结果，BigQuery 和 Snowflake 缺少云端验收配置。

| 后端 | 状态 | 服务版本与验收结果 |
| --- | --- | --- |
| SQLite | 通过 | SQLite 3.50.4，使用文件数据库按地区汇总得到 7 和 3。已验证 CSV 转柱状图、跨用户读取被拒绝、只读连接拒绝更新及可写连接更新 3 行。 |
| DuckDB | 通过 | DuckDB v2.0.0，内存数据库返回一行一列 42。两个并行占用的物理连接都能读到初始化 SQL 创建的会话临时视图，初始化失败时重试仍返回错误。CLI 已验证省略 DSN 的启动和初始化报错。 |
| PostgreSQL | 通过 | PostgreSQL 18.6，在独立数据目录启动数据库，经 Unix 套接字使用 pgx 查询得到 42。 |
| MySQL | 通过 | MySQL 26.7.0，在独立数据目录启动 mysqld，经 Unix 套接字查询得到 42。 |
| Microsoft SQL Server | 通过 | Microsoft SQL Server 2022 Developer，镜像构建版本 16.0.4265.3。在独立临时容器中使用 go-mssqldb 直连，查询得到 42。 |
| Presto | 通过 | 官方 PrestoDB 0.300-01f660f，独立本机协调器，Temurin 21。标准 Presto DSN 经 Go 驱动与 SQLTool 实测常量 42 和三行求和 42，每次均返回一行一列。Trino 484 的 MES 兼容性另见下文。 |
| ClickHouse | 通过 | 官方 ClickHouse 26.10.10.3 原生服务，绑定回环地址并使用独立数据目录。clickhouse-go 通过原生 TCP 协议查询得到 42。 |
| Hive | 通过 | Apache HiveServer2 4.0.1，以原生 Thrift 和 NOSASL 连接独立服务，SQLTool 返回 42，CLI 启动连接检查后首页返回 HTTP 200。已有 ODBC DSN 的驱动选择通过回归测试，厂商 ODBC 驱动与生产认证待现场验证。 |
| Oracle | 通过 | Oracle AI Database 26ai Free Lite 23.26.4.0.0。首次使用实例初始化账号登录被拒绝，在独立 PDB 中创建仅有 CREATE SESSION 权限的测试账号后，Go 驱动对 DUAL 查询得到 42。 |
| BigQuery | 缺少服务配置 | 需要获准使用的 GCP 项目、数据集、区域和查询身份，可使用已配置的应用默认凭据或只读账号。 |
| Snowflake | 缺少服务配置 | 需要 Snowflake 账号标识、数据库、schema、可用仓库和查询身份。 |

PrestoDB 的协调器报告查询状态为 `FINISHED`。常量查询 ID 为 `20261123_125710_00000_xej2p`、`20261123_125710_00002_xej2p`。聚合查询 `SELECT sum(n) AS answer FROM (VALUES 10, 12, 20) AS sample(n)` 的 ID 为 `20261123_125710_00001_xej2p`。

Trino 484 的 MES 实例使用自定义 HTTP transport 转换 Presto 和 Trino 请求头并限制可执行的 SQL。已验证订单汇总返回一行两列，尝试读取订单明细时被拒绝。MES 验收使用以下两项聚合。Go 工具拿到实际行值后才判为通过。Go 工具直测的 Trino 查询 ID 为 `20261120_173701_00024_vmhby`。Chrome 中文复验由本机 Qwen2.5:1.5b 生成统计 SQL，得到 4100 / 4100，Trino 查询 ID 为 `20261120_181059_00026_vmhby`。见[本轮功能与真实服务验收](Go-功能与真实服务验收.md)。

```sql
SELECT count(*) AS total_orders, count(DISTINCT moid) AS unique_order_ids
FROM mes.dbo.pro_mo
```

## 本地复测

在仓库根目录执行下列命令。SQLite 测试自建临时数据。DuckDB 空 DSN 会创建内存数据库。

```sh
go -C src/doolla test -count=1 -run '^(TestQueryAndVisualization|TestExplainUpdateReturnsQueryPlan)$' -v .
DOOLLA_TEST_DSN_DUCKDB='' go test \
  -race -tags doolla_duckdb -count=1 -run '^TestDuckDB|^TestLiveBackends$/^duckdb$' -v ./tests/drivers
```

其他后端使用各自获准访问的 DSN。在终端从凭据管理器导入 `DOOLLA_TEST_DSN_<大写后端名>`，如 PostgreSQL 的 `DOOLLA_TEST_DSN_POSTGRES`，再用以下命令选择对应子测试。环境变量缺失时子测试会显示 `SKIP`，只有对应子测试显示 `PASS` 才代表服务验收成功。

```sh
backend=postgres
go test -mod=readonly \
  -tags "doolla_${backend}" -count=1 -run "^TestLiveBackends$/^${backend}$" -v ./tests/drivers
```

参数 `backend` 支持 `bigquery`、`clickhouse`、`duckdb`、`hive`、`mssql`、`mysql`、`oracle`、`postgres`、`presto` 和 `snowflake`。Oracle 执行 `SELECT 42 AS answer FROM DUAL`，其余执行 `SELECT 42 AS answer`。Presto 子测试默认连接标准 PrestoDB 端点，现场 MES 测试使用前述 Trino transport 和聚合语句。

Hive 的原生 Thrift 连接可使用形如 `hive://analyst@host:10000/default?auth=NOSASL` 的 DSN。已配置厂商 ODBC 驱动的环境继续使用 `DSN=HiveWarehouse`。Oracle 使用形如 `oracle://user:password@host:1521/FREEPDB1` 的连接信息。

Apple Silicon 上编译 Hive 标签时需设置 unixODBC 头文件和库路径。下面的命令一次构建并检查全部可选驱动注册。构建检查与上表中的服务验收分别记录。

```sh
CGO_CFLAGS="-I$(brew --prefix unixodbc)/include" \
CGO_LDFLAGS="-L$(brew --prefix unixodbc)/lib" \
go test -mod=readonly -count=1 \
  -tags 'doolla_bigquery doolla_clickhouse doolla_duckdb doolla_hive doolla_mssql doolla_mysql doolla_oracle doolla_postgres doolla_presto doolla_snowflake' ./tests/drivers
```

DSN 形式及 DuckDB SDK 和 CLI 参数见[数据库驱动说明](../../src/doolla/integrations/database/README.md)。
