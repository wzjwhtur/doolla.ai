# Go 记忆存储真实服务验收

> 历史验收记录。文中的接口和命令按当前命名展示，原始测试日期、结果及二进制指纹对应更名前的构建。

2026-11-23 在本机验证了 11 种记忆后端中的 8 种，共 10 种运行模式。Qdrant 和 Chroma 各包含两种模式。自带向量的后端使用兼容 Python 版的本地向量函数。Chroma 原生运行时使用本地 ONNX 模型 all-MiniLM-L6-v2，Marqo 使用本地 BGE-small-zh-v1.5。测试均在独立目录、集合或命名空间执行。

| 实现及运行模式 | 本轮结果 | 实际执行内容 |
| --- | --- | --- |
| local | 通过 | 真实 Go 进程内存储；写入工具及文本、重复写入、检索、近期列表、分类删除、条件清理 |
| FAISS | 通过 | `faiss-cpu 1.15.1` 写入原生索引和元数据，Go 读入并追加，Python 重开确认向量和元数据；另验 IP/L2 两种索引的完整存储操作 |
| Qdrant embedded | 通过 | Go 与 `qdrant-client 1.20.0` 通过本地 SQLite 索引双向读写，重开后检索和更新 |
| Qdrant HTTP | 通过 | 独立 Qdrant 1.15.4 服务，临时集合写入、去重、检索、近期记录、分类删除及清理 |
| Chroma HTTP | 通过 | 本机 `chroma run` 服务，CLI 报告 1.4.4，服务 `/api/v2/version` 返回 1.0.0；完整集合操作通过 |
| Chroma 本地原生 | 通过 | 原生 Chroma + 本地 ONNX 模型与 Python Chroma 1.5.9 对同一临时目录双向读写与相似检索，Go 关闭后重开验证 |
| Milvus | 通过 | Milvus 3.0.0 REST v2，独立集合通过完整存储操作并成功删除 |
| Weaviate | 通过 | Weaviate 1.32.4 独立服务，创建临时 class，自带向量检索、近期记录、分类删除与清理 |
| OpenSearch | 通过 | 官方 OpenSearch 2.19.2、Lucene 9.12.1。新建 `knn_vector` 索引，真实相似度脚本检索、近期记录、分类删除及清理均通过。临时索引已删除 |
| Marqo | 通过 | 官方 Marqo 2.16.1，内置 Vespa 8.472.109，CPU 加载本地 512 维 BGE-small-zh-v1.5。工具和文本写入、重复写入、向量搜索、近期记录、条件清理和删除全部通过，用例耗时 43.75 秒。临时索引已删除 |
| Azure AI Search | 待提供测试服务 | 需要服务端点与允许创建和删除独立索引的测试 key |
| Pinecone | 待提供测试服务 | 需要已有 serverless 测试索引的主机、维度及可写测试 namespace 的 key。Pinecone Local 模拟器已于 2026-10-07 结束支持，官方 Prism 替代方案仅返回示例数据。[官方说明](https://docs.pinecone.io/guides/production/mock-the-api) |
| Premium | 待提供测试服务 | 需要测试 API 端点、组织 ID 及 key，以新生成的唯一工具名隔离该次操作 |

## 复测

下列命令从仓库根目录执行。真实服务端点和密钥通过本机环境变量提供。`-count=1` 强制重新执行，`-v` 区分通过与缺少配置而跳过的用例。

```sh
DOOLLA_TEST_FAISS=1 DOOLLA_TEST_QDRANT_PYTHON=/path/to/python-with-qdrant-client \
  go -C src/doolla test -count=1 -v -run 'Test(LocalMemoryLifecycle|FAISSPythonInterop|FlatLifecycle|QdrantPythonInteroperability|QdrantLocalLifecycle)$' ./integrations/memory

DOOLLA_TEST_MILVUS_URL=http://127.0.0.1:19535 go -C src/doolla test -count=1 -v -run '^TestLiveMilvus$' ./integrations/memory
DOOLLA_TEST_CHROMA_URL=http://127.0.0.1:8000 go -C src/doolla test -count=1 -v -run '^TestLiveChroma$' ./integrations/memory

DOOLLA_TEST_NATIVE_CHROMA=1 DOOLLA_TEST_CHROMA_PYTHON=/path/to/python-with-chromadb \
  CHROMAGO_ONNX_RUNTIME_PATH=/path/to/libonnxruntime.dylib \
  go test -count=1 -v -run '^TestNativeChroma$' ./tests/chromalocal

DOOLLA_TEST_QDRANT_URL=http://127.0.0.1:6333 go -C src/doolla test -count=1 -v -run '^TestLiveMemoryBackends$/^qdrant$' ./integrations/memory
DOOLLA_TEST_WEAVIATE_URL=http://127.0.0.1:8080 go -C src/doolla test -count=1 -v -run '^TestLiveMemoryBackends$/^weaviate$' ./integrations/memory
DOOLLA_TEST_OPENSEARCH_URL=http://127.0.0.1:9200 go -C src/doolla test -count=1 -v -run '^TestLiveMemoryBackends$/^opensearch$' ./integrations/memory
DOOLLA_TEST_MARQO_URL=http://127.0.0.1:8882 DOOLLA_TEST_MARQO_MODEL=hf/bge-small-zh-v1.5 go -C src/doolla test -count=1 -v -run '^TestLiveMemoryBackends$/^marqo$' ./integrations/memory
```

可选云服务配置见 [存储接入说明](../../src/doolla/integrations/memory/README.md)。云端点缺失时，相应运行返回 `SKIP`；本表只将实际运行并通过断言的后端列为通过。

### Chroma 记忆进入本地模型回答

在新的 Chroma 目录写入一条虚构工厂验收代号，使用本机 ONNX all-MiniLM-L6-v2 生成文本向量，再将存储交给 Go Agent。以全新会话提出下列问题，检查 Agent 发给本地 Ollama 的系统消息包含存储中取回的事实，并核对最终答复中的代号。代号每次随机生成。

| 项目 | 首次实测 |
| --- | --- |
| 用户问题 | What is the internal acceptance code for Cedar Mill Factory's offline memory check? |
| 本地检索相似度 | 0.849，Agent 使用的检索阈值为 0.7 |
| 模型与响应 | `qwen2.5:1.5b`，答复为 “The internal acceptance code for Cedar Mill Factory's offline memory check is CL-B9DE3CB3.” |
| 复测 | 另连续执行三次，三个新代号均匹配，检索相似度分别为 0.864、0.853、0.881 |

```sh
DOOLLA_TEST_NATIVE_CHROMA=1 DOOLLA_TEST_OLLAMA_URL=http://127.0.0.1:54053 \
  DOOLLA_TEST_OLLAMA_MODEL=qwen2.5:1.5b CHROMAGO_ONNX_RUNTIME_PATH=/path/to/libonnxruntime.dylib \
  go test -count=1 -v -run '^TestNativeChromaOllamaAgent$' ./tests/chromalocal
```

### Marqo 离线模型与服务启动

本轮使用官方 arm64 镜像 `marqoai/marqo:2.16.1`，为测试容器分配 3 核 CPU 与 6 GiB 内存。将 Hugging Face 缓存中整个 `models--BAAI--bge-small-zh-v1.5` 目录只读挂载到容器内的 `/qa/huggingface/hub/models--BAAI--bge-small-zh-v1.5`，本轮模型修订号为 `7999e1d3359715c523056ef9478215996d62a620`。服务环境如下。

```sh
HF_HOME=/qa/huggingface
HF_HUB_CACHE=/qa/huggingface/hub
HF_SAVE_PATH=/qa/huggingface/hub
HF_HUB_OFFLINE=1
TRANSFORMERS_OFFLINE=1
MARQO_MODELS_TO_PRELOAD=[]
```

首次启动时，内置 Vespa 的 HTTP 端口先于配置收敛就绪，Marqo 在读取收敛状态时出现 `httpx.ReadTimeout`。复测等待 Vespa 配置服务的 `/application/v2/tenant/default/application/default/environment/default/region/default/instance/default/serviceconverge` 返回 `converged=true`，再启动镜像内的 Marqo API。其余写入、向量计算、查询和删除均经原版 Marqo 与 Vespa 执行。对于已就绪的服务可直接运行上面的 Go 用例。
