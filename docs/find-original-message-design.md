# 找原文（原话/原句）检索与整轮上下文注入方案

> 目标问题：跨联系人 AI 问答中，用户先问“钟视航如何评价李佳轩”，AI 第一次回答并展示原文；用户紧接着说“把这段的原文直接找出来”，AI 反而找不到/不引用。
>
> 本方案设计如何做到：找到原文、整轮对话里的原文“视情况”补充、且命中原文时如实引用。

---

## 一、现状与根因

### 1. 第一次能命中，第二次找不到

两次提问走的是同一套 `/api/ai/memory-search` 增强检索，但召回路径不同：

- **第一次（语义问题）**：`DecomposeQuery` 把“如何评价”拆成偏语义的 `concepts`，向量检索能命中 `mem_facts` 里那一条提炼事实；前端再从 `facts` 的 `source_from/source_to` 通过 `ExtractFactSources` 拉出原始消息（`vec_messages`），所以“检索详情”能看到原文出处。
- **第二次（找原文）**：检索入口仍是 `EnhancedRetrieval`，但对**原始消息**只有一路 `SearchVecMessagesFiltered`（向量相似度，`minSim=0.3`、取 top-100）。它**没有**对原始聊天记录做关键词/BM25 精确匹配。
  - `SearchMemFactsBM25` 精确匹配的是 `mem_facts_fts`（提炼后的事实表），不是 `vec_messages` 原文。
  - `DecomposeQuery` 的 `concepts` 规则是“不要包含人名”，所以“找原文”这种问题很可能把人名/专有名词洗掉，向量检索在原文里召不回那条具体消息。
  - 即便偶尔命中了事实，也依赖那条 fact 是否提炼到了内容、以及 `ExtractFactSources` 能否把源区间完整带出。

文档 [docs/retrieval-enhancement-options.md](retrieval-enhancement-options.md) 已有记录：记忆提取是“有损压缩”，检索层无法弥补。这正是第二次不稳定的根因之一。

### 2. 整轮对话里的原文没被“视情况”利用

- 前端把 `history` 传给 LLM 时**只保留 `role/content`**，不保留每条 assistant 消息的 `memorySearchData`（其中的 `sources` / `vec_messages` 是原文）。
- 前端构造 `dataContext` 时，只使用**当前这一次** `memory-search` 返回的 `sources` / `vec_messages`，不会把整个对话里已经检索到、且可能相关的原文片段带进当前问题。
- 于是即使 AI 第一次已经找到原文，第二次“直接找出来”时，这些原文也不在 prompt 里，模型只能重新检索或依赖记忆事实。

---

## 二、目标

在“找原文 / 找原话 / 原句怎么说 / 直接贴出来”这类意图下：

1. 先把该问题的原文通过**原始聊天记录精确检索**稳定召回。
2. 整轮对话中**已经找到过的原文**作为候选，结合当前问题“视情况”挑选并注入（不一股脑全塞，受上下文长度限制）。
3. prompt 明确要求：命中原文时**原样引用**并标注来源；未命中时**如实说明查不到**，不得用记忆 / 猜测编造“原文”。

---

## 三、方案设计

### 阶段 A：意图识别

在 `DecomposeQuery`（memory_search.go）输出中新增字段：

```go
type QueryDecomposition struct {
    // ... 现有字段 ...
    LookupRaw bool `json:"lookup_raw"` // 是否“找原文/原话/原句/直接贴出来”
}
```

LLM 提示词增加规则：

- 用户意图是“找原文 / 找原话 / 原句 / 原话怎么说 / 直接贴出聊天记录原文”时，`lookup_raw=true`。
- `lookup_raw=true` 时，`concepts` **必须保留人名、专有名词、原文关键词**（允许从原问题中沿用，不被“remove 人名”规则洗掉）。
- 连续追问（“那这句的原文呢”等）时，沿用上一轮 `entities / concepts / groups`，并让 `previous_decomposition` 参与。

### 阶段 B：原始消息精确检索（后端）

在 `EnhancedRetrieval` 中，当 `decomp.LookupRaw == true` 时，对原始聊天记录新增一路**精确检索**，并合并进返回的 `sources` / `vecMessages`（或新增独立字段 `rawHits`）：

