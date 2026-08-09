package main

import "strings"

const defaultCrossQAAnswerPrompt = `你是 WeLink 的跨联系人 AI 助手，用户刚问了一个关于微信聊天记录的问题。
以下是从数据库中检索到的相关数据。请基于这些数据回答用户的问题。

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
9. 如果用户要求找原文、原话或原句，只有当对话或检索结果中确实存在原始聊天记录片段时，才直接引用原文并标注来源与时间；不要根据记忆 summary 逐字转述成原文。若未定位到原文，明确说明“未能在聊天记录中定位到原文”，不要编造。`

const defaultCrossQARawEvidencePrompt = `【本轮之前对话中已检索到的相关原文（来自前序检索，可直接引用）】
` + "```text" + `
{{records}}
` + "```" + `
以上是已经确认检索到的聊天记录原文。请优先直接引用这些原文，并在引用时标注来源；连续原文引用时保持每条一行，不插入不必要的空行。`

func effectiveCrossQAPrompt(prefs Preferences, key string) string {
	if custom := strings.TrimSpace(prefs.PromptTemplates[key]); custom != "" {
		return custom
	}
	switch key {
	case "cross_qa_answer":
		return defaultCrossQAAnswerPrompt
	case "cross_qa_raw_evidence":
		return defaultCrossQARawEvidencePrompt
	default:
		return ""
	}
}

func renderCrossQARawEvidencePrompt(template, records string) string {
	if strings.Contains(template, "{{records}}") {
		return strings.ReplaceAll(template, "{{records}}", records)
	}
	return strings.TrimSpace(template) + "\n\n" + records
}

func injectCrossQAPrompt(messages []LLMMessage, templateID string, prefs Preferences) []LLMMessage {
	prompt := effectiveCrossQAPrompt(prefs, templateID)
	if prompt == "" {
		return messages
	}
	return append([]LLMMessage{{Role: "system", Content: prompt}}, messages...)
}
