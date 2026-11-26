# Running Doolla 2.x in Go

中文版覆盖浏览器问数、本地模型和 MES 演示，并提供网页、HTTP 及 Go SDK 的接入示例。[中文使用与接入指南](docs/guide/使用指南.md)

The Go module lives in `src/doolla/` and uses the import path `doolla.ai/sdk`. Run the commands below from the repository root with Go 1.25 or newer. SQLite and embedded Qdrant require CGO and a C compiler. The SQLite driver and chat UI are included in the binary. Node is used to rebuild the frontend when its TypeScript changes. Python is optional for applications that explicitly register the Python execution tools.

```sh
go -C src/doolla build -o /tmp/doolla ./cmd/doolla
/tmp/doolla -help
/tmp/doolla -provider mock -listen 127.0.0.1:8000
```

Open `http://127.0.0.1:8000`. The mock provider demonstrates the chat protocol. For actual SQL generation, select a tool-capable model and a database. For example, with an existing SQLite file, Ollama server and installed model:

```sh
/tmp/doolla -provider ollama -model qwen3:8b -dsn /path/to/warehouse.sqlite
```

## Models and databases

| CLI provider | API |
| --- | --- |
| `openai` | OpenAI Chat Completions and compatible endpoints |
| `responses` | OpenAI Responses |
| `azureopenai` | Azure OpenAI, with a deployment name and Azure endpoint |
| `anthropic` | Anthropic Messages |
| `gemini` | Gemini generateContent |
| `ollama` | Local Ollama chat |
| `mock` | In-process mock model |

Set `-model-url` to the provider's base URL. The CLI reads `DOOLLA_API_KEY` or the provider's usual API-key environment variable. The SDK's `NewModel(ModelConfig{...})` also accepts an HTTP client, temperature, maximum output tokens and provider-specific settings. `StreamingModel.Stream` provides incremental text and assembles the assistant's tool calls.

`-dsn` opens an existing SQLite file read-only. Relative file paths resolve from the directory where you launch the binary. For `go -C src/doolla run ./cmd/doolla`, that directory is `src/doolla/`. Pass `-sqlite-writable` to enable writes and creation of a missing database. With another backend, select its driver at build time and use its native DSN. DuckDB, PostgreSQL, MySQL, MSSQL, BigQuery, ClickHouse, Hive, Oracle, Presto and Snowflake are available in the optional driver module. Hive accepts native HiveServer2 Thrift connections via a `hive://` DSN and existing ODBC data sources via their configured DSN. [Driver builds and DSN examples](src/doolla/integrations/database/README.md)

## Identity, conversations and memory

The web demo issues a signed browser cookie. Set `DOOLLA_SESSION_SECRET` to a stable secret of at least 32 bytes to keep browser identities across restarts. To use the demo's administrative memory tools, set `DOOLLA_ADMIN_TOKEN` and send its value in an HTTP `Authorization: Bearer` header. Applications can pass their authenticated `UserResolver` to `NewServer`. Use `-allow-origins` for an embedding frontend on a different origin. Allowed groups govern both tool discovery and execution. Database accounts or the application's SQL runner govern row visibility.

The optional `-ui-features path/to/groups.json` file maps UI feature names to allowed groups. For example, `{"tool_names":["analyst","admin"],"export_report":["analyst"],"public_help":[]}` overrides tool-name visibility and registers two application-defined features. An empty array grants access to every group. Add `-replace-ui-features` (SDK: `ReplaceDefaultUIFeatures`) to use the file as the entire policy, as with Python's explicit `feature_group_access` map. In that mode, omitted features are denied. SDK tools read permitted names from `CurrentTurn(ctx).UIFeatures`. Enable `-audit-ui` to record each configured grant and denial. Applications still enforce tool access in the tool registry.

The task sidebar shows conversation preparation and, for users with `tool_names` access, progress for each executed tool. The Agent checkpoints the result before delivering the tool completion event. SDK workflows that read or change saved messages can set `AgentConfig.ConversationWorkflow`. Return a handled `WorkflowDecision` with a replacement message slice to persist the new history; an empty slice clears it.

Conversation history defaults to process memory. `-history-dir` enables atomic, per-user file snapshots. Point it at an existing Python 2.x conversation directory to load its metadata, messages and tool calls. The first Go update writes a snapshot under `.go/`. Python services continue to read their original history. `FileConversations.ExportPython` exports updated turns to a fresh Python-compatible directory. Each completed tool result is checkpointed before delivery to the browser. SQL result CSVs are kept under the user's artifact directory inside `-workdir`. Applications can set the `Artifacts.Directory` to an existing Python artifact root.

`-memory local` selects the in-process demo memory. Other values are `chroma`, `faiss`, `azuresearch`, `marqo`, `milvus`, `opensearch`, `pinecone`, `qdrant`, `weaviate` and `premium`. Text recall adds up to five relevant memories to the prompt. Saving and searching correct question/tool examples uses the registered memory tools.

