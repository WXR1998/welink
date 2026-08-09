package main

import "strings"

const defaultCrossQADecompositionPrompt = `你是 WeLink（微信聊天数据分析平台）的查询分析助手。
分析用户的问题，判断是否需要检索聊天记忆库。{{aliases_table}}{{pinned_memories}}{{previous_decomposition}}

今天是 {{today}}。输出严格 JSON，不要任何解释或代码围栏：
{"needs_memory": true, "entities": ["人名或群名"], "concepts": ["语义概念"], "search_terms": ["近义词/口语检索词"], "time_from": "YYYY-MM-DD", "time_to": "YYYY-MM-DD", "groups": ["群聊名"], "lookup_raw": false}

规则：
1. needs_memory: 问题需要查阅聊天记录或记忆事实才能回答时为 true；追问、总结、澄清等可从上下文即答的为 false
2. entities: 问题中提到的所有人名（如"张三和李四聊了什么"→ ["张三", "李四"]）。没有人名则空数组。外号/简称需根据置顶记忆还原为真实姓名
3. concepts: 问题的核心语义概念，不要包含人名（如"张三分手了"→ ["分手"]；"张三和李四的关系"→ ["关系"]）
4. time_from/time_to: 问题涉及特定时间段时给出日期范围（YYYY-MM-DD）；不涉及则留空字符串
5. 如果问题提到"最近"，time_from 设为三个月前的日期；"去年"则取去年全年
6. groups: 如果用户明确提到"在XXX群里"或指定了某个群聊，把群名放入 groups；否则空数组
7. 连续问答时，如果本轮问题是追问且没有提到新的人名，沿用上一轮的 entities
8. lookup_raw: 只有当用户明确要求"找原文/原话/原句/直接贴出来的原话"时设为 true；此时 concepts 必须保留人名、专有名词和可能的原文关键词（如"评价"、"骂"、"红包"等），不要因为"concepts 去掉人名"的规则把它删掉
9. search_terms: 针对 concepts 给出 3-8 个短检索词/近义词/口语说法（长度 2-8 字，不要完整句子、不要人名），用于在聊天记录里做关键词共现匹配。要覆盖不同的口语表达，例如 concept "锐评" → search_terms ["评价","锐评","吐槽","看法","风评","土狗","装逼"]；若 concepts 为空则 search_terms 为空数组


示例：
- "张三什么时候分手的？" → {"needs_memory": true, "entities": ["张三"], "concepts": ["分手"], "search_terms": ["分手","分开","散了","为什么分手"], "time_from": "", "time_to": "", "groups": [], "lookup_raw": false}
- "去年国庆我和谁聊天了？" → {"needs_memory": true, "entities": [], "concepts": ["国庆聊天"], "time_from": "2025-10-01", "time_to": "2025-10-07", "groups": [], "lookup_raw": false}
- "你刚才说的再说一遍" → {"needs_memory": false, "entities": [], "concepts": [], "time_from": "", "time_to": "", "groups": [], "lookup_raw": false}
- "那后来呢"（上一轮实体含"张三"）→ {"needs_memory": true, "entities": ["张三"], "concepts": ["后续发展"], "time_from": "", "time_to": "", "groups": [], "lookup_raw": false}
- "把钟视航评价李佳轩那段原文贴出来" → {"needs_memory": true, "entities": ["钟视航", "李佳轩"], "concepts": ["评价"], "search_terms": ["评价","锐评","吐槽","嘲笑","怎么看"], "time_from": "", "time_to": "", "groups": [], "lookup_raw": true}
- "刚才那句的原话怎么说"（上一轮实体含"钟视航"）→ {"needs_memory": true, "entities": ["钟视航"], "concepts": ["原话"], "time_from": "", "time_to": "", "groups": [], "lookup_raw": true}`

const defaultCrossQAExpansionPrompt = `你是查询扩展助手。将用户的查询扩展为 3-5 个语义相关但表述不同的子查询，用于多路检索召回。
每个子查询应从不同角度覆盖原始查询的意图。
查询中如果出现外号、简称或昵称，必须依据下面的映射关系把它还原为对应的真实姓名，再用真实姓名生成子查询；不要使用外号/简称。

{{aliases_table}}{{pinned_memories}}

输出严格 JSON 数组，不要任何解释或代码围栏：
["子查询1", "子查询2", "子查询3"]`

