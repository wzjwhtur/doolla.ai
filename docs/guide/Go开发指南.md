# doolla.ai Go 开发指南

这份指南面向修改 Doolla 2.x Go 服务的开发者。从仓库根目录执行下列命令，可以先跑通一个独立的聊天实例，再按功能找到要改的代码和要跑的测试。如果只想把 Doolla 嵌入已有 Go 应用，可以直接使用带身份校验的完整 SDK 示例。[Go SDK 接入](Go-SDK接入.md)

## 1. 跑通本地开发环境

整仓开发使用 Go 1.26 或更新版本。核心 SDK 的最低版本是 Go 1.25；仓库根模块和可选数据库驱动模块要求 Go 1.26。使用 SQLite 或嵌入式 Qdrant 时，开启 CGO 并安装 C 编译器。聊天页面已嵌入二进制，修改 TypeScript 再准备 Node.js 和 npm。

```sh
go version
go env CGO_ENABLED CC
dev_dir="$(mktemp -d)"
go -C src/doolla build -mod=readonly -o "$dev_dir/doolla" ./cmd/doolla
"$dev_dir/doolla" -provider mock -memory local -dsn= \
  -workdir "$dev_dir/work" -listen 127.0.0.1:18089
```

保留当前终端，在另一个终端请求开发实例。这里选用独立端口，方便与已有服务并行调试。

```sh
curl -fsS http://127.0.0.1:18089/health
curl -fsS http://127.0.0.1:18089/api/doolla/v2/chat_poll \
  -H 'Content-Type: application/json' -d '{"message":"/help"}'
```

健康接口应返回 `status: healthy`；第二个请求应返回包含 `conversation_id`、`request_id` 和 `chunks` 的 JSON。`/help` 走内置工作流，可以独立验证 HTTP 和 Agent 的连接。验证模型生成 SQL 时，换用支持工具调用的模型和有数据的数据库，并核对返回的行值。按 Ctrl+C 停止本次开发实例。[真实数据库问数步骤](使用指南.md)

## 2. 按修改目标定位代码

| 要修改的功能 | 从哪里开始 |
| --- | --- |
| 启动、配置、依赖装配 | [CLI 入口](../../src/doolla/cli/main.go)创建模型、记忆存储、工具和 HTTP 服务。[配置加载](../../src/doolla/cli/config.go)将 YAML 或 JSON 的键关联到已注册的命令行参数 |
| 聊天编排、工具调用 | [Agent](../../src/doolla/agent.go)管理对话轮次；[扩展点](../../src/doolla/extensions.go)负责提示词、模型调用和中间件；[工具注册表](../../src/doolla/registry.go)负责工具发现、参数校验和执行授权 |
| 模型、SQL 与记忆适配 | [模型工厂](../../src/doolla/model_config.go)、[SQL 工具和执行器](../../src/doolla/sql.go)、[数据库驱动](../../src/doolla/integrations/database)、[记忆接口与工具](../../src/doolla/memory.go)、[记忆后端](../../src/doolla/integrations/memory) |
| 对话历史、查询产物 | [ConversationStore](../../src/doolla/conversations.go)按用户和会话保存消息；[Artifacts](../../src/doolla/artifacts.go)保存查询结果文件，供图表等工具继续使用 |
| HTTP 协议与浏览器组件 | [服务路由](../../src/doolla/server.go)、[请求重放](../../src/doolla/server_replay.go)、[OpenAPI 定义](../../src/doolla/web_components/openapi.json)和[前端 TypeScript](../../frontends/webcomponent/src) |
| 评估、示例和验收 | [评估包](../../src/doolla/evaluation)、[可运行示例](../../examples/README.md)、[仓库测试](../../tests)和[真实服务验收记录](../verification/Go-功能与真实服务验收.md) |

核心模块的导入路径为 `doolla.ai/sdk`，模块根在 `src/doolla/`。仓库根模块通过本地 `replace` 引用 SDK；可选 SQL 驱动和原生 Chroma 各有自己的 `go.mod`。新增依赖时，进入实际导入该依赖的模块。用 `rg --files -g go.mod` 查看当前模块清单。[根模块](../../go.mod) · [核心模块](../../src/doolla/go.mod)

## 3. 沿一次问数请求调试

以“某产线今天完成了多少工单”为例，一条触发 SQL 的普通请求经过以下路径：

