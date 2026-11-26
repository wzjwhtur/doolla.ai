# Go 模型适配器本机推理验收

> 历史验收记录。文中的接口和命令按当前命名展示，原始测试日期、结果及二进制指纹对应更名前的构建。

本轮在 macOS 上运行 Ollama 0.40.17，绑定 `127.0.0.1:54053`，启用 `OLLAMA_NO_CLOUD=1`。Azure OpenAI 和 Gemini 格式的请求经本机 LiteLLM 1.97.0 转发，推理也在这台机器上完成。云厂商的账号鉴权、配额、部署和模型行为需要在对应厂商环境另行验收。

## 验收内容

每个适配器均通过公开的 `NewModel`、`Reply` 和 `Stream` 发起请求。普通回答要求算出 8 + 9，流式回调拼接后必须等于最终回答，两种路径均需返回终止原因。工具用例分别用同步和流式发起一次 `lookup_order`，检查订单号 `A17`，随后要求模型从工具返回中读取状态码 `COBALT42`。OpenAI Chat、Azure OpenAI 和 Gemini 另将输出限制为两个 token，检查同步、流式的部分文本与真实截断原因。

### 2026-11-23 实测记录

以下六项均使用 `qwen2.5:1.5b`。两个 LiteLLM 路由共用本机模型别名 `doolla-mes`。

| Go 适配器 | 本机实际接口 | 普通与流式回答 | 同步与流式发起工具调用 | 输出上限验证 |
| --- | --- | --- | --- | --- |
| Ollama | Ollama `/api/chat` | 均通过，`stop` | 均通过 | 原生接口返回 `done_reason=length` |
| OpenAI Chat | Ollama `/v1/chat/completions` | 均通过，`stop` | 均通过 | 同步、流式均返回部分文本和 `length`。直连时需启用 `OpenAIUseLegacyMaxTokens` |
| OpenAI Responses | Ollama `/v1/responses`，另用 LiteLLM 复测截断 | 均通过，`completed` | 均通过 | LiteLLM 同步及流式均交付部分文本，状态为 `incomplete`。Ollama 原生路由把两 token 的截断答复标为 `completed` |
| Anthropic Messages | Ollama `/v1/messages` | 均通过，`end_turn` | 均通过 | 原生兼容接口同步返回 `stop_reason=max_tokens` |
| Azure OpenAI | LiteLLM `/openai/deployments/doolla-mes/chat/completions` | 均通过，`stop` | 均通过 | 同步、流式均返回部分文本和 `length` |
| Gemini | LiteLLM `/v1beta/models/doolla-mes:generateContent` 与 `:streamGenerateContent` | 均通过，`stop` | 均通过 | 同步、流式均保留部分文本并得到 `max_tokens` |

Go 对 OpenAI Chat 默认发送 `max_completion_tokens`，可通过 `ModelConfig.OpenAIUseLegacyMaxTokens` 切换为 上游 Python 适配器使用的 `max_tokens`。Ollama 0.40.17 直连会忽略前一个字段。指定 `max_tokens=2` 后，同步原始响应确实只有 `1.`，`completion_tokens=2`。另外用 LiteLLM 的 OpenAI Chat 路由复测，默认字段也通过全部断言。

Responses 的单独截断检查中，两条路由都生成了两个 token。Ollama 回报 `status=completed`，客户端依赖服务端终止标记，在此路由无法识别此次截断。LiteLLM 回报 `status=incomplete`，省略 `incomplete_details.reason`。Go 现在保留已返回文本，标记答复未完成并放弃部分工具调用。同步和流式的实际返回均为 `1,`，用量报告为输入 39、输出 2。明确返回 `max_output_tokens` 与 `content_filter` 的两种情况分别有协议回归，后者仍作为错误处理。

```sh
DOOLLA_TEST_RESPONSES_PARTIAL_MODEL=doolla-mes DOOLLA_TEST_RESPONSES_PARTIAL_URL=http://127.0.0.1:54054/v1 \
DOOLLA_TEST_RESPONSES_PARTIAL_KEY=local-inference go -C src/doolla test -count=1 -v -run '^TestLiveIncompleteResponses$' .
```

LiteLLM 的 Gemini 原始流式末帧含 `finishReason=STOP`，且 `usageMetadata` 中三个计数均为零。验收记录保留了上游返回值，不能用该路由核对流式 token 数。

## 在本机复测

Ollama 的原生接口、OpenAI Chat、Responses 和 Anthropic Messages 协议使用同一个推理进程。安装同款工具模型后，可在仓库根目录运行以下命令。若使用 Ollama 的默认端口，请将端口改为 `11434`。

```sh
model='qwen2.5:1.5b'
endpoint='http://127.0.0.1:54053'
export DOOLLA_TEST_MODEL_OLLAMA="$model" DOOLLA_TEST_MODEL_OLLAMA_BASE_URL="$endpoint"
export DOOLLA_TEST_MODEL_OPENAI="$model" DOOLLA_TEST_MODEL_OPENAI_BASE_URL="$endpoint/v1"
export DOOLLA_TEST_MODEL_OPENAI_LEGACY_MAX_TOKENS=1
export DOOLLA_TEST_MODEL_RESPONSES="$model" DOOLLA_TEST_MODEL_RESPONSES_BASE_URL="$endpoint/v1"
export DOOLLA_TEST_MODEL_ANTHROPIC="$model" DOOLLA_TEST_MODEL_ANTHROPIC_BASE_URL="$endpoint"
export DOOLLA_TEST_MODEL_OPENAI_API_KEY=local-inference DOOLLA_TEST_MODEL_RESPONSES_API_KEY=local-inference
export DOOLLA_TEST_MODEL_ANTHROPIC_API_KEY=local-inference DOOLLA_TEST_MODEL_OLLAMA_API_KEY=local-inference
go -C src/doolla test -count=1 -v -run '^TestLiveModelProviders$' .
```

Azure 和 Gemini 的测试入口分别使用 `DOOLLA_TEST_MODEL_AZUREOPENAI`、`DOOLLA_TEST_MODEL_GEMINI`。它们取本地代理里的模型别名。对应的 `_BASE_URL` 分别取 `http://127.0.0.1:54054` 和 `http://127.0.0.1:54054/v1beta`。如果接口要求密钥，通过同前缀的 `_API_KEY` 传入。每个提供方只有在显式指定模型名时才运行，输出为 `SKIP` 的项目尚未执行。

此次演示的可执行文件和模型位于 `/private/tmp/doolla-local-llm.a8mAvT`。已保留验收后的本地服务。重启时可在该目录执行 `python3 start.py ollama` 和 `python3 start.py proxy`。在其他机器上可通过 Ollama 获取公开模型 `ollama pull qwen2.5:1.5b`，将 LiteLLM 的 `model_name` 设为 `doolla-mes`、`litellm_params.model` 设为 `ollama_chat/qwen2.5:1.5b`，并将 `api_base` 指向同机 Ollama。

在没有模型服务的环境中，协议回归可重放正常、截断及安全拦截的响应，覆盖 Azure、OpenAI、Gemini 和 Responses 的关键字段。运行 `go -C src/doolla test -count=1 -run 'TestTruncatedModelReplies|TestGeminiRejectedCandidate'`。
