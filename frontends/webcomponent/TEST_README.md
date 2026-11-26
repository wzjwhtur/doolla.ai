# 网页组件测试

## Chrome 自动回归

从仓库根目录执行以下命令。测试使用本机 Chrome 检查 SSE、WebSocket、数据表格和任务跟踪。可以通过环境变量 `CHROME_BIN` 指定 Chrome 可执行文件。

```sh
npm ci --prefix src/doolla/web_components
npm test --prefix frontends/webcomponent
```

## 手动检查组件

在仓库根目录运行 `go run ./tests/webcomponent`，打开终端打印的地址。默认监听 `127.0.0.1:5555`。测试服务使用 Go 聊天 API 和内嵌的网页组件，样例数据随程序编译，可离线运行。

在页面上选择 **Rapid** 或 **Realistic**，点击 **Run Comprehensive Test**。两种模式的事件顺序相同，显示速度不同。左栏记录接收到的组件类型和更新次数。检查聊天区的表格、图表和状态卡，再点击 **Single Button** 查看服务返回的通知与卡片。

[测试服务、页面和样例数据](../../tests/webcomponent/)
