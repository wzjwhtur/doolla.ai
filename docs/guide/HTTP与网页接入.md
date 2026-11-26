# 将 Doolla 接入后端或业务网页

已有聊天界面时，可以直接调用 Go 服务的聊天 API。要在现有网页显示问答、表格、图表和任务状态，可以加载服务自带的 `<doolla-chat>` 组件。示例使用已启动的服务。普通 CLI 默认监听 `http://127.0.0.1:8000`，本次 MES 演示监听 `http://127.0.0.1:56707`。

## 从后端发起一轮问答

在装有 `curl`、`jq` 和 `uuidgen` 的终端运行以下命令。示例查询 MES 演示里的工单统计。接自己的数据库时，替换 `BASE`、问题中的表名和列名。仅检查协议时，可将问题设为 `/help`，读取返回的文本组件即可。

```sh
BASE=http://127.0.0.1:56707
WORK=$(mktemp -d)
curl -fsS "$BASE/health"

jq -n --arg rid "$(uuidgen)" \
  --arg q '请调用 run_sql，在 MES 工单表 mes.dbo.pro_mo 上执行 SELECT COUNT(*) AS total_records, COUNT(DISTINCT moid) AS distinct_moid FROM mes.dbo.pro_mo。请用中文告诉我工单总行数和不同工单号数量。' \
  '{message:$q,request_id:$rid}' > "$WORK/first.json"

curl -fsS -c "$WORK/cookies" -H 'Content-Type: application/json' \
  --data-binary @"$WORK/first.json" "$BASE/api/doolla/v2/chat_poll" \
  > "$WORK/first-response.json"

jq -e '.chunks[].rich | select(.type=="dataframe") | .data.data' "$WORK/first-response.json"
jq -r '.chunks[].rich | select(.type=="text") | .data.content' "$WORK/first-response.json"
```

第一次请求可以省略 `conversation_id`，服务会在响应中返回它。轮询端点会等本轮执行结束，一次返回 `chunks` 数组。`rich.type` 标识每个组件，`dataframe` 的列和行分别在 `rich.data.columns` 和 `rich.data.data`。只需文字的客户端也可读 `simple.text`，部分组件的 `simple` 为 `null`。本次示例得到一行两列数据，分别为 4,100 条工单记录和 4,100 个不同工单号。业务问数以数据表为准。若当前轮次没有数据表，先检查工具执行或重新发起明确的查询，再做涉及数值的追问。

**追问沿用会话和身份，每个新问题使用新的请求 ID。** 在同一终端执行以下命令。浏览器默认通过签名 Cookie 识别用户，curl 用同一份 Cookie 文件保留身份。

```sh
cid=$(jq -er 'select(any(.chunks[]; .rich.type=="dataframe")) | .conversation_id' "$WORK/first-response.json") &&
jq -n --arg cid "$cid" --arg rid "$(uuidgen)" \
  '{message:"请用一句话复述上一轮得到的两个数字。",conversation_id:$cid,request_id:$rid}' \
  > "$WORK/next.json" &&
curl -fsS -b "$WORK/cookies" -c "$WORK/cookies" \
  -H 'Content-Type: application/json' --data-binary @"$WORK/next.json" \
  "$BASE/api/doolla/v2/chat_poll" |
  jq -r '.chunks[].rich | select(.type=="text") | .data.content'
```

## 实时接收组件

**SSE** 使用 POST 发送相同的 JSON 请求。下面在默认的五分钟重放窗口内切换传输，读取第一轮已完成的结果。要开始另一轮流式提问，创建含新 `request_id` 的请求正文。

```sh
curl -fsSN -b "$WORK/cookies" -H 'Content-Type: application/json' \
  --data-binary @"$WORK/first.json" "$BASE/api/doolla/v2/chat_sse"
```

每个 SSE 事件的 `data` 是一个 JSON 组件块，最后一个事件是 `data: [DONE]`。浏览器自建客户端可用 `fetch` 发送 POST 并按空行解析事件。同源 Cookie 随请求发送，跨域调用时加 `credentials: 'include'`。

**WebSocket** 适合已有长连接客户端。在 Doolla 页面同源的浏览器控制台运行以下示例，HTTPS 页面自动使用 `wss:`。

```js
const url = new URL('/api/doolla/v2/chat_websocket', location.href)
url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
const socket = new WebSocket(url)
const requestId = crypto.randomUUID()
socket.onopen = () => socket.send(JSON.stringify({ message: '/help', request_id: requestId }))
socket.onmessage = ({ data }) => {
  const frame = JSON.parse(data)
  if (frame.request_id !== requestId) return
  if (frame.type === 'error') console.error(frame.data.status, frame.data.message)
  else if (frame.type === 'completion') console.log('本轮完成', frame.conversation_id)
  else console.log(frame.rich.type, frame.rich.data)
  if (frame.type === 'completion' || frame.type === 'error') socket.close()
}
```

