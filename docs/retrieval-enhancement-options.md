# 检索增强方案对比：为什么 AI 问答"找不到信息"

> 相关代码：
> - `backend/memory_search.go` — 记忆检索主流程（`DecomposeQuery`、`registerMemorySearchRoutes`）
> - `backend/mem.go` — 记忆事实提取与搜索（`SearchMemFactsFiltered`、`extractAndStoreFacts`）
> - `backend/embedding.go` — 向量化与相似度计算（`GetEmbeddingsBatch`、`cosineSimilarity`）
> - `backend/llm.go` — 多 Provider LLM 流式调用（`streamOpenAICompat`、`streamClaude`）
> - `backend/main.go` — `/ai/analyze` 入口，记忆注入 + SSE 流式返回
>
> 相关文档：
> - `docs/memory-extraction-pipeline-diagnosis.md` — 记忆提取管线的"张冠李戴"诊断
> - `docs/memory-summary-chain-design.md` — 上下文摘要链设计

---

## 一、现状诊断

### 现有检索流水线

```
用户提问
  → DecomposeQuery       (LLM 拆解: needs_memory / entities / concepts / time / groups)
  → ResolveEntities      (人名 → contact_key 映射, Remark/Nickname/Alias 精确+子串匹配)
  → SearchMemFactsFiltered (每联系人 top-10, cosine 相似度, minSim=0.3, 最多 50 条)
  → ExtractFactSources   (从 source_from/source_to 提取 vec_messages 源消息)
  → 时间过滤 → 返回
```

流水线本身设计精细：查询分解 + 实体解析 + 两级检索（事实 → 源消息）+ 时间过滤。

### 痛点定位

| # | 痛点 | 代码位置 | 根因 |
|---|------|----------|------|
| 1 | 纯向量检索，无关键词召回 | `mem.go:660` `SearchMemFactsFiltered` | 只做 embedding cosine 相似度，人名/专有名词/数字的精确匹配场景容易漏 |
| 2 | 记忆提取是"有损压缩"，检索层无法弥补 | `mem.go:315` `extractAndStoreFacts` | 80 条消息压成几条 fact，LLM 提取时漏掉的信息后面再怎么搜都搜不到 |
| 3 | 全量扫描 + 简单 top-K，无重排 | `mem.go:660` 全表扫描 → cosine 排序 → top-10 | top-10 里混入大量低相关结果，真正的答案排到 10 名开外被截断 |
| 4 | 查询分解依赖 LLM，不稳定 | `memory_search.go:159` `DecomposeQuery` | LLM 可能拆错（丢失实体或概念），30s 超时降级为原始 query 效果更差 |

---

## 二、方案详解

### 方案 1: BM25 混合检索

**核心思路**：在现有向量语义检索的基础上，增加 BM25 关键词检索，两路结果通过 RRF（Reciprocal Rank Fusion）融合。

```
query → [向量语义检索: cosine top-K]  ─┐
         [BM25 关键词检索: FTS top-K] ─┤→ RRF 融合 → top-10
```

**优势**
- 互补性强：向量检索擅长语义近似（"分手" ≈ "感情结束"），BM25 擅长精确词匹配（人名、专有名词、数字日期）
- 召回率提升：覆盖了纯向量检索漏掉的精确匹配场景
- 成本低：BM25 是传统算法，计算开销极小

**劣势**
- 需要维护额外的全文索引（FTS5 表或 Bleve 索引），索引同步增加复杂度
- BM25 对中文分词敏感，分词器不好则召回质量差
- RRF 融合需要调参（两路权重），不同数据分布最优权重不同

**适配 welink**
- 你已有 `msg_fts` 表（SQLite FTS5）和 `gse` 分词器（`go-ego/gse`），可以复用
- 改动点：在 `SearchMemFactsFiltered` 中增加一路 BM25 检索，结果合并
- 注意：当前 `mem_facts` 表没有 FTS 索引，需要新建

**开源选择**
- SQLite FTS5（内置，零依赖） — 你已在用
- Bleve（`github.com/blevesearch/bleve`） — Go 原生全文检索引擎，支持 BM25 / fuzzy / 前缀匹配，功能比 SQLite FTS5 强
- `go-ego/gse`（你已有） — 中文分词

**工作量**：中（建 FTS 索引 + 改检索逻辑 + 调融合权重）

---

### 方案 2: Rerank 重排模型 ⭐ 投入产出比最高

**核心思路**：向量检索召回 top-50 候选 → rerank 模型（cross-encoder）对每个 (query, document) 对精排 → 取 top-10。

```
query → embedding → cosine 相似度 → top-50 候选
                                        ↓
                          rerank cross-encoder 精排
                                        ↓
                                     top-10
```

