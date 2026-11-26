# doolla.ai 示例

从仓库根目录执行以下命令。仓库根模块通过本地引用使用 `src/doolla` SDK。运行这里的 Go 示例需要 Go 1.26 或以上。涉及 SQLite 的示例需要 CGO 和 C 编译器。

| 场景 | 运行入口 |
| --- | --- |
| 离线体验聊天界面 | `go -C src/doolla run ./cmd/doolla -provider mock`，打开终端打印的地址 |
| 用本地模型查询 SQLite、导出 CSV 和绘图 | [中文使用指南](../docs/guide/使用指南.md) |
| 查询 MES 工单总数和不同工单号数量 | [MES Go 示例](./mes) 和[本地启动参数](../docs/guide/使用指南.md) |
| 沿用 SemaCortex 的模型、SQL Server、Milvus 和记忆召回设置 | [配置示例与启动说明](./semacortex/README.md) |
| 在 Go 服务中接入模型、数据库和已认证用户 | [可编译的 Go SDK 示例](../docs/guide/Go-SDK接入.md) |
| 将数据查询、图表和任务面板嵌入业务网页 | [HTTP 与网页接入](../docs/guide/HTTP与网页接入.md) |
| 在本机使用 Chroma 检索知识并回答问题 | [Chroma 与 Ollama 实例](../tests/chromalocal/agent_live_test.go) 和[复测环境](../docs/verification/Go-记忆存储验收.md) |

## 根据登录身份绑定工具参数

```sh
go run ./examples/transformargs
```

示例用内存 SQLite 创建南、北两个组织的工单。工具参数里填的是 `south`，已认证用户属于 `north`。注册表从用户身份绑定组织，工具通过参数化 SQL 得到北区的两条记录。缺少组织的用户会收到 `missing organization`。[示例源码](./transformargs/main.go)

## 比较两个本地模型的 SQL 工具调用

```sh
go run ./examples/compare -url http://127.0.0.1:54053 qwen2.5:1.5b qwen2.5:0.5b
```

将端点和模型名换为本机 Ollama 的实际配置。省略 `-url` 时使用 `http://127.0.0.1:11434`。示例让两个模型分别查询同一份 SQLite 样例数据，评估是否调用 `run_sql`、最终答案是否包含合计值 35，并将 HTML 和 CSV 写入终端打印的临时目录。模型得分取决于本次运行。[示例源码](./compare/main.go)
