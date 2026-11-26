# Memory stores

Pass the result of `memory.Open(ctx, config)` to `doolla.AgentConfig.Memory`.
The Go package uses native HTTP APIs for remote services and reads and writes FAISS flat indexes and embedded Qdrant SQLite directories locally. It has no Python runtime dependency.

| Provider | Endpoint or directory | Index settings |
| --- | --- | --- |
| `chroma` | Chroma HTTP v2, default `http://localhost:8000` | Collection name, tenant and database. Supply the embedding model and dimension used by the collection. The optional [native Go adapter](../chromalocal) opens a Python directory directly and supplies all-MiniLM-L6-v2. To expose the same directory over HTTP instead, stop the embedded writer and start Chroma with `chroma run --path /path/to/chroma_memory`. |
| `faiss` | `Config.Directory`, default `./faiss_index` | `cosine` reads/writes IndexFlatIP, `l2` reads/writes IndexFlatL2. Both use `index.faiss` and `metadata.pkl` with the same integer row positions as the Python store. |
| `qdrant` | Embedded by default, or a Qdrant HTTP endpoint such as `http://localhost:6333` | Collection with one unnamed cosine vector. Set `Config.Directory` to an embedded Qdrant directory or `:memory:` to use the local adapter. |
| `milvus` | Milvus REST v2, default `http://localhost:19530` | Optional database and bearer token. The collection uses the existing Doolla string primary key and `embedding` field. |
| `weaviate` | Weaviate HTTP, default `http://localhost:8080` | Supply a class name and optionally an API key. Stores client-provided vectors. |
| `opensearch` | OpenSearch with the k-NN plugin, default `http://localhost:9200` | Uses the existing `embedding` k-NN field. Supply a configured HTTP client for signed AWS requests. Scoring returns cosine similarity clamped to [0, 1]. |
| `azuresearch` | Azure AI Search service endpoint | Supply an API key and index name. Uses REST version `2024-07-01`. Existing Python indexes which lack a `content` column store text in their existing question and metadata fields. |
| `marqo` | Marqo 2.x data endpoint, default `http://localhost:8882` | Unstructured index using server-side tensor fields. Enumeration checks the returned set against the index document count. |
| `pinecone` | Pinecone data-plane index host | Serverless index with optional namespace. Provisioning uses `ControlEndpoint` (default `https://api.pinecone.io`) and AWS `Region` (default `us-east-1`). |
| `premium` | Premium API, explicit endpoint required | Optional API key and organization ID. The Premium API in this checkout exposes tool usage memory only. |

Set `Config.Provision` to create a missing index or collection. Existing resources keep their schemas. Pinecone provisioning discovers the data-plane host from its control plane. Chroma provisioning expects an existing tenant and database.

For vector backends other than Chroma and Marqo, the default client embedding reproduces the Python adapters' deterministic MD5-derived input vectors, dimension 384. This preserves lookup compatibility with their existing data. Supply `Config.Embed` and set `Config.Dimension` when using a semantic embedding model. A collection must use the same model for stored documents and search queries. Chroma's Python default is an ONNX embedding function, which the Go REST client takes as an explicit dependency through `Config.Embed`.

The FAISS adapter supports the two flat formats produced by this repository. It accepts the scalar and container pickle opcodes needed by JSON-like Python metadata. It uses process-level file locking for Go writers on Windows, macOS, Linux and BSD. A Python process writing the same files does not participate in this lock. Give each index one writer implementation at a time.

## Validation

From the repository root, `go -C src/doolla test ./integrations/memory` runs storage and provider contract tests without external credentials. Opt-in checks exercise native file and service implementations.

| Environment variable | Coverage |
| --- | --- |
| `DOOLLA_TEST_QDRANT_PYTHON` | Path to a Python interpreter with qdrant-client. Verifies that both the Python and Go clients can query and update an embedded index. |
| `DOOLLA_TEST_FAISS=1` | Generate a legacy index with Python's native Faiss, update it in Go and load it again in Python. Requires the `faiss` and `numpy` Python packages. |
| `DOOLLA_TEST_MILVUS_URL` | Provision a uniquely named collection on the specified Milvus instance. Optionally set `DOOLLA_TEST_MILVUS_KEY`. |
| `DOOLLA_TEST_CHROMA_URL` | Provision a uniquely named collection on the specified Chroma instance with an explicit deterministic test embedding. |

The service checks exercise duplicate saves, text/tool retrieval, recent records, filtered clear, ID deletion and missing IDs. They remove their test collections on completion.

For Qdrant HTTP, Weaviate, OpenSearch, Marqo and Azure AI Search, set the corresponding `DOOLLA_TEST_<PROVIDER>_URL` (uppercase provider name) and optional `DOOLLA_TEST_<PROVIDER>_KEY`. The service check creates and drops a uniquely named collection, class or index. Use a test service on which the credential allows those operations. For Pinecone, point `DOOLLA_TEST_PINECONE_URL` at an existing serverless index and supply `DOOLLA_TEST_PINECONE_KEY` and `DOOLLA_TEST_PINECONE_DIMENSION`; the check uses a new isolated namespace. The client-side test vectors are generated locally. Marqo embeds the documents using its configured model on the service.

Set `Config.MarqoModel` to select a locally cached Marqo model when creating an index. The opt-in test accepts the same model name in `DOOLLA_TEST_MARQO_MODEL`. Leaving this empty selects Marqo's server default.

```sh
DOOLLA_TEST_QDRANT_URL=http://127.0.0.1:6333 go -C src/doolla test -count=1 -v -run '^TestLiveMemoryBackends$/^qdrant$' ./integrations/memory
```

For Premium, set `DOOLLA_TEST_PREMIUM_URL`, `DOOLLA_TEST_PREMIUM_KEY` and `DOOLLA_TEST_PREMIUM_ORG` for a test organization, then run `go -C src/doolla test -count=1 -v -run '^TestLivePremium$' ./integrations/memory`. This checks saving an example, searching for it, listing recent memories and deleting it. Each run uses a distinct tool name.