1. 已解析出的**联系人 key**（`contact:xxx`）：
   - 用原始 `query` 调 `svc.SearchMessages(username, query, includeMine=true)`，精确 LIKE 命中原文，返回 `ChatMessage`。
2. 已解析出的**群聊 key**（`group:xxx`）：
   - 用原始 `query` 调 `svc.SearchGroupMessages(username, query, "")`，返回 `GroupChatMessage` 原文。
3. **全库 / 跨联系人兜底**：
   - 用 `SearchFTS(key, query, topK)`（`msg_fts` FTS5 索引，支持 trigram 精确匹配）对所有有 `msg_fts` 索引的 key 检索，拿到 `content / datetime / sender / seq / contact_key` 原文，并做 ± 上下文窗口扩展。

把以上原始消息统一转换为可展示/可注入的记录：

```go
type RawExcerpt struct {
    SourceName string `json:"source_name"` // 联系人/群聊可读名或 contact_key
    Datetime   string `json:"datetime"`
    Sender     string `json:"sender"`
    Content    string `json:"content"`
    Seq        int    `json:"seq,omitempty"`
}
```

- `EnhancedRetrievalResult` 增加 `RawHits []RawExcerpt`。
- `/api/ai/memory-search` 响应中带出 `raw_hits`，前端检索详情可展示“原文精确命中”。

> 目的：绕过“提炼后事实表”，直接从 `vec_messages` / `msg_fts` / 原始消息库取原文。

### 阶段 C：整轮对话原文候选（前端收集，后端选择）

#### C1. 前端收集候选

在 `CrossContactQA.askQuestion` 中，从**整轮 `messages`**（不只上一轮）收集所有 assistant 消息 `memorySearchData` 里的原文：

- `memorySearchData.sources[*].messages`（原话，来自 `ExtractFactSources`）
- `memorySearchData.vec_messages`（原始消息向量命中）
- 新增 `memorySearchData.raw_hits`（本方案阶段 B 的精确命中）

统一成 `candidateSources`，按 `contact_key / source + datetime + content` 去重：

```ts
interface CandidateSource {
  sourceName: string;
  datetime: string;
  sender: string;
  content: string;
}
```

这些候选随 `/api/ai/analyze` 请求体传后端（新增字段 `candidate_sources`），幅度控制在对话内已检索原文的**上限条数 / 字符数**内（见阶段 D 截断）。

#### C2. 后端用额外 LLM 调用来“选择相关原文”（视情况注入）

在 `/api/ai/analyze` 中，当收到 `candidate_sources` 且当前为“找原文”类问题（可用 `query.charAt` 关键词判断，或直接始终尝试）：

- 用 `CompleteLLMFeature(..., "prior_excerpt_selection", profileID)` 追加一次 LLM 判断：
  - 输入：当前问题 + 候选原文列表（截断后）。
  - 输出：`["index 0", "index 3"]` 这类 JSON 数组，或直接输出“该保留的原文 index”。
- 从候选里挑选出相关片段，最多保留 **N 条（默认 8）且总字符 ≤ ~6k**，再注入 system prompt。

> 为什么用额外 LLM：用户明确说“视情况 + 上下文长度限制 + 可能需要额外 LLM”，整轮原文全塞不可控。LLM 挑选比关键词启发式更能判断“和前一句原文相关的是哪条”。rerank 作为二级降级，能在不额外消耗生成型 LLM 的情况下，用 cross-encoder 做语义精排，比纯关键词重合更准确。

**降级**：若额外 LLM 调用失败 / 超时，改用 **rerank 精排**挑选相关片段（复用 `RerankCandidatesWithFallback`，用当前问题作为 query、候选原文作为 documents，取分数最高的前 N 条）。若 rerank 也未配置或失败，最后再用关键词重合度取前几条兜底，并限制条数，保证主链路可用。

#### C3. 注入位置

在 `/ai/analyze` 的 `messages` 里，若命中候选原文，在 system prompt 追加：

```
【本轮之前对话中已检索到的相关原文（由前序检索得到）】
- [来源] 2026-XX-XX HH:MM [说话人]: 原文内容
```