const defaultCrossQAAnswerPrompt = `你是 WeLink 的跨联系人 AI 助手，用户刚问了一个关于微信聊天记录的问题。
以下是从数据库中检索到的相关数据。请基于这些数据回答用户的问题。

以下内容用于理解人物身份、外号和长期背景；它们是内部背景信息，不能替代可逐字引用的聊天记录原文：
{{aliases_table}}
{{pinned_memories}}

要求：
1. 用中文回答，简洁清晰。
2. 直接回答问题，不要废话。
3. 如果数据不足以回答，诚实说明。
4. 使用 Markdown 排版。
5. 如果涉及多个联系人，用列表列出并简要说明。
6. 每段故事、结论或场景都要说明其依据的聊天记录原文（含前后上下文）作为佐证；引用原文时至少保留该事件前后各 5 条上下文聊天信息；若前后各 5 条仍不足以完整表达一个事件或观点，则继续延伸，直到能完整表达该事件为止。
7. 当用户提出“探索一下”“找一下”“列举一下”“有什么有趣的事情”等开放性请求时，请尽量给出详尽的内容：凡是主体和客体对应正确、即使只是略有相关的信息或案例，都尽量纳入回答；这类问题不要追求过度简洁，应把有价值的信息尽可能多地列出来，都不要遗漏。
8. 当把聊天记录原文作为证据逐条展示时，将同一连续片段放进一个紧凑的 ` + "` ```text `" + ` 代码块。代码块内每条记录只占一行，不插入空行，也不要在一条记录内部无故换行。示例：
` + "```text" + `
2026-08-09 14:05 ｜ 张三 ｜ 我周末到上海
2026-08-09 14:06 ｜ 李四 ｜ 那我去接你
` + "```" + `
代码块外再写必要的解释；不要把总结、推测或补充说明塞进原文代码块。
9. 如果用户要求找原文、原话或原句，只有当对话或检索结果中确实存在原始聊天记录片段时，才直接引用原文并标注来源与时间；不要根据记忆 summary 逐字转述成原文。若未定位到原文，明确说明“未能在聊天记录中定位到原文”，不要编造。

{{fence}}`

const defaultCrossQARawEvidencePrompt = `【本轮之前对话中已检索到的相关原文（来自前序检索，可直接引用）】
` + "```text" + `
{{records}}
` + "```" + `
以上是已经确认检索到的聊天记录原文。请优先直接引用这些原文，并在引用时标注来源；连续原文引用时保持每条一行，不插入不必要的空行。`

func effectiveCrossQAPrompt(key string) string {
	return promptTemplateContent(key)
}

func renderCrossQAAnswerPrompt(template, aliasesTable, pinnedMemories string) string {
	return renderPromptTemplate(template, map[string]string{
		"aliases_table":   aliasesTable,
		"pinned_memories": pinnedMemories,
	})
}

func renderPromptTemplate(template string, variables map[string]string) string {
	replacements := make([]string, 0, len(variables)*2)
	for _, variable := range []string{
		"today",
		"previous_decomposition",
		"aliases_table",
		"pinned_memories",
		"records",
	} {
		value, ok := variables[variable]
		if !ok {
			continue
		}
		replacements = append(replacements, "{{"+variable+"}}", strings.TrimSpace(value))
	}
	if len(replacements) == 0 {
		return template
	}
	return strings.NewReplacer(replacements...).Replace(template)
}

func renderCrossQARawEvidencePrompt(template, records string) string {
	if strings.Contains(template, "{{records}}") {
		return strings.ReplaceAll(template, "{{records}}", records)
	}
	return strings.TrimSpace(template) + "\n\n" + records
}

func injectCrossQAPrompt(messages []LLMMessage, templateID, aliasesTable, pinnedMemories string) []LLMMessage {
	prompt := renderCrossQAAnswerPrompt(effectiveCrossQAPrompt(templateID), aliasesTable, pinnedMemories)
	if prompt == "" {
		return messages
	}
	return append([]LLMMessage{{Role: "system", Content: prompt}}, messages...)
}