**优势**
- 效果立竿见影：cross-encoder 直接对 (query, doc) 对打分，精度远高于 bi-encoder（embedding 相似度）
- 投入产出比最高：改动量小，检索精度提升 20-40%
- 工业标准：所有主流 RAG 系统（Dify、FastGPT、LangChain）都用 rerank

**劣势**
- 额外 API 调用成本（如果用云端 rerank，如 Jina/Cohere）
- 本地部署 rerank 模型需要 GPU，或接受 CPU 推理的延迟（200-500ms）
- 增加一次网络往返，端到端延迟增加

**适配 welink**
- 你已有 Jina embedding 配置（`embedding.go` 的 `defaultEmbeddingConfig`），Jina 也提供 rerank API，集成成本最低
- 或用 Ollama 部署 `bge-reranker-v2-m3`（但 Ollama 对 rerank 模型支持有限，可能需要 TEI）
- 改动点：在 `SearchMemFactsFiltered` 返回结果后，加一步 rerank

**开源选择**
- BGE-reranker（`BAAI/bge-reranker-v2-m3`） — 中文 rerank 效果最好的开源模型，可通过 HuggingFace TEI 本地部署
- Jina Rerank API — 你已有 Jina 配置，开箱即用
- Cohere Rerank API — 效果好但闭源

**工作量**：小（加一个 `RerankConfig` + rerank 调用函数 + 在检索流程中插入）

---

### 方案 3: 双路检索（mem_facts + vec_messages）

**核心思路**：检索时不只搜 `mem_facts`（压缩事实），也搜 `vec_messages`（原始消息 embedding），两路结果合并后给 LLM。

```
query → [搜 mem_facts: 压缩事实, 精度高]  ─┐
         [搜 vec_messages: 原始消息, 覆盖全] ─┤→ 合并 → LLM
```

**优势**
- 零新依赖：你现有的数据结构已经支持
- 覆盖面提升：`mem_facts` 是 LLM 提炼的"精华"，但可能有遗漏（痛点 2）；`vec_messages` 是全量原始消息，覆盖完整
- 互补：`mem_facts` 擅长"总结性事实"，`vec_messages` 擅长"具体某条消息说了什么"

**劣势**
- 噪音增加：原始消息里有大量无关内容（"哈哈"、"收到"、表情包），会稀释信号
- 上下文膨胀：返回给 LLM 的消息量变大，可能超 token 预算，需要截断策略
- 需要配合 rerank 才能有效去噪，否则 top-K 里混入太多噪音

**适配 welink**
- `vec_messages` 表已有 `embedding` 列，可以直接做向量搜索
- 你代码里已有 `echo_search.go` 在做类似的原始消息检索，可以参考
- 改动点：在 `registerMemorySearchRoutes` 流程中，增加一路对 `vec_messages` 的向量搜索

**工作量**：小-中（加一路检索 + 结果合并逻辑 + 截断策略）

---

### 方案 4: 查询改写（HyDE / Query Expansion）

**核心思路**：
- **HyDE**（Hypothetical Document Embedding）：先让 LLM 生成一个"假想答案"，用假想答案的 embedding 去检索（"答案"和"文档"的语义距离比"问题"和"文档"更近）
- **Query Expansion**：让 LLM 把"张三分手了"扩展成多个子查询（"张三分手时间"、"张三分手原因"、"张三分手后状态"），每个子查询分别检索，结果合并

```
query → LLM 生成假想答案 → 用假想答案 embedding 检索 (HyDE)
query → LLM 扩展为 N 个子查询 → 每个子查询分别检索 → 合并 (Query Expansion)
```

**优势**
- 对复杂问题效果显著：多角度检索能覆盖更多相关文档
- HyDE 对"问题描述方式和文档描述方式差异大"的场景特别有效
- 可与现有 `DecomposeQuery` 流程融合，不破坏现有架构

**劣势**
- 增加 LLM 调用次数：每轮多 1-2 次 LLM 调用，延迟 + 成本上升
- HyDE 生成的假想答案可能跑偏，反而降低检索质量
- Query Expansion 的子查询质量依赖 prompt 工程，不稳定
- 对简单查询（"张三是谁"）过度优化，反而增加不必要的延迟

**适配 welink**
- 你已有 `DecomposeQuery` 做查询分解，可以在此基础上扩展
- 改动点：在 `DecomposeQuery` 后增加 query expansion 步骤，或加 HyDE 步骤
- 风险：多一轮 LLM 调用，如果 LLM 不稳定（你已有 30s 超时降级），可能引入新的不稳定性

**开源参考**
- LangChain 的 `MultiQueryRetriever` 和 `HyDE` 实现（Python，但思路可移植到 Go）
- LlamaIndex 有类似的 query transform 模块

**工作量**：中（改查询分解流程 + 多路检索合并 + prompt 调优）

---

### 方案 5: ColBERT / Late Interaction