普通响应帧和 SSE 使用同一组件结构。WebSocket 以 `type: "completion"`、`data.status: "done"` 结束传输，SSE 以 `[DONE]` 结束。两种传输在结束前都会发送 `rich.type: "turn_outcome"`，轮询响应将该组件放在 `chunks` 末尾。用 `rich.data.status` 判断业务结果，取值为 `succeeded`、`partial` 或 `failed`。`rich.data.code` 提供机器可读的原因，例如 `query_partial` 或 `processing_error`。同一请求的重放保留原始结果及标识。连接中断时可能收不到结束组件，按下方的重试说明处理。

## 将组件放进现有页面

业务页面和 Doolla 对外同源时，直接加入下列 HTML。若通过网关转发，请将 `/assets/doolla-components.js` 和 `/api/doolla/v2/` 路径转发到 Doolla。使用 SSE 时让网关及时转发事件。

```html
<script type="module" src="/assets/doolla-components.js"></script>
<doolla-chat id="mes-chat" title="制造问数" placeholder="输入工单问题"></doolla-chat>
```

如果业务前端和 API 分别部署，启动普通 Go CLI 时用 `-allow-origins` 指定**前端页面的完整来源**，包括协议和非默认端口。多个来源以逗号分隔。例如本地前端在 `http://127.0.0.1:3000`，Doolla 在 `http://127.0.0.1:8000`，就在原启动参数中加入 `-allow-origins http://127.0.0.1:3000`，并在业务页面使用以下代码。

```html
<script type="module" src="http://127.0.0.1:8000/assets/doolla-components.js"></script>
<doolla-chat id="mes-chat" api-base="http://127.0.0.1:8000" title="制造问数"></doolla-chat>
```

组件会先用 SSE 接收问答，首次收到组件前若连接失败，会携带同一请求 ID 回退到轮询。网关自定义路由时可配置 `sse-endpoint`、`poll-endpoint` 和 `ws-endpoint`。跨域页面与 API 均使用 HTTPS 时，签名 Cookie 支持可信跨源请求。宿主浏览器的第三方 Cookie 策略仍可能阻止跨站身份延续。通过同源网关也可保留会话。

宿主页面可以监听组件的 `turn-completed` 事件，从 `event.detail` 读取 `status`、`code`、`request_id` 和 `conversation_id`。`sendMessage()` 在服务明确返回部分完成或失败时返回 `false`，页面仍会保留已收到的结果和错误提示。旧服务仅发送传输结束标记时，组件沿用原有返回值。

## 选择界面和回复语言

独立网页右上角可以切换「简体中文 / English」，刷新后保留选择。嵌入网页可以设置 `<doolla-chat locale="zh-CN">`，并通过 `locale-changed` 事件读取用户选择。服务支持 `en` 和 `zh-CN`，以及与它们匹配的语言标签。

SSE、轮询和 WebSocket 的请求正文均接受可选的 `locale`。例如 `{"message":"/help","locale":"zh-CN"}`。服务依次读取正文语言、`Accept-Language` 和服务默认值。重试时保留同一语言和请求 ID。更换语言发起新请求时使用新的请求 ID。

`/docs?locale=zh-CN` 和 `/docs?locale=en` 提供对应语言的交互文档。更多示例见[英文接入指南](en/getting-started.md)。

## 管理身份和重试

- **接入登录系统。** 在 Go 后端提供 `UserResolver`，从已认证会话取得用户 ID 与用户组。请求正文的 `metadata` 来自调用方。浏览器演示默认使用 `doolla_session` 签名 Cookie。需要跨服务重启延续演示用户时，给服务配置固定的 `DOOLLA_SESSION_SECRET`（至少 32 字节）。需要保存对话内容时另配 `-history-dir`。
- **调用管理功能。** CLI 配置 `DOOLLA_ADMIN_TOKEN` 后，可信后端可通过 `Authorization: Bearer <令牌>` 访问管理命令。令牌仅由后端持有。用户组决定可见和可执行的工具，数据库账号或自定义 SQL 执行器决定可读取的数据。
- **网络断开后重试。** 保存原始正文和 Cookie，向同一服务进程重发相同的 `request_id`。三种传输共享重放记录，默认保留五分钟。改动同 ID 的问题、会话或授权上下文可能得到 `409`。原执行中断或响应超出缓存预算也可返回 `409`，动作可能已经执行，此时先核对业务结果。多实例部署需要把重试路由到原进程。
- **定位接入错误。** `400` 检查 JSON 和必填 `message`，`401` 检查请求身份，浏览器跨域出现 `403` 时核对来源和允许列表。请求缓存满时会返回 `503`，稍后重试。打开服务的 `/docs` 可交互测试轮询，`/openapi.json` 提供请求和响应字段定义。

本地启动步骤见[使用指南](使用指南.md)。要由业务服务创建 Agent 并映射登录身份，见 [Go SDK 接入](Go-SDK接入.md)。
