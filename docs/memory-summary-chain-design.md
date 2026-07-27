# 记忆摘要链设计文档

> 分支：`codex/fix-memory-summary-chain`
> 相关代码：`backend/mem.go`
> 诊断文档：`docs/memory-extraction-pipeline-diagnosis.md`

---

## 一、问题回顾

当前记忆提取管线（`extractAndStoreFacts`）采用固定条数分段：

- 每批 80 条消息（`memExtractChunkSize = 80`）
- 步进 64 条（`memExtractStride = 64`），重叠 16 条

每批独立调用 LLM 提炼事实，批次之间仅靠 16 条原始消息重叠衔接。这导致两类系统性故障：

1. **张冠李戴（幻觉）**：指代对象在很早之前被引入，重叠窗口之外，模型只能猜
2. **找不到主语**：群聊大量省略主语，分段边界切断"主语引入点"与"后续讨论"

根本原因：**16 条原始消息重叠的信噪比太低**，且**无结构化上下文在批次间传递**。

---

## 二、解决方案：上下文摘要链

### 核心思路

不再仅依赖原始消息重叠，而是在每批分析后生成一份**结构化上下文摘要**，传递给下一批。下一批模型可以参考摘要中的"人物表"、"当前话题"、"未消解指代"来消解"他/她/那个事"等指代。

```
段1分析 → 提取事实 + 生成摘要_0
                ↓ 摘要_0 传入
段2分析 → 参考摘要_0 消解指代 → 提取事实 + 生成摘要_1
                ↓ 摘要_1 传入
段3分析 → ...
```

### 数据结构

```go
// memContextSummary 是上一段分析传递给下一段的结构化上下文摘要。
type memContextSummary struct {
    Entities     []memEntity `json:"entities"`               // 人物表
    ActiveTopics []string    `json:"active_topics"`          // 当前话题
    Unresolved   []string    `json:"unresolved_references"`  // 未消解指代
}

type memEntity struct {
    Name    string   `json:"name"`
    Aliases []string `json:"aliases"`
}
```

### LLM 输出格式变更

**旧格式**（JSON 数组）：
```json
["张三喜欢户外运动", "李四在北京做程序员"]
```

**新格式**（JSON 对象）：
```json
{
  "facts": ["张三喜欢户外运动", "李四在北京做程序员"],
  "context_summary": {
    "entities": [{"name": "张三", "aliases": ["老张"]}],
    "active_topics": ["张三的项目进度"],
    "unresolved_references": ["第75条的'他'不确定指谁"]
  }
}
```

### 兼容性处理

`parseExtractResult` 函数兼容两种格式：
1. 优先尝试解析 JSON 对象（新格式）
2. 若失败，回退到 JSON 数组（旧格式）

这样即使某些模型仍返回旧格式，管线也能正常工作。

---

## 三、Prompt 改进

### 1. 强调"宁愿少记也不要错记"

将此作为**最重要原则**，置于规则列表之前：

```
【最重要原则】宁愿少记也不要错记。记忆一旦出错会误导后续所有判断，因此：
  - 如果某条信息缺乏主语、上下文不完整或无法确定所指对象，跳过该条
  - 不要根据片段猜测、脑补或推断
  - 宁可遗漏一条可能有价值的信息，也不要记录一条可能错误的信息
```

### 2. 注入前文上下文摘要

在 prompt 中新增"前文上下文"段，格式化展示上一段的人物表、当前话题、未消解指代：

```
前文上下文（来自上一段分析的摘要，用于消解"他/她/那个"等指代）：
已知人物：张三（又称：老张）、李四
当前话题：张三的项目进度
未消解指代：第75条的"他"不确定指谁
```

第一段无前文上下文时，显示"（本段是第一段，无前文上下文）"。

### 3. 要求输出上下文摘要

在输出格式中明确要求模型同时输出 `context_summary`，即使没有有价值的事实（`facts` 为 `[]`），也必须输出 `context_summary`。

---

## 四、实现细节

### 摘要体积限制

为防止摘要随批次无限膨胀，`capContextSummary` 函数限制：
- 最多 15 个人物条目
- 最多 8 个当前话题
- 最多 5 个未消解指代

### 摘要链在循环中的传递

在 `extractAndStoreFacts` 的循环中：

```go
var runningSummary memContextSummary  // 循环前初始化

for chunkIdx := ... {
    result, err := extractFactsFromChunk(chunk, ..., runningSummary)
    if err != nil {
        lastErr = err
    } else {
        runningSummary = result.ContextSummary  // 更新摘要，供下一段使用
        facts := result.Facts
        // ... 后续 embedding、去重、存库逻辑不变 ...
    }
}
```

### 保留原有重叠机制

16 条原始消息重叠仍然保留。摘要链是**补充**而非替代：
- 重叠提供短期上下文衔接（最近 16 条消息）
- 摘要链提供长期上下文衔接（跨批次的人物、话题、指代）

---

## 五、不变的部分

以下部分**不改动**，保持向后兼容：

- DB schema（`mem_facts` 表结构不变）
- 检查点机制（`extract_offset` 续传逻辑不变）
- 跨批次去重机制（embedding 相似度比对不变）
- 置顶记忆背景注入（`backgroundCtx` 不变）

---

## 六、预期效果

| 问题 | 改进前 | 改进后 |
|------|--------|--------|
| 张冠李戴 | 模型看不到指代引入点，硬猜 | 摘要中"已知人物"帮助消解指代 |
| 找不到主语 | 分段边界切断主语链 | 摘要中"当前话题"延续主语链 |
| 上下文衔接断裂 | 16 条原始重叠信噪比低 | 结构化摘要提供高质量上下文 |
| 错误记忆 | 模型可能猜测记录 | Prompt 强制"宁愿少记也不要错记" |
