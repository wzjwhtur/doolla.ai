# 用 SemaCortex 的配置启动 doolla.ai

[doolla.example.yaml](doolla.example.yaml) 对应上游 `master` 提交 `3bdb085` 中的单实例配置。数值取自仓库模板和启动代码，实际部署可以通过私有环境覆盖。模板启用 SQL Server、OpenAI 兼容对话模型、BGE 嵌入和 Milvus。[上游环境模板](https://git.semaweaver.cn/gongzhitupu/semacortex/src/commit/3bdb085fc4f7ebe4ee8f96315338debd2a4bc0e7/.env.example) · [模型与数据库初始化](https://git.semaweaver.cn/gongzhitupu/semacortex/src/commit/3bdb085fc4f7ebe4ee8f96315338debd2a4bc0e7/agent_factory.py)

## 运行

在仓库根目录执行。源代码构建需要 Go 1.26+，安装包用户需要安装带 SQL Server 驱动的 `doolla-db`。复制配置后，确认三个服务地址（对话模型、嵌入模型、Milvus）从 Doolla 进程所在机器可达。在容器内运行时，`127.0.0.1` 指向该容器。

```sh
cp -n examples/semacortex/doolla.example.yaml ./doolla.semacortex.yaml
go -C src/doolla/integrations/database/drivers build -tags doolla_mssql -o "$PWD/doolla-db" ./cmd/doolla-db
```

已有部署请沿用当前配置中的 `workdir`、`history-dir` 和 `memory-collection`。迁移数据后，验证历史会话和记忆召回，再切换到新位置。

配置中的对话模型默认请求 Qwen/Qwen3.8-27B。若要在本机推理，改用本地 OpenAI 兼容服务的 `model-url` 和 `model`，并将 `max-output-tokens` 调整到该模型支持的范围。嵌入服务应提供 `POST /v1/embeddings`，示例假设模型输出 1024 维向量。

在启动终端或部署系统中提供以下变量。SQL Server 连接串的格式示例为 `sqlserver://readonly:URL_ENCODED_PASSWORD@db.example:1433?database=warehouse`，用户名和密码中的特殊字符需按 URL 编码。

| 环境变量 | 用途 |
| --- | --- |
| `DOOLLA_DSN` | 业务库连接串，对应上游 `CORTEX_MSSQL_CONN_STR` 的连接信息。使用 Go 驱动的 DSN 格式 |
| `DOOLLA_API_KEY` | 对话模型密钥，对应 `CORTEX_LLM_API_KEY` |
| `DOOLLA_EMBED_API_KEY` | 嵌入服务启用鉴权时填写，对应 `CORTEX_EMBED_API_KEY` |
| `DOOLLA_MEMORY_API_KEY` | Milvus 启用鉴权时填写，对应 `CORTEX_MILVUS_TOKEN` |

```sh
: "${DOOLLA_DSN:?请先设置业务库连接串}"
./doolla-db -config ./doolla.semacortex.yaml
```

打开 `http://127.0.0.1:8080/`。相对数据目录以启动进程的工作目录为基准。生产部署可改为绝对路径。命令行覆盖 YAML，YAML 覆盖对应环境变量；要让环境变量控制某项，请从 YAML 省略该键。

使用本机 Ollama 的 OpenAI 兼容接口时，也可以直接覆盖对话模型参数。以下命令假设本机端口为 54053，已安装支持工具调用的 `qwen2.5:1.5b`：

```sh
./doolla-db -config ./doolla.semacortex.yaml \
  -model-url http://127.0.0.1:54053/v1 -model qwen2.5:1.5b -max-output-tokens 512
```

上游向 `admin` 和 `user` 两组展示工具名称、参数、调用消息和错误。需要沿用该展示范围时，在 YAML 中启用 `ui-features`，路径指向随附的 [ui-features.json](ui-features.json)。这份配置仅控制界面展示。

## 映射依据

| SemaCortex 来源 | Go 配置 | 说明 |
| --- | --- | --- |
| `CORTEX_HOST`、`CORTEX_PORT` | `listen` | 上游启动脚本默认监听 `127.0.0.1:8080` |
| `CORTEX_LLM_BASE_URL`、`CORTEX_LLM_MODEL` | `model-url`、`model` | 使用 OpenAI Chat Completions 协议；`openai-legacy-max-tokens: true` 对齐上游请求的 `max_tokens` 字段 |
| `CORTEX_LLM_MAX_TOKENS`、`CORTEX_MAX_TOOL_ITERATIONS` | `max-output-tokens`、`max-tool-iterations` | 每次模型调用输出上限 40960，每轮问答最多执行 100 轮工具调用 |
| `CORTEX_DB_TYPE`、`CORTEX_DATA_DIR` | `database`、`workdir`、`history-dir` | 默认 SQL Server；Go 使用独立目录保存文件与对话 |
| `CORTEX_MILVUS_URI`、`CORTEX_MILVUS_PREFIX` | `memory-url`、`memory-collection` | 沿用宿主机端口 19535。上游按类型、模型和版本拆集合；Go 的一个集合同时存两类记忆，因此示例指定新的完整集合名并启用自动建集合 |
| `CORTEX_EMBED_BASE_URL`、`CORTEX_EMBED_MODEL` | `embed-url`、`embed-model`、`embed-dim` | 上游默认在进程内运行 BGE；Go 示例需要另行提供 HTTP 嵌入服务。切换模型时同步调整模型 ID、维数和集合名 |
| `CORTEX_CHAT_TEXT_MEMORY_LIMIT`、`CORTEX_TEXT_MEMORY_MIN_SIMILARITY` | `memory-text-limit: 150`、`memory-text-threshold: 0.70` | 沿用上游环境模板的值。仅使用上游代码默认值时，Milvus 的文本阈值为 0.50 |
| `CORTEX_CHAT_SQL_MEMORY_LIMIT`、`CORTEX_TOOL_MEMORY_MIN_SIMILARITY` | `memory-tool-limit: 100`、`memory-tool-threshold: 0.75` | 上游自动注入历史 SQL，Go 在调用检索工具时使用这两个默认值。0.75 取自上游 Milvus 适配器 |

上游结合余弦相似度和 BM25 召回，Go 的 Milvus 适配器使用向量内积。使用单位归一化的嵌入时内积等于余弦相似度；混合召回的排名仍有差异。迁移时可用同一批业务问题复核命中结果，再决定是否调整阈值。

上游的 Casdoor 登录、SpiceDB 授权、按用户或公开范围过滤记忆、独立检索服务令牌、SQL 自动记忆策略、训练任务、SQL 修复与结果预算，以及推理强度，都没有可直接照搬的 Go 启动键。共享环境需要单独实现相应的身份和数据访问策略。模型采样温度可通过 `temperature` 或 `DOOLLA_TEMPERATURE` 配置。Go 的管理员令牌有独立用途，另行设置 `DOOLLA_ADMIN_TOKEN`。

## 改接 Oracle

将构建标签改为 `doolla_oracle`、YAML 中的 `database` 改为 `oracle`，通过 `DOOLLA_DSN` 传入 Go Oracle 驱动的连接串，例如 `oracle://readonly:URL_ENCODED_PASSWORD@db.example:1521/ORCLPDB1`。为该实例指定独立的 `workdir`、`history-dir` 和 `memory-collection`；与 SQL Server 实例同时运行时另选监听端口。上游 Oracle Thick 客户端和分号分隔的 `CORTEX_ORACLE_CONN_STR` 属于其 Python 驱动配置。[Go 数据库驱动说明](../../src/doolla/integrations/database/README.md)
