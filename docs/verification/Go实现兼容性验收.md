# Doolla Go 实现兼容性验收

> 历史验收记录。文中的接口和命令按当前命名展示，原始测试日期、结果及二进制指纹对应更名前的构建。

2026-11-25 在本地验收 Go 实现。协议对照使用 PyPI 发布的上游 Python 包。验收覆盖新版 Agent 及其集成，包括 HTTP 服务、SDK、数据库和记忆适配器。

**当前结论**　本机 MES 问数链路、模型协议和下表列出的行为已通过测试。数据库已有 9/11 项真实引擎证据，记忆后端已有 8/11 项真实服务或存储引擎证据。Hive 原生及 Argus ODBC 路径均已连接真实 HiveServer2，仍有五种托管集成待真实云资源验收。对所有集成作生产替代承诺仍缺少这些证据。

本报告所述独立端口验收之后，8089 已实际切换到最终包。后续已在 macOS arm64 通过 Argus ODBC 查询真实 HiveServer2 的双行业务表。端口复核及其余离线结果见[8089 升级与本地集成验收](Go-8089升级与本地集成验收.md)。

## Python 行为与 Go 实现

| 项目 | 已验证的行为 | 复查入口 |
| --- | --- | --- |
| Agent 生命周期 | 成功工具结果可替换，失败、未知或不可用工具也可交给完成钩子处理。下一次模型调用和完整会话回调读取替换后的值。达到工具轮数上限即结束，关闭自动保存时跳过工具检查点与最终写入。实际 Python 包也验证了未知工具经钩子修复后继续答复 | [生命周期测试](../../tests/lifecycle_observability_test.go) |
| 扩展、权限与审计 | 用户解析、界面分组、工具授权、历史存取、消息增强、处理前后中间件、请求内追踪。自定义工具能从当前轮次读取记忆并记录指标 | [工具上下文测试](../../tests/tool_context_test.go) · [权限与真实服务记录](Go-功能与真实服务验收.md) |
| 模型调用 | 实例默认值和单次请求覆盖分别生效。真实 Ollama 请求以温度 0、输出上限 128 执行两轮，SQLite 返回 30。独立运行的 6 种协议均通过同步、流式和工具往返。Azure 与 Gemini 经过本机 LiteLLM | [逐请求参数](../../tests/model_request_test.go) · [协议端点与断言](Go-本地模型验收.md) |
| 组件与传输 | 取 Python 实包生成的五个组件变更，逐字段比较新增、修改、同 ID 替换、跨 ID 替换和删除，比较时排除时间戳。Go 支持批次、嵌套删除、位置提示和历史快照。Python FastAPI 路由及 Go WebSocket 在同一连接上连续收到三次错误输入后，均可处理有效请求并发送完成帧 | [Python 基准输出](../../tests/testdata/component_changes_baseline.json) · [组件测试](../../tests/component_parity_test.go) · [传输测试](../../tests/_whitebox/sdk/server_replay_test.go) |
| SQL、图表及工作区 | SQL 返回查询类型、结果表及简版消息。实际 SQLite 查询经 CSV 交给可注入的图表生成器。文件工具验证多行编辑，进程工具验证标准输出、标准错误与非零退出码。SQL Server 接受原始 pyodbc 与带 `odbc:` 前缀两种连接串 | [工具测试](../../tests/tool_parity_test.go) · [连接串测试](../../tests/_whitebox/database/mssql_dsn_test.go) |
| 会话及记忆 | 文件会话可导入、导出 Python 格式。FAISS、嵌入式 Qdrant、本地 Chroma 与 Python 客户端互读实测通过。Milvus 使用临时集合验证写入、检索排名、删除和清理。记忆参数中的大整数保留精度 | [会话实现](../../src/doolla/conversations.go) · [存储逐项记录](Go-记忆存储验收.md) |
| 评估 | 本机 Ollama 的 Qwen2.5 1.5B 和 0.5B 分别调用 `run_sql` 查询 SQLite，均得到 35，各通过 1/1 条样例。已生成 CSV 和 HTML 比较报告。测试覆盖 JSON 输入、评委响应解析和评估追踪上下文 | [评估测试](../../tests/evaluation/parity_test.go) · [可运行示例](../../examples/compare) |

