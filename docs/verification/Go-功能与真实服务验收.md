# Go 功能与真实服务验收

> 历史验收记录。文中的接口和命令按当前命名展示，原始测试日期、结果及二进制指纹对应更名前的构建。

2026-11-25 的补充实现、Python 实包对照及 MES 浏览器验收见[替代验收记录](Go实现兼容性验收.md)。

本轮以上游 Python 包为行为基线，在本地从 `src/doolla` 构建 Go 实现。六种模型适配器通过本机推理验收，11 种数据库中的 9 种、11 种记忆后端中的 8 种通过实际运行验收。其余五种托管集成缺测试资源。测试报告中的云协议名称描述请求格式，实际提供推理的服务和模型单独列出。

## MES 浏览器端到端

2026 年 11 月 23 日在本机 Chrome 的独立会话中打开 Go 演示。Go Agent 使用 Ollama `qwen2.5:1.5b`，通过 Presto 驱动访问 Trino 484 的 MES 目录。演示连接只允许读取工单聚合。以下提交均未提供预期数值。

| 输入 | Go 实际执行的查询 | 数据库返回、页面与落盘结果 |
| --- | --- | --- |
| 指定 SQL，要求执行后回答 | 两项聚合的 Trino ID 为 `20261120_180749_00025_vmhby` | 一行两列，工单总行数和不同工单号数均为 4100，中文答复与 CSV 一致 |
| 只给表名和字段名，问“MES 中一共有多少条工单记录？又有多少个不同的工单号？” | 模型生成聚合 SQL 并调用 `run_sql`，Trino ID 为 `20261120_181059_00026_vmhby` | 表格、模型中文答复及 CSV 均为 4100 / 4100。输入框恢复可用，任务面板显示 2/2 完成 |
| 从仓库中的 Go 示例重新构建并启动，使用全新浏览器会话重复自然语言提问 | 模型生成下方 SQL，Trino ID 为 `20261120_183413_00029_vmhby` | 页面表格为一行两列、答复和落盘 CSV 同为 4100 / 4100。截图取自这次运行 |
| 目录迁移后从根目录的 `examples/mes` 构建，在独立 Chrome 会话中要求执行两项工单聚合 | 本机 Ollama 调用 `run_sql`，Trino ID 为 `20261120_193902_00034_vmhby` | 表格、中文答复和落盘 CSV 均为 4100 / 4100，页面显示 2/2 项任务完成 |

```sql
SELECT COUNT(DISTINCT moid) AS num_unique_moid, COUNT(*) AS total_num
FROM mes.dbo.pro_mo
```

本轮本机演示地址为 `http://127.0.0.1:56707/`，Ollama 地址为 `http://127.0.0.1:54053`。两者均使用回环地址。

如需从仓库另起一个演示，先确认 Ollama 已加载该模型，且本机 Trino 提供 `mes.dbo.pro_mo`。在仓库根目录运行下面的命令，然后在浏览器打开 `http://127.0.0.1:8000/`。示例只接受本机模型和数据库端点，结果写入独立临时目录。[演示源码](../../examples/mes/main.go)

```sh
go run -tags doolla_presto ./examples/mes \
  -trino-url http://127.0.0.1:18080 -model-url http://127.0.0.1:54053 \
  -model qwen2.5:1.5b -listen 127.0.0.1:8000 -stream-model=false \
  -workdir "$(mktemp -d)" -audit
```

另用同一请求 ID 实测断线重连所用的三种传输。首次 SSE 请求查询 MES，再通过轮询和 WebSocket 重放，三次收到的 9 个组件及数值完全相同，Trino 只执行一次，查询 ID 为 `20261120_181854_00027_vmhby`。

一次改写问法的测试中，模型生成了按 `moid` 分组并返回工单号的 SQL，超出本次只读聚合的范围。演示连接在提交 Trino 前拒绝了这次调用，该轮没有数据表，未计为问数成功。

## 功能复测

