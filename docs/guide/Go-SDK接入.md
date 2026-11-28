# 在 Go 应用中接入 Doolla

已有 Go 服务可以直接创建 Agent，并把它挂到自己的 HTTP 路由。下面从一个独立 Go 模块开始，用本机 Ollama 调用 `run_sql`，读取一个只读 SQLite 工单库。示例将一个服务令牌映射到一个分析员账号。

要把 SDK 或服务安装到另一台机器，可使用[Go 模块打包与安装](Go模块打包与安装.md)中的源码包。

## 1. 引用本地 SDK

准备 Go 1.25 或更新版本、C 编译器、`sqlite3`、`curl`、`openssl` 和 `jq`。先在 Doolla 仓库根目录运行以下命令。新目录用于存放示例程序、数据库和查询结果。

```sh
doolla_repo="$(pwd -P)"
sdk_demo_dir="$(mktemp -d)"
cd "$sdk_demo_dir"
go mod init example.com/doolla-demo
go mod edit -require=doolla.ai/sdk@v0.0.0
go mod edit "-replace=doolla.ai/sdk=$doolla_repo/src/doolla"
sqlite3 orders.sqlite 'CREATE TABLE orders (id INTEGER PRIMARY KEY); INSERT INTO orders VALUES (101),(102),(103);'
```

`replace` 要指向包含 SDK 的 `go.mod` 的 `src/doolla` 目录。在已有项目中，在调用方的 `go.mod` 中设置相同的 `require` 和 `replace` 即可。

## 2. 将模型、数据库和身份接到 HTTP 服务

在刚创建的目录中将以下代码保存为 `main.go`。程序监听本机 18091 端口。SQL 工具只对 `analyst` 组开放，非空结果会写入 `results` 下的用户目录。

```go
package main

import (
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	doolla "doolla.ai/sdk"
)

func run() error {
	token := os.Getenv("SDK_DEMO_TOKEN")
	if token == "" {
		return errors.New("请设置 SDK_DEMO_TOKEN")
	}
	model, err := doolla.NewModel(doolla.ModelConfig{
		Provider: "ollama", BaseURL: os.Getenv("OLLAMA_HOST"), Model: os.Getenv("OLLAMA_MODEL"),
	})
	if err != nil {
		return err
	}
	db, err := doolla.OpenSQLite("orders.sqlite")
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return err
	}

	query := doolla.SQLTool{DB: db, Artifacts: doolla.Artifacts{Directory: "results"}}
	agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{
		SystemPrompt: "工单表是 orders，主键是 id。回答工单数量前请使用 run_sql 查询 SQLite，再用中文回复。",
	}, doolla.Registration{Tool: query, Groups: []string{"analyst"}})
	if err != nil {
		return err
	}
	resolve := func(_ http.ResponseWriter, r *http.Request) (doolla.User, error) {
		credential, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(credential), []byte(token)) != 1 {
			return doolla.User{}, errors.New("凭据无效")
		}
		return doolla.User{ID: "orders-reader", Groups: []string{"analyst"}}, nil
	}
	server := &http.Server{
		Addr:              "127.0.0.1:18091",
		Handler:           doolla.NewServer(agent, resolve),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return server.ListenAndServe()
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
```

## 3. 发起问数

以下命令在同一个终端执行。`OLLAMA_HOST` 是运行推理的地址。普通 Ollama 安装通常使用 `http://127.0.0.1:11434`，本仓库的 MES 演示使用 `http://127.0.0.1:54053`。把 `OLLAMA_HOST` 和模型名改成本机实际值，`/api/tags` 的输出应包含所选模型。

```sh
export OLLAMA_HOST=http://127.0.0.1:11434
export OLLAMA_MODEL=qwen2.5:1.5b
curl -fsS "$OLLAMA_HOST/api/tags" | jq -r '.models[].name'
export SDK_DEMO_TOKEN="$(openssl rand -hex 32)"
go mod tidy
CGO_ENABLED=1 go build -o orders-agent .
./orders-agent &
sdk_pid=$!
```

服务启动后，先检查身份解析，再提交查询。第一个请求预期显示 `401`。第二个请求的结果表应有一行，统计值为 `3`，并返回模型的中文答复。

```sh
api=http://127.0.0.1:18091/api/doolla/v2/chat_poll
curl -sS -o /dev/null -w '%{http_code}\n' -H 'Content-Type: application/json' \
  -d '{"message":"/help"}' "$api"

curl -fsS -H 'Content-Type: application/json' -H "Authorization: Bearer $SDK_DEMO_TOKEN" \
  -d '{"message":"请查询 orders 表一共有多少条工单。"}' "$api" > answer.json
jq '.chunks[].rich | select(.type == "dataframe") | .data.data' answer.json
jq -r '.chunks[].rich | select(.type == "text") | .data.content' answer.json
```