Python 包声明的错误恢复策略含 `retry`、`fail`、`fallback`、`skip`。实际调用原版 Agent 时，模型抛错和工具抛错两个场景的策略回调次数均为零。Go 的可选 `Recover` 已单独测试重试序号、等待和取消。OpenAI Chat 接入点要求 Python 风格的 `max_tokens` 字段时，在 Go 配置启用 `openai-legacy-max-tokens`。[配置参考](../guide/配置参考.md)

本轮另启动隔离的 Oracle AI Database 26ai Free 23.26.4.0.0。只授予 `CREATE SESSION` 的 FREEPDB1 用户通过 `database.Open` 和 `SQLTool` 执行 `SELECT 42 AS answer FROM DUAL; -- terminal delimiter`，得到一行一列 42，耗时 0.25 秒。临时容器及其卷已清理。[Oracle 方言实现](../../src/doolla/integrations/database/drivers/oracle.go) · [真实引擎用例](../../tests/drivers/drivers_test.go)

## 真实 MES 浏览器验收

从当前源码编入 `doolla_mssql` 标签，在隔离端口启动服务。读取已有的原始 ODBC 连接串，沿用已配置的自托管 Qwen 对话模型。该实例使用独立工作目录和本进程记忆，另用临时 Milvus 集合验收持久记忆。数据库账号、模型密钥及连接串的值不写入报告。

独立连接在业务库执行以下只读基准查询，得到一行 `4100, 4100`。

```sql
SELECT COUNT_BIG(*) AS total_orders,
       COUNT_BIG(DISTINCT moid) AS unique_order_ids
FROM dbo.pro_mo
```

随后在本机 Google Chrome 打开 Go 的聊天首页，输入“请查询 MES 生产工单表 dbo.pro_mo 的记录总数和按 moid 去重后的工单数”。验证网页实际渲染的表格、服务端生成的 CSV 以及中文答复。表格和 CSV 的两列均为 4100，中文答复说明工单号未重复，SSE 请求返回 HTTP 200 并以 `[DONE]` 结束。

同一份发布实包另跑了标签内的 `test_workflow.py`、`test_tool_permissions.py`、`test_llm_context_enhancer.py` 和 `test_memory_tools.py`，共 56 个 Python 基准测试全部通过。这组结果记录上游行为；Go 的回归覆盖见前表和下方命令。

另用最终安装包和配置中的 BGE-M3（1024 维）、Milvus、自托管 Qwen、真实 MES 做了联合验收。管理员通过 `save_text_memory` 在随机命名的临时集合写入核对规范及随机验收代号。全新 Chrome 会话提交的问题里没有该代号；网页答复返回正确代号，且实际 SQL 表格和 CSV 仍为 4100 / 4100。该轮从提交到答复耗时约 20 秒，完成后已删除并确认临时集合消失。

## 尚待真实资源验收

| 接入项 | 已有证据 | 签收所需资源 |
| --- | --- | --- |
| BigQuery、Snowflake | 两个 Go 驱动均已在本地协议服务完成从连接、发出 SQL 到 dataframe 的回归。BigQuery 覆盖 REQUIRED 整数、字符串与布尔值、NULLABLE 空值、REPEATED 数组和零行结果，默认异步作业也通过状态轮询及两页结果读取。Snowflake 覆盖登录、查询和会话关闭 | 可执行只读查询的 BigQuery 项目和数据集，Snowflake 账号、仓库、数据库及角色 |
| Azure AI Search | 索引创建、查询、分页、删除与旧索引兼容的 HTTP 协议测试 | 可读写临时索引的测试服务 |
| Pinecone | 索引控制面和向量写入、检索、分页、删除的 HTTP 协议测试 | 现有测试索引与可读写的隔离 namespace |
| Premium | 组织及认证头、保存、搜索、排名、删除与清理的 HTTP 协议测试 | 测试组织、端点和凭据 |


按本项目选定的本地模型验收口径，六种模型协议均已验证。本轮没有验证公有云模型账号的认证、配额或计费。数据库及记忆引擎的既有实测、运行日期和环境详见各项原始记录。[数据库](Go-数据库验收.md) · [记忆后端](Go-记忆存储验收.md)

## 复测

从仓库根目录运行以下命令。真实模型与 Milvus 用例需要另行设置对应环境变量；测试结果中的 `SKIP` 表示该项没有执行。