| Setting | Purpose |
| --- | --- |
| `DOOLLA_MEMORY_URL`, `DOOLLA_MEMORY_COLLECTION` | Service endpoint and collection/index name |
| `DOOLLA_MEMORY_API_KEY` | Service credential |
| `DOOLLA_MEMORY_PROVISION` | Set to `true` to create a missing collection on supported services |
| `DOOLLA_MEMORY_TENANT`, `DOOLLA_MEMORY_DATABASE` | Chroma tenant and database |
| `DOOLLA_MEMORY_NAMESPACE`, `DOOLLA_MEMORY_REGION`, `DOOLLA_MEMORY_CONTROL_URL` | Pinecone namespace and provisioning settings |
| `DOOLLA_MEMORY_MARQO_MODEL` | Server-side embedding model used when creating a Marqo index |
| `DOOLLA_MEMORY_ORGANIZATION` | Premium organization |
| `DOOLLA_EMBED_URL`, `DOOLLA_EMBED_MODEL`, `DOOLLA_EMBED_API_KEY`, `DOOLLA_EMBED_DIM` | OpenAI-compatible embedding service and vector dimension |
| `DOOLLA_QDRANT_DIR`, `DOOLLA_CHROMA_DIR` | Embedded Qdrant directory or `:memory:`, or an existing Chroma data directory for the optional native Chroma build |
| `DOOLLA_FAISS_DIR`, `DOOLLA_FAISS_METRIC` | Local flat FAISS index directory and metric |

Use the same embedding model and dimension that created an existing index. The standard Chroma HTTP adapter takes an embedding function or the configured embedding endpoint. To open a local Chroma directory with the default all-MiniLM-L6-v2 model, build and run the optional native adapter. Set `CHROMAGO_ONNX_RUNTIME_PATH` to the ONNX Runtime shared library installed for your platform. The native runtime reuses the standard Chroma model cache and downloads missing Chroma libraries or model files on first use.

```sh
export CHROMAGO_ONNX_RUNTIME_PATH=/path/to/libonnxruntime.so
DOOLLA_CHROMA_DIR=/path/to/chroma_memory go -C src/doolla/integrations/chromalocal run ./cmd/doolla-chroma -memory chroma
```

Pass an embedding endpoint to use a different model. The SDK exposes the same adapter as `chromalocal.Open`. FAISS supports the flat index formats used by the Python integration. Cloud memory retains the upstream service's tool-memory API. The Go memory SDK accepts an injected HTTP client for deployments with custom authentication.

## Embedding the agent

Import the SDK as `doolla.ai/sdk`. If another local module uses a `replace` directive for this checkout, point it at `/path/to/doolla/src/doolla`.

```go
model, err := doolla.NewConfiguredModel("anthropic", "", "", "")
if err != nil { return err }
db, err := doolla.OpenSQLite("warehouse.sqlite")
if err != nil { return err }
defer db.Close()
files := doolla.Artifacts{Directory: "workspaces"}
memory := doolla.NewLocalMemory(10000)
tools := append(doolla.MemoryTools(memory),
    doolla.Registration{Tool: doolla.SQLTool{DB: db, Artifacts: files}},
    doolla.Registration{Tool: doolla.VisualizationTool{Artifacts: files}},
)
agent, err := doolla.NewConfiguredAgent(model, doolla.AgentConfig{Memory: memory}, tools...)
if err != nil { return err }
return http.ListenAndServe("127.0.0.1:8000", doolla.NewServer(agent, nil))
```

Applications can inject a `Model`, `SQLRunner`, `ResultFiles`, `FileToolStorage`, `MemoryStore`, `ConversationStore` and custom `Tool` implementations. `AgentConfig` controls model middleware, request-context enrichment, conversation filters, prompt building, lifecycle hooks, workflow interception, retry decisions, UI feature groups and observation. Use `-audit` on the CLI for structured tool and model audit events and timings.

`FileTools` exposes search, list, read, write and line edits. `PythonTools` accepts a workspace process executor. `LocalPythonProcess` executes an explicitly selected host interpreter with the user's artifact directory as the working directory. The embedding application must provide process isolation for untrusted code. The default CLI registers SQL, visualization and memory tools according to its configuration.

The existing Lit frontend uses the same v2 chat payloads through SSE, polling and WebSocket. `/health` reports server liveness. The root page serves the embedded frontend from the same origin. SSE, Poll and WebSocket share a process-local request cache. Reusing a request ID with the same identity, request body and authorization context within the default five-minute window replays the original response. An interrupted or oversized response returns a conflict to prevent duplicate tool execution. A different process or an expired entry starts a fresh execution. Set cache limits with `ServerOptions.Replay`. Regenerate the bundle after changing the web component with `go -C src/doolla generate ./web_components`.

Set `-max-output-tokens` to cap a model's response length. When serving the OpenAI Chat protocol with Ollama, add `-openai-legacy-max-tokens` to send the token-limit field that Ollama implements.

## Verification

Open `/docs` on a running Go server to inspect the chat endpoints and send a polling request. Clients can read the same contract at `/openapi.json`. The API reference and the chat frontend are both embedded in the binary.

The [Chinese acceptance report](docs/verification/Go-功能与真实服务验收.md) records the local models, MES browser checks, databases and memory backends exercised in this checkout.

```sh
go -C src/doolla vet ./...
go -C src/doolla test -race ./...
go -C src/doolla build -o /tmp/doolla ./cmd/doolla
```

From the repository root, run `go test -race ./tests/... ./examples/...` with Go 1.26+ for the public API and integration suites.

The Go tests exercise the model protocols, SQLite queries and updates, CSV-to-Plotly flow, three HTTP transports, user-scoped history, memory protocols and legacy-format conversation import. The optional database module has its own dependencies and test commands. The `evaluation` package loads Python 2.x YAML/JSON datasets to compare Go agents.

Model protocol tests use local responses. Real cloud acceptance requires each deployment's credentials, permissions, endpoint, schema and model.

For model comparison, tool argument binding and MES query examples, see the [Go examples](examples/README.md).
