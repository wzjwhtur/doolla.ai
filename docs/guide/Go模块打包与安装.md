# Go 模块打包与安装

在仓库根目录执行 `./scripts/package-go.sh`，产物为 `dist/doolla-source.tar.gz`。将压缩包复制到目标机器即可。包内含核心 SDK、可选驱动模块和预编译的聊天网页。编译需要 Go 工具链以及依赖缓存或可访问的 Go 模块代理。核心 SDK 需要 Go 1.25+，构建可选数据库驱动需要 Go 1.26+。SQLite 功能需要开启 CGO 并安装 C 编译器。

## 在另一台机器上解包

将下面的压缩包路径换成实际位置，选择专用安装目录。接入 SDK 的项目会通过本地路径引用该目录。

```sh
archive=/path/to/doolla-source.tar.gz
doolla_dir="$HOME/.local/share/doolla"
mkdir -p "$doolla_dir"
tar -xzf "$archive" -C "$doolla_dir"
```

## 在已有 Go 项目中使用 SDK

在业务项目的 `go.mod` 所在目录执行以下命令。模块版本 `v0.0.0` 是本地占位符，`replace` 指向解包后的 SDK。

```sh
go mod edit -require=doolla.ai/sdk@v0.0.0
go mod edit "-replace=doolla.ai/sdk=$doolla_dir/src/doolla"
```

在业务代码中使用 `import "doolla.ai/sdk"`，再运行 `go mod tidy` 和 `go build ./...`。如果业务代码直接导入可选的数据库驱动模块，还需在业务项目增加该子模块自己的本地映射，并选择相应驱动标签。例如 PostgreSQL 使用以下配置：

```sh
go mod edit -require=doolla.ai/sdk/integrations/database/drivers@v0.0.0
go mod edit "-replace=doolla.ai/sdk/integrations/database/drivers=$doolla_dir/src/doolla/integrations/database/drivers"
# 在代码中加入相应驱动的匿名导入后运行
go mod tidy
go build -tags doolla_postgres ./...
```

## 安装服务命令

在解包时使用的终端运行。命令安装到 `$HOME/.local/bin`，网页资源已内嵌。

```sh
mkdir -p "$HOME/.local/bin"
GOBIN="$HOME/.local/bin" go -C "$doolla_dir/src/doolla" install ./cmd/doolla
```

通用服务通过启动参数选择数据源和模型。例如，使用已有的 SQLite 数据库和本机 Ollama：

```sh
"$HOME/.local/bin/doolla" -listen 127.0.0.1:8019 \
  -database sqlite -dsn /absolute/path/to/warehouse.sqlite \
  -provider ollama -model-url http://127.0.0.1:11434 -model qwen2.5:1.5b
```

将路径和模型换成实际配置，打开 `http://127.0.0.1:8019/`。通用数据库服务也可以独立安装，例如 `GOBIN="$HOME/.local/bin" go -C "$doolla_dir/src/doolla/integrations/database/drivers" install -tags doolla_postgres ./cmd/doolla-db`。

## 发布给其他使用者

已安装的服务也可以用 `doolla -config ./doolla.yaml` 启动。YAML、JSON 和环境变量的用法见[启动配置参考](配置参考.md#使用启动配置文件)。

在仓库根目录运行发布脚本，传入新的三段式版本号。Go 缓存按模块路径与版本号查找包；项目沿用上游模块路径，版本号必须与已有 Python 标签错开。脚本会检查本地已有的上游标签。先将 shell 变量 `version` 设为本次发布的版本号，省略开头的 `v`。以下命令使用同一个变量。脚本按版本写入 `dist/releases/` 下的子目录，已有版本会拒绝覆盖。

```sh
./scripts/release-go.sh "${version:?请先设置待发布版本号}"
```

| 使用者 | 分发文件 | 安装方式 |
| --- | --- | --- |
| 运行服务的 Mac 用户 | `doolla-v${version}-macos-arm64.pkg` 和 `SHA256SUMS` | 双击安装器，或运行下方的 `installer` 命令。安装通用命令 `/usr/local/bin/doolla`。 |
| 引用 SDK 的 Go 开发者 | `doolla-module-proxy-v${version}.tar.gz` | 发布到团队 Go 模块代理，然后执行 `go get`。模块包包含 SDK、可选数据库驱动和本地 Chroma 适配器。 |

### 分发服务安装器

脚本在 macOS 上为本机架构构建安装器。在制品库或团队下载页发布匹配目标架构的 `.pkg` 及校验值。接收方可核对文件后安装，不需要 Go 编译器。安装完成后按需启动服务。

```sh
sudo installer -pkg "./doolla-v${version}-macos-arm64.pkg" -target /
/usr/local/bin/doolla -listen 127.0.0.1:8019 \
  -database sqlite -dsn /absolute/path/to/warehouse.sqlite \
  -provider ollama -model-url http://127.0.0.1:11434 -model qwen2.5:1.5b
```

默认产出未签名的安装器。正式对外分发时，在具备证书的 Mac 上将 `MAC_APP_IDENTITY` 设为团队的 Developer ID Application 身份，将 `MAC_INSTALLER_IDENTITY` 设为 Developer ID Installer 身份，再运行发布脚本。脚本会分别签署程序和安装器。随后用 `xcrun notarytool submit ... --wait` 提交 Apple 公证，确认 Accepted 后执行 `xcrun stapler staple`、`xcrun stapler validate`。装订后请重新生成最终文件的 SHA-256，再分发。[Apple Developer ID 分发说明](https://developer.apple.com/developer-id/)

### 发布 SDK，让业务方用 go get 安装

1. 解压 `doolla-module-proxy-v${version}.tar.gz`。将 `proxy/` 的内容按原路径发布到团队 Go 代理的静态文件根目录。仓库管理员应合并各模块的 `@v/list`，保留历史版本的 `.info`、`.mod` 和 `.zip`。这些文件位于模块代理中，无需向上游 GitHub 仓库推送分支。
2. 在业务项目的终端将 `GOPROXY` 指向团队代理。以下地址是示例，实际代理应同时提供或转发项目所需的第三方依赖。
3. 用准确版本安装。以下配置仅作用于当前终端。同一模块名和版本号应始终对应相同内容，后续改动请递增版本。

```sh
export GOPROXY=https://go-proxy.example.com
export GONOPROXY=none
export GONOSUMDB=doolla.ai/sdk
go get doolla.ai/sdk@v${version}
```

如需在开发机上使用版本化命令，可以执行：

```sh
go install doolla.ai/sdk/cmd/doolla@v${version}
go mod download doolla.ai/sdk/integrations/database/drivers@v${version}
go install -tags doolla_postgres doolla.ai/sdk/integrations/database/drivers/cmd/doolla-db@v${version}
```

在本机验证尚未上传的 SDK 时，可将 `GOPROXY` 的第一项设为解包目录的 `file://` 绝对地址，第二项使用已有的依赖代理。脚本只在打包副本里将两个子模块对 SDK 的依赖固定到同一版本。[Go 模块代理协议](https://go.dev/ref/mod#goproxy-protocol)