**核心思路**：把文档编码成 token 级别的向量，查询时做 token 级别的交互匹配（late interaction）。比句子级 embedding 精度高一个量级。

```
传统:  query → [768 维向量] ← cosine → [768 维向量] ← doc
ColBERT: query → [N×128 维] ← MaxSim → [M×128 维] ← doc (token 级交互)
```

**优势**
- 检索精度最高：token 级交互比句子级相似度精确得多
- 对长文档和复杂查询效果显著
- 无需额外 rerank（本身就是高精度检索）

**劣势**
- 存储成本高：每个 token 一个向量，存储量是句子级 embedding 的 10-100 倍
- 需要专门的索引引擎（不能简单存在 SQLite BLOB 里）
- 和现有架构完全不兼容：需要重建整个向量存储和检索逻辑
- 中文支持需要专门微调的 ColBERT 模型

**适配 welink**
- 与现有 SQLite + BLOB 存储的架构不兼容
- 需要引入新的向量数据库（如 Qdrant、Milvus）
- 改动量最大，不推荐作为第一步

**开源选择**
- RAGatouille（`github.com/AnswerDotAI/RAGatouille`） — Python，封装了 ColBERT v2
- ColBERT 原生（`github.com/stanford-futuredata/ColBERT`）

**工作量**：大（换底层检索引擎 + 重建索引 + 维护新依赖 + 可能需要 Python 微服务）

---

## 三、对比总览

| 方案 | 核心改进 | 工作量 | 预期提升 | 新依赖 |
|------|----------|--------|----------|--------|
| BM25 混合检索 | 加关键词召回 | 中 | 召回率 +15-25% | FTS5（已有）/ Bleve |
| **Rerank 重排** ⭐ | cross-encoder 精排 | **小** | **精度 +20-40%** | Jina API（已有）/ BGE-reranker |
| 双路检索 | mem_facts + vec_messages | 小-中 | 召回率 +10-20% | 无 |
| 查询改写 | HyDE / Query Expansion | 中 | 复杂问题 +15-30% | 无（复用现有 LLM） |
| ColBERT | token 级交互检索 | 大 | 精度 +30-50% | RAGatouille / 新向量库 |

---

## 四、推荐实施路径

### 第一步（立即）：加 Rerank

在 `SearchMemFactsFiltered` 返回 top-50 后，用 Jina Rerank API 做二次精排，取 top-10。

- 改动量最小，效果立竿见影
- 复用你现有的 Jina embedding 配置体系
- 如果 Jina 不可用，可以用 BGE-reranker 通过 TEI 本地部署

### 第二步（1-2 周）：双路检索

在 `registerMemorySearchRoutes` 流程中，除了搜 `mem_facts`，也搜 `vec_messages` 的原始消息 embedding，两路结果合并后 rerank。

- 你现有的数据结构已经支持，不需要新依赖
- 配合第一步的 rerank，可以有效去噪
- 参考已有的 `echo_search.go` 实现

### 第三步（2-4 周）：BM25 混合检索

如果前两步还不够，再加 SQLite FTS5 或 Bleve 做关键词召回，三路结果（向量 + BM25 + 原始消息）通过 RRF 融合。

- 需要建 FTS 索引 + 改检索逻辑 + 调融合权重
- 你已有 `msg_fts` 表和 `gse` 分词器，基础具备

### 暂不推荐

- **查询改写（HyDE / Query Expansion）**：多轮 LLM 调用增加延迟和不稳定性，等 rerank + 双路检索稳定后再考虑
- **ColBERT**：架构改动太大，和现有 SQLite 存储不兼容，投入产出比不合理

---

## 五、附录：各方案与现有代码的改动点映射

| 方案 | 改动文件 | 改动点 |
|------|----------|--------|
| Rerank | `embedding.go` / `mem.go` / `memory_search.go` | 新增 `RerankCandidates()` 函数；在 `SearchMemFactsFiltered` 返回后插入 rerank 步骤；`registerMemorySearchRoutes` 中推送 rerank 进度 |
| 双路检索 | `memory_search.go` / `mem.go` | 新增 `SearchVecMessages()` 函数；在 `registerMemorySearchRoutes` 中增加一路原始消息检索；合并 `mem_facts` + `vec_messages` 结果 |
| BM25 混合 | `mem.go` / `memory_search.go` / 新文件 `fts_search.go` | 新建 `mem_facts` 的 FTS5 索引；新增 `SearchMemFactsBM25()` 函数；在 `SearchMemFactsFiltered` 中增加 BM25 路径 + RRF 融合 |
| 查询改写 | `memory_search.go` | 在 `DecomposeQuery` 后新增 `ExpandQuery()` 或 `GenerateHyDE()` 步骤；多路检索合并 |
| ColBERT | 几乎所有 AI 相关文件 | 换底层向量存储；重建索引；引入 Python 微服务或新向量数据库 |