| 功能 | 验收操作 | 证据 |
| --- | --- | --- |
| 模型终止原因与部分回答 | 分别检查同步和流式完成原因、输出上限下已返回的文本，以及部分工具调用的处理 | [模型验收](Go-本地模型验收.md)；回归用例 `TestTruncationKeepsPartialTextAndSuppressesToolCall` |
| 自定义界面权限 | 对真实 Go HTTP 服务发送普通用户和管理员的 `/help` 请求，核对请求对应的审计事件 | `export_report` 对普通用户拒绝、对管理员允许；公开功能 `help_public` 对两者均允许。补充回归检查工具上下文、工具审计记录和未获授权时的面板内容 |
| 任务面板 | 工具获准后增加任务，完成后用同一任务 ID 更新状态。若执行产生副作用，先保存对应结果再推送工具完成事件 | `TestConfiguredFeaturesReachToolsAndAudit`、`TestTaskCompletionAfterCheckpoint`；本机 Chrome 检查新增、更新和删除任务 |
| DuckDB 初始化 SQL | 两个同时打开的物理连接分别读取各自初始化的临时视图，另行验证失败后的重试 | [数据库验收](Go-数据库验收.md) |
| 工作流历史更新 | 自定义工作流读取上轮消息、替换历史、清空历史，再从文件存储重新载入 | `TestConversationWorkflowReplacesAndClearsHistory` |
| 工具调用轮数上限 | 超限时通过网页协议返回原因、继续提问提示并恢复输入 | `TestHTTPToolLimitFeedback` |
| 带角色权限的完整问数 | 使用完整替换的权限表，访客、分析员和管理员分别从 Go HTTP 入口，经本机 Qwen2.5 查询同一 SQLite 销售表 | 三者实际汇总和答复均为 10。只有分析员能看工具名，界面显示两个完成任务，其余角色各看到一个。管理员未获得配置中省略的默认权限 |
| 评测运行器 | 读取两例 Python 2.x 格式的 YAML，用真实 Qwen2.5、只读 SQLite 和 `run_sql` 执行，检查回答、工具轨迹、用量及 CSV 和 HTML 报告生成 | 汇总 52、最大值 25，两例均通过。实际模型输入输出共计 997 token |
| 工作区和进程工具 | 通过 Go 工具注册表写入、列出、查找、行编辑、读取并运行算术脚本，再用 `pip_install --no-index` 安装 `ensurepip` 自带的 wheel 到临时 target | 真实 CPython 3.14.7 在编辑前返回 13、编辑后返回 42。脚本从临时 target 导入 pip 26.2.1，核对了路径及安装元数据 |
| 记忆检索后回答 | 在临时 Chroma 索引保存随机工厂代号，用本机 ONNX 模型检索，从 Go Agent 将命中项加入上下文，再向本机 Qwen2.5 提问 | 用户问题未包含代号，模型答复命中本次随机值 `CL-B9DE3CB3`，相似度为 0.849。每次复测生成新代号 |
| API 文档入口 | 在 Chrome 打开 `/docs`，加载本机 `/openapi.json`，点击发送默认的 `/help` | 文档列出健康检查和三种聊天传输，实际轮询请求返回 HTTP 200 和一条帮助消息 |

在两个临时 Go CLI 进程上将输出限制设为 2 token，再提交同一个长回答请求。直连 Ollama 的 OpenAI Chat 协议返回 `length`，LiteLLM 的 Responses 路由返回 `incomplete`。两个网页轮询响应都包含服务实际生成的部分文本 `Sure,` 和对应的警告。

## 集成逐项记录

| 集成类别 | 已有运行证据 | 其他情况 |
| --- | --- | --- |
| 六种模型适配器 | Ollama、OpenAI Chat、OpenAI Responses、Anthropic Messages、Azure OpenAI、Gemini 均以本机 Qwen2.5:1.5b 完成同步、流式及工具往返 | Azure 和 Gemini 经本机 LiteLLM 转换协议。云厂商认证、发布的云端模型与计费均未执行；Ollama 的 Responses 截断标记、LiteLLM 的 Gemini 流式用量存在服务侧限制。[模型逐项记录](Go-本地模型验收.md) |
| 数据库 | 11 种后端中的 9 种已通过真实引擎查询，包括 SQLite、DuckDB、PostgreSQL、MySQL、SQL Server、ClickHouse、PrestoDB、Oracle、Apache HiveServer2。Hive 4.0.1 使用原生 Thrift，SQLTool 返回一行一列 42。MES/Trino 另有业务验收 | BigQuery 和 Snowflake 缺测试项目、仓库或可用身份。Hive 的 ODBC 路径已验证驱动分派，缺此路径的端到端证据。[数据库逐项记录](Go-数据库验收.md) |
| 记忆存储 | 本地内存、FAISS、Qdrant、Chroma、Milvus、Weaviate、OpenSearch 和 Marqo 已通过实际读写及检索，Chroma 和 Qdrant 各覆盖两种运行方式。Marqo 使用 Vespa 和本机 BGE 中文嵌入模型 | Azure AI Search、Pinecone 和 Premium 缺可写的隔离测试资源和身份。[记忆逐项记录](Go-记忆存储验收.md) |
| 结果展示 | SQLite 的实际查询结果生成 CSV 和 Plotly 柱状图。浏览器 MES 流程核对了表格、CSV、中文答复和任务完成状态 | 示例 Mock 模型只用于可重复的协议回归。 |

## 本地复测命令

在仓库根目录执行。已安装的真实数据库和存储需要分别配置其端点，具体环境变量与断言见各项记录。

本轮使用 macOS arm64、Go 1.26.6 和 Node.js 26.7.0。核心模块 `vet` 和全包 `-race` 回归通过，Chrome 前端回归通过。对最终代码另外启用所有六个本地模型路由，带 `-race -count=1` 一次复跑模型协议、Responses 部分答复与三个角色的真实 HTTP 用例，全部通过，零跳过。

```sh
go -C src/doolla vet ./...
go -C src/doolla test -race -count=1 ./...
go test -race -count=1 ./...
go -C src/doolla generate ./web_components
node frontends/webcomponent/tests/run-browser-tests.mjs
```

用支持工具调用的 Ollama 模型复测角色权限、任务事件和实际 SQL 时，指定本地模型名和端点。该测试启动 Go HTTP 服务，向只读 SQLite 查询总额，分别断言普通访客与获准角色看到的界面事件、工具上下文和答复。

```sh
DOOLLA_TEST_AGENT_MODEL='本机已安装的模型名' \
DOOLLA_TEST_AGENT_URL='http://127.0.0.1:11434' \
go -C src/doolla test -race -count=1 -run '^TestLiveAgentUIFeatures$' -v .
```