1. **HTTP 识别用户。** `chat_poll`、`chat_sse` 和 WebSocket 共用 `ChatRequest`。服务调用 `UserResolver`，处理请求重放，再将问题交给 `Agent.Chat`。追问沿用登录身份和 `conversation_id`；同一请求重试沿用 `request_id`。[传输协议及请求示例](HTTP与网页接入.md)
2. **Agent 准备上下文。** Agent 获取当前用户可见的工具，加载该用户的会话历史。默认提示词构建流程使用当前问题检索文本记忆，把匹配内容加入系统提示词。成功 SQL 示例通过 `search_saved_correct_tool_uses` 工具检索，两条路径有各自的条数与相似度阈值。[记忆召回配置](配置参考.md#记忆召回)
3. **模型选择工具。** 当模型返回 `run_sql` 调用时，注册表核对访问组和参数，`SQLTool` 将已认证用户及 SQL 交给 `SQLRunner.RunSQL`。默认 `DatabaseRunner` 使用 CLI 打开的数据库连接池。工具可把查询结果作为 `dataframe` 发送给客户端，并将非空结果写成 CSV。
4. **Agent 完成回答。** 工具结果加入消息列表，供模型继续调用工具或生成最终回答。正常结束且启用自动保存时，Agent 保存会话。前端按 `rich.type` 渲染组件；核对查询数值请读取 `dataframe` 的 `rich.data.data`。轮询一次返回全部组件，SSE 逐条推送并以 `[DONE]` 收尾。

定位错误时先看最后走通的步骤。网页能打开但没有 SQL 工具，先查 CLI 是否取得 DSN、驱动是否编入，以及当前用户能否发现工具。SQL 能执行但召回无结果，分别检查嵌入端点的实际输出维数、Milvus 集合维数、记录数和匹配阈值。HTTP 200 时也要检查组件中报告的工具错误。

**排查 SQL Server 表关联。** 配置 `database: mssql` 或 `sqlserver` 后，CLI 会注册 `inspect_mssql_schema`。例如传入 `{"tables":[{"schema":"dbo","name":"Pro_Mo"}]}`，可读取该账号可见的列、主键，以及任一端关联到此表的声明外键。`other_visible_foreign_keys_exist` 为真表示账号还能看到本批范围以外的外键。回答中的外键数量应注明检查范围。验证业务关联时，还要看连接查询是否有匹配行。

排查字段错误时，先看同一次提问中首次使用这张表的工具记录。对于可识别的简单 `SELECT`，若业务表尚未检查，服务会先调用 `inspect_mssql_schema`，再由模型依据实际列名重新提交数据查询。比如解释状态码时，即使工单主表已经检查过，也要单独查看新引入的字典表。工具返回表结构后，仍需等 `run_sql` 返回业务行才能给出统计。系统目录查询、复杂 SQL 和带供应商签名状态的模型回复沿用原有执行路径。

表结构结果的 `column_types` 提供列的 SQL 基础类型。查询字典时，先用 `SELECT TOP` 查看实际码值和标签。如果未加筛选的样例为空，可以直接报告原始码值。例如 MES 的 `Base_CommonType.TypeCode` 是 `int`，`CommonTypeCode` 和 `CommonTypeName` 是 `nvarchar`。

对于已经检查过的表，服务会核对可明确归属的简单输出列，包括联表时带表别名的字段。它也会检查单表查询中直接用于 `LIKE` 的列，以及整数列 `IN` 列表中的明显不兼容文本。当检查发现这类问题，表结构工具返回的 `deferred_sql` 会说明哪条 SQL 暂缓执行及原因，供模型重新生成查询。

同一次提问中，SQL Server 报告无效列后，工具会提示检查表结构。对于能确定字段所属表的简单查询，即使修改了表别名，再次引用同一无效列也会在执行前被拦下。下一次提问会重新读取需要的表结构。[首查调度](../../src/doolla/integrations/database/mssql_schema_first.go) · [元数据工具](../../src/doolla/integrations/database/mssql_schema.go) · [重试处理](../../src/doolla/integrations/database/mssql_retry.go)

## 4. 增加业务工具并接入权限

先运行已有组织权限示例：

```sh
go run ./examples/transformargs
```

示例中模型参数要求查询 `south`，服务端已认证身份属于 `north`。预期输出为 `north orders: 2`；未分配组织时返回 `unassigned user: missing organization`。可以在这个示例上验证自己的业务工具。[完整可运行代码](../../examples/transformargs/main.go)

1. 实现 `Tool.Spec() ToolSpec`，声明稳定的工具名、描述和 JSON 参数结构。实现 `Tool.Run(ctx, user, args)`，返回供模型读取的 `ToolResult.Content`，需要网页展示时再填 `ToolResult.Component`。
2. 通过 `doolla.Registration{Tool: tool, Groups: []string{"analyst"}}` 注册，作为参数传给 `NewConfiguredAgent`。组列表为空会向所有已解析用户开放。注册表在模型发现工具和执行调用时都会检查组权限。
3. 在业务服务入口用 `doolla.NewServer(agent, resolver)` 接入已有登录态。由 `resolver` 校验会话或令牌，从可信来源填入 `User.ID`、`Groups` 和组织信息。请求体中的 `metadata` 由调用者提供。对于固定形状的业务查询，可通过 `ToolRegistry.TransformArgs` 从服务端身份绑定组织，再用参数化 SQL 执行。
4. 行级授权放在数据库策略或自定义 `SQLRunner`；记忆的可见范围需要在写入和检索时落实。当前通用 CLI 的 SQL 工具共用一个连接池，Milvus 适配器的搜索条件包含记忆类型和工具名。多租户部署还需实现租户隔离，包括会自动进入提示词的文本记忆。[SQLRunner 接口](../../src/doolla/sql.go) · [Milvus 查询实现](../../src/doolla/integrations/memory/milvus.go)

## 5. 修改适配器、配置或网页

调试 SQL Server 与 Milvus 的组合时，可按 SemaCortex 示例构建 `doolla-db`，通过 `DOOLLA_DSN` 注入业务库连接串，加载自己的 YAML 副本。使用独立的开发集合和 `history-dir`；切换嵌入模型时同步检查集合维数。配置文件内的相对路径按进程工作目录解析，使用 `go -C` 时可给 `-config` 传绝对路径。[SemaCortex 配置与启动步骤](../../examples/semacortex/README.md)

| 改动 | 实施与验证 |
| --- | --- |
| 增加模型协议 | 实现 `Model.Reply`，需要流式支持时实现 `StreamingModel.Stream`。在 `NewModel` 中接入工厂，核对普通回复、流式回复、工具调用的 ID 和参数，以及输出截断。[协议测试](../../tests/_whitebox/sdk/model_protocol_test.go) |
| 接入 SQL 驱动 | 在可选驱动模块用构建标签注册 `database/sql` 驱动，再在 `database.Open` 中映射后端名。以 SQL Server 为例，使用 `go -C src/doolla/integrations/database/drivers build -mod=readonly -tags doolla_mssql -o "$dev_dir/doolla-db" ./cmd/doolla-db`。配置 DSN 后，CLI 在开始监听前执行 `PingContext`。[驱动接入与 DSN](../../src/doolla/integrations/database/README.md) |
| 接入记忆后端 | 按 `MemoryStore` 的保存、搜索、近期记录、删除和清理契约实现，在 `integrations/memory.Open` 中选择适配器。用独立集合验证文本与工具记忆可分别写入、检索和清理；使用外部嵌入时核对模型、维数和相似度度量。[记忆后端与实测入口](../../src/doolla/integrations/memory/README.md) |
| 增加启动参数 | 在 CLI 注册 flag，向实际使用方传递解析结果，补充环境变量映射、默认值和边界校验。同时测试“显式命令行 > 配置文件 > 对应环境变量 > 默认值”，包括显式传入 `false` 或 `0` 的情况。[配置测试](../../tests/_whitebox/cli/config_test.go) · [配置参考](配置参考.md) |
| 修改聊天组件或响应结构 | 在 `frontends/webcomponent/src/` 修改 TypeScript；协议变化同步更新 OpenAPI 和传输测试。使用 CI 所用的 Node.js 22，运行 `go -C src/doolla generate ./web_components` 重新生成压缩资源，再编译 Go 服务。前端回归命令为 `node frontends/webcomponent/tests/run-browser-tests.mjs`。[嵌入资源](../../src/doolla/web_components/assets.go) · [浏览器测试说明](../../frontends/webcomponent/TEST_README.md) |

## 6. 验证和打包

从仓库根目录运行核心检查：

```sh
go -C src/doolla vet -mod=readonly ./...
go -C src/doolla test -mod=readonly -race -count=1 ./...
go test -mod=readonly -race -count=1 ./...
```

核心模块测试会执行链接到包目录的 `tests/_whitebox/` 同包测试。根模块测试覆盖公开 API、示例和默认集成；嵌套模块需要单独构建或测试。修改驱动时还应启用对应构建标签。CI 的模块、前端与标签组合见 [Go 工作流](../../.github/workflows/go.yml)。

真实服务用例通过各测试约定的环境变量启用。以 Milvus 为例，指向允许创建和删除临时集合的测试实例后执行：

```sh
DOOLLA_TEST_MILVUS_URL=http://127.0.0.1:19535 \
  go -C src/doolla test -mod=readonly -count=1 -v -run '^TestLiveMilvus$' ./integrations/memory
```

检查指定用例实际显示 `PASS`；缺少端点时用例可能显示 `SKIP`。涉及问数主流程的变更，还应使用已知结果的 SQL 验证网页或 API 返回的行值。改动会话时，用同一身份追问；改动授权时，分别验证允许和拒绝的用户。[数据库实测](../verification/Go-数据库验收.md) · [模型实测](../verification/Go-本地模型验收.md) · [记忆实测](../verification/Go-记忆存储验收.md)

需要交付可安装产物时，`scripts/package-go.sh` 生成源码压缩包，`scripts/release-go.sh` 按指定版本生成 Go 模块代理制品，并在 macOS 上构建服务安装包。业务方使用 SQL Server 等可选驱动时，另行提供编入相应标签的 `doolla-db`。[打包与安装](Go模块打包与安装.md)

## 迁移 Python 扩展

| Python 接入点 | Go 接入点与调用时机 |
| --- | --- |
| `after_message` | 在 `AgentConfig.Hooks` 中注册 `AfterConversation` 以取得本轮结束时的完整 `Conversation`，包含消息历史、用户与会话标识。只处理模型最终消息的已有代码可继续使用 `AfterMessage` |
| `after_tool` | `AfterTool` 可改写成功结果。若也需要处理执行失败、校验失败或找不到工具，使用 `AfterToolResult`，返回 `nil` 保留原结果。后续模型请求读取处理后的结果 |
| 用户上下文及模型中间件 | `AgentConfig.EnhanceUserMessages` 处理本轮模型输入。`LLMMiddlewares` 的 `BeforeRequest` 和 `AfterResponse` 在各阶段按注册顺序调用。可逐请求修改 `SystemPrompt`、`Temperature`、`MaxTokens` 和 `Stream`。自定义模型实现 `RequestModel` 或 `StreamingRequestModel` 后可取得完整请求。自定义工具通过 `doolla.CurrentTurn(ctx)` 获取用户、会话、记忆存储和观测服务 |
| 指标和追踪 | 实现 `doolla.ObservabilityProvider` 的 `CreateSpan`、`EndSpan`、`RecordMetric`，赋给 `AgentConfig.Observability`。从 `CreateSpan` 返回子上下文可保留跨模型和工具调用的父子关系。批量评估可通过 `evaluation.WithObservability` 传入同一实现 |
| 组件状态 | `ComponentManager.Emit` 按组件 ID 判断新增、更新或替换。`Update` 修改单个字段，`Replace` 支持更换 ID，`Remove` 删除子树，`Add` 接受位置参数。将返回的 `ComponentChange` 编码为 JSON 即得到前端变更消息 |
| 工具发现与可替换能力 | `ToolRegistry.ListTools`、`GetTool` 用于服务端检查；对用户展示时使用按组过滤的 `Schemas`。`VisualizationTool.WithGenerator` 可接入图表生成器，`PythonToolsWithRunner` 可注入能返回标准输出、标准错误和退出码的进程执行器 |

工具回调、失败恢复、服务端追踪以及 Python 组件协议都有可执行示例。[生命周期与追踪测试](../../tests/lifecycle_observability_test.go) · [自定义工具上下文测试](../../tests/tool_context_test.go) · [组件协议对照测试](../../tests/component_parity_test.go) · [替代验收记录](../verification/Go实现兼容性验收.md)

### 问数结果与记忆写入确认

标准服务启用 `AgentConfig.VerifyToolAnswers`。自行创建 Agent 的 SDK 调用方可启用同一选项。查询业务数据时，Agent 使用本轮 `run_sql` 返回的行数元数据确认已取得结果，零行也属于有效结果。仅检查表结构时，Agent 会引导模型继续查询。最终仍未取得结果时，用户界面和会话历史都会显示“本轮未取得查询结果”。

数值型单行单列查询的最终答复直接使用结果中的列名和原值。比例按数据库返回的尺度展示，例如返回 0.98 时显示 0.98。多行或多列明细请查看结果表。对“本月良品率？”这样的业务指标提问，模型给出的百分数需要本轮查询支持。模型可以先询问工厂、产线或统计周期。

记忆写入沿用工具的用户组授权。管理员可以发送“请记住：质检代码是 AZ991”，冒号后的文本将作为原文交给 `save_text_memory`。写入工具成功返回 `memory_id` 后才确认完成。普通浏览器用户可使用已授权的召回功能。例如“记住的质检代码是什么？”会走读取流程；“把质检代码存进长期记忆”需要写权限。缺少本轮写入回执时，服务会提示尚未确认保存。

标准服务对“记住的质检代码是什么？”这类明确的文本记忆读取提取主题，按已配置的相似度阈值检索，向用户展示最相关的一条原文。低于阈值时提示未找到匹配，后端出错时提示检索失败。一般问答仍由模型结合召回上下文作答。SDK 使用自定义系统提示或模型中间件时，可通过 `AgentConfig.DirectTextRecall` 显式选择直接展示；自定义提示构建、历史过滤和消息增强仍按调用方策略执行。