- 这些是“已确认检索到的原文”，模型应优先引用，无需再次检索。
- 与阶段 B 本地新检索的 `RawHits` 去重后一起给出。

### 阶段 D：上下文长度控制

在 `candidate_sources` 组装和挑选后，统一用 `truncatePromptChars` 的同类逻辑做上限控制：

- 候选原文进入 LLM 选择前：每段原文截断到 ~200 字，总候选 ≤ ~12k 字符（只影响判断，不影响命中质量）。
- 最终注入 prompt 的精选原文：≤ ~6k 字符。
- 现有 `/ai/analyze` 已对 `body.Messages` 做 `truncatePromptChars`（100K 上限）兜底，新增片段也会被合并进该上限。

### 阶段 E：prompt 如实引用约束

在 `/ai/analyze` 的 system prompt（或前端 `dataContext`）追加规则：

```
如果用户要求找原文/原话：
- 只有在你确实拿到原始聊天记录片段时才直接引用原文，并在引用前标注来源与时间。
- 不要根据“记忆事实 / 提炼 summary”逐字转述成原文。
- 若检索/候选里没有对应原文，明确说明“未能在聊天记录中定位到原文”，不要编造。
```

这样能同时解决“第二次找不到还硬答”的问题。

---

## 四、改动文件清单

| 文件 | 改动 |
|------|------|
| `backend/memory_search.go` | `QueryDecomposition` 增加 `LookupRaw`；`DecomposeQuery` 提示词与降级逻辑；`/memory-search` 响应带 `raw_hits`。 |
| `backend/enhanced_retrieval.go` | `EnhancedRetrievalResult` 增加 `RawHits`；`EnhancedRetrieval` 在 `LookupRaw` 时对原始消息做精确检索（联系人/群聊/`msg_fts`）。 |
| `backend/main.go` | `/ai/analyze` body 增加 `candidate_sources`；用 `CompleteLLMFeature` 选择相关原文并注入 system prompt；追加如实引用约束。 |
| `frontend/src/components/dashboard/CrossContactQA.tsx` | 识别“找原文”意图；收集整轮 `messages` 的原文候选；把 `candidate_sources` 传给 `/ai/analyze`；在同类场景复用前序原文。 |
| `frontend/src/components/dashboard/AIHomePage.tsx`（如需要） | 透传/类型补齐。 |

---

## 五、测试与验证

1. 后端编译：`cd backend && GOFLAGS=-mod=mod go build -o /dev/null .`
2. 前端类型：`cd frontend && node ./node_modules/typescript/bin/tsc --noEmit`
3. 手动场景：
   - 第一轮问“钟视航如何评价李佳轩”，确认检索详情出现原文。
   - 第二轮问“把刚才那段原文直接找出来/贴出来”，确认：
     - 检索详情出现 `raw_hits`（精确原文）；
     - prompt 中出现前一轮相关的原文片段；
     - AI 回复直接引用原文、带来源，不脱离原文编造。
   - 三轮以上对话（话题 A → 原文 a，话题 B → 原文 b，再问“a 的原文”）确认只注入相关原文（a），不把整轮无关原文全塞进去。

---

## 六、开放问题 / 待确认

> 实施状态：阶段 A（LookupRaw 意图）、阶段 B（原始消息精确检索 + raw_hits）、阶段 C（前端整轮候选 + 后端 LLM 选择→rerank→关键词降级）、阶段 E（如实引用约束）均已落地；前端 `isRawLookup` 对“找原文”关键词语义做了兜底。阶段 D 的截断在 `selectRelevantSources` 中实现。

- 原始消息精确检索的“联系人/群聊 key 确定”依赖 `ResolveEntities` / `FindGroupsContainingContact`。若 AI 提问里没点名但只有“刚才那段”这样的指代，需要靠 `previous_decomposition` 沿用人名/群名，否则精确检索退化为 `msg_fts` 全库模糊召回。
- “找原文”意图判定：后端 `DecomposeQuery` 为主，前端 `isRawLookup` 兜底（用于本轮无记忆检索结果或后端意图漏判时仍传候选 / 约束）。
- 额外 LLM 选择调用会多一次 provider 请求与 token 消耗；当前未在 UI 提示“正在从历史中整理相关原文”，后续可选加。

---
