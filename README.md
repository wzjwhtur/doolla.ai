# doolla.ai

## Go edition

This branch includes a Go implementation of Doolla 2.x with the existing chat web component embedded in the executable. The default demo uses the mock model. Run it from the repository root and open the address printed in the terminal.

```sh
go -C src/doolla run ./cmd/doolla
```

For natural-language queries, choose a model and an existing database. This example reads the API key from `OPENAI_API_KEY` and opens SQLite in read-only mode.

```sh
go -C src/doolla run ./cmd/doolla -provider openai -model gpt-4o-mini -dsn /path/to/warehouse.sqlite
```

新版 Agent、HTTP 服务和集成位于 `src/doolla/`。上面的命令从仓库根目录执行，SQLite 文件可使用绝对路径，也可使用相对于 `src/doolla/` 的路径。模型、数据库和记忆存储配置见 [Go 运行指南](GO.md)。

首次用本地模型问数，请从[中文使用指南](docs/guide/使用指南.md)开始。修改服务源码请看 [doolla.ai Go 开发指南](docs/guide/Go开发指南.md)。在业务系统中集成时，可查阅 [HTTP 与网页接入](docs/guide/HTTP与网页接入.md)、[Go SDK 接入](docs/guide/Go-SDK接入.md)和[配置参考](docs/guide/配置参考.md)。

使用本地 Ollama 和 MES 数据源运行演示、查看真实服务逐项结果及浏览器截图，请参阅[中文验收报告](docs/verification/Go-功能与真实服务验收.md)。

模型对比、工具参数绑定和 MES 问数的运行命令见 [Go 示例索引](examples/README.md)。

模块名为 `doolla.ai/sdk`，支持本地源码和团队 Go 代理两种安装方式。[打包与安装](docs/guide/Go模块打包与安装.md) · [更名与部署迁移](docs/guide/更名与部署迁移.md)