```sh
go -C src/doolla vet -mod=readonly ./...
go vet -mod=readonly ./...
go -C src/doolla test -mod=readonly -race -count=1 ./...
go test -mod=readonly -race -count=1 ./...
node frontends/webcomponent/tests/run-browser-tests.mjs
```

设置 `DOOLLA_TEST_AGENT_MODEL`、`DOOLLA_TEST_AGENT_URL` 后可执行 `go test -race -count=1 -v ./tests -run '^TestLiveLifecycleWithOllamaAndSQLite$'`。设置 `DOOLLA_TEST_MILVUS_URL` 后可执行 `go -C src/doolla test -race -count=1 -v ./integrations/memory -run '^TestLiveMilvus$'`。其余后端的环境变量和独立运行方式见分类报告。

BigQuery 回归最初在 `viant/bigquery v0.5.4` 上复现合法必填整数列解码失败。可选数据库模块现锁定上游修复提交 `ce985cbbc860`。测试从原始 BigQuery REST schema 同时检查列的可空性和最终表格的值，可用 `go test -mod=readonly -race -count=1 -tags 'doolla_bigquery doolla_snowflake' ./tests/drivers` 复跑。[BigQuery 协议测试](../../tests/drivers/bigquery_protocol_test.go) · [Snowflake 协议测试](../../tests/drivers/snowflake_protocol_test.go)

取得测试资源后，可单独验收剩余集成。数据库探针读取 `DOOLLA_TEST_DSN_BIGQUERY`、`DOOLLA_TEST_DSN_SNOWFLAKE` 或 `DOOLLA_TEST_DSN_HIVE`。Azure AI Search 使用 `DOOLLA_TEST_AZURESEARCH_URL` 和 `DOOLLA_TEST_AZURESEARCH_KEY`。Pinecone 使用 `DOOLLA_TEST_PINECONE_URL`、`DOOLLA_TEST_PINECONE_KEY` 和现有索引的 `DOOLLA_TEST_PINECONE_DIMENSION`。Premium 使用 `DOOLLA_TEST_PREMIUM_URL`、`DOOLLA_TEST_PREMIUM_KEY`、`DOOLLA_TEST_PREMIUM_ORG`。

```sh
go test -mod=readonly -count=1 -v -tags 'doolla_bigquery doolla_snowflake' ./tests/drivers -run '^TestLiveBackends$/(bigquery|snowflake)$'
go test -mod=readonly -count=1 -v -tags doolla_hive ./tests/drivers -run '^TestLiveBackends$/hive$'
go -C src/doolla test -mod=readonly -count=1 -v ./integrations/memory -run '^TestLiveMemoryBackends$/(azuresearch|pinecone)$'
go -C src/doolla test -mod=readonly -count=1 -v ./integrations/memory -run '^TestLivePremium$'
```

Hive 的 ODBC 验收应使用 `DSN=...` 连接串，以便测试实际经过厂商驱动。Azure 测试创建并删除临时索引；Pinecone 使用现有索引上的随机 namespace，Premium 使用隔离的测试工具名。核对这些子测试实际显示 `PASS` 后更新真实验收数。

## 本地交付

最终本地版本号为 `v0.20261125.1`。安装复测曾命中缓存里的上游 Python 标签 `v0.0.8`；最终版本号避开该标签，打包脚本也会识别这种冲突。Go 1.26.6 全包竞态测试、`go vet` 和 Chrome 前端回归通过。全数据库标签构建通过，本地 Chroma 模块已补齐 YAML 间接依赖，可在 `-mod=readonly` 下独立构建。另在空白 Go 项目使用本地文件模块代理安装 SDK、SQL Server 版 `doolla-db` 与 `doolla-chroma`，SDK 消费程序运行成功，两种 CLI 均能启动帮助入口。从模块代理安装的数据库二进制另经同一 Chrome 流程复查 MES，表格、CSV 及 SSE 结束标记全部通过。

本机 `dist/releases/v0.20261125.1/` 含模块代理压缩包、SHA-256 清单和 macOS arm64 安装包。已通过两份产物的校验和，并解包执行其自带的 `doolla`。macOS 包为未签名的本机测试产物，默认命令包含 SQLite，SQL Server 部署使用编入 `doolla_mssql` 的命令。初次安装验收使用临时端口。随后已将 8089 实例切换到该发行版，并在原端口复核 MES 查询。[模块打包与安装](../guide/Go模块打包与安装.md)