结束本次独立示例后，在同一终端执行 `kill "$sdk_pid"`。结果表和 CSV 文件仍保存在 `$sdk_demo_dir`。

## 4. 接入已有应用

- **复用登录态。** 将示例的单一令牌解析器换成已有应用的会话或令牌校验逻辑。验证通过后，把应用中稳定、唯一的用户 ID 和服务端查到的角色写入 `doolla.User`。同一示例令牌的调用方共享 `orders-reader` 的身份、会话和结果文件。SDK 根据用户 ID 和会话 ID 读取历史。
- **配置数据权限。** `Registration.Groups` 同时控制工具的发现与执行。空列表向所有已解析用户开放。默认的 `DatabaseRunner` 使用传入的同一数据库连接池，示例的 `OpenSQLite` 约束写操作。需要限制不同用户能读哪些行时，将策略放在数据库权限或自定义 `SQLTool.Runner` 中。其 `RunSQL(ctx, user, query)` 可根据已认证用户选择数据源或执行授权查询。[SQL 工具接口](../../src/doolla/sql.go)
- **保留历史、复用查询结果。** 需要跨进程重启保留对话时，将 `AgentConfig.Conversations` 设为 `doolla.FileConversations{Directory: "history"}`，并将该目录放在持久卷。需要绘图时，用同一个 `Artifacts` 实例注册 `doolla.VisualizationTool{Artifacts: files}`，即可读取 SQL 工具落盘的 CSV。[会话存储](../../src/doolla/conversations.go) · [可视化工具](../../src/doolla/plotly.go)
- **替换模型或工具。** 通过 `doolla.ModelConfig` 选择适配器。业务工具实现 `doolla.Tool` 的 `Spec` 和 `Run` 后，通过 `doolla.Registration` 注册到 Agent。已有 HTTP 服务可以挂载 `doolla.NewServer` 返回的 `http.Handler`。需要跨域嵌入时，用 `NewServerWithOptions` 设置 `AllowedOrigins`。[SDK 入口](../../src/doolla/agent.go) · [模型配置](../../src/doolla/model_config.go)

继续阅读 [使用指南](使用指南.md) 和 [HTTP 与网页接入](HTTP与网页接入.md)。

## 企业租户记忆隔离

企业服务可以将经过身份认证的租户和用户交给 `TenantUser`，并将各租户的存储登记到 `NewTenantMemoryStore`。下面的装配函数接受宿主已有的登录验证函数。该函数应验证令牌的签名、签发方和受众，并从已验证的声明读取租户、主体和权限组。聊天消息的 `metadata` 由客户端填写，不能作为租户身份来源。

```go
func tenantChat(
    model doolla.Model,
    sqlTool doolla.SQLTool,
    stores map[string]doolla.MemoryStore,
    authenticate func(*http.Request) (tenant, subject string, groups []string, err error),
) (http.Handler, error) {
    routed, err := doolla.NewTenantMemoryStore(stores)
    if err != nil {
        return nil, err
    }
    tools := append(doolla.MemoryTools(routed), doolla.Registration{Tool: sqlTool})
    agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{
        Memory: routed, ConfirmSQLMemory: true,
    }, tools...)
    if err != nil {
        return nil, err
    }
    return doolla.NewServer(agent, func(_ http.ResponseWriter, req *http.Request) (doolla.User, error) {
        tenant, subject, groups, err := authenticate(req)
        if err != nil {
            return doolla.User{}, err
        }
        return doolla.TenantUser(tenant, doolla.User{ID: subject, Groups: groups})
    }), nil
}
```

`stores` 的键是身份系统中的租户 ID。每个值应指向独立的存储，例如两个 Milvus collection、两个 Pinecone namespace，或两个不同的 `NewLocalMemory` 实例。将同一个底层集合登记给两个租户会共享数据。由宿主管理各存储的打开和关闭。已有的单租户实例可继续使用原集合，新租户应显式指定其集合。共享历史集合中的归属需在迁移前单独核实。

路由覆盖 `Save`、`Search`、`Recent`、`Delete` 和 `Clear`。租户未知或未绑定时，服务拒绝访问。工具、自动召回和维护命令使用同一组存储。`TenantUser` 将租户和原始主体共同编码成 `User.ID`，会话、请求重放和结果文件因此能区分不同租户内的同名账号。原始主体保存在 `User.Metadata["tenant_subject"]`；已有自定义 SQLRunner 如按主体做数据权限映射，应同时读取 `tenant_id` 和 `tenant_subject`。离线管理可对已核验的主体调用 `routed.ForUser(user)` 获取其存储。
