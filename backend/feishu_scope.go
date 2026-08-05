package main

import "strings"

// applyFeishuScope 根据飞书群 chat_id 对应的白名单过滤 contact_key 集合。
// 只做白名单：未配置该群的飞书群默认放行（兼容现状）。
// 群白名单（group:xxx）与私聊白名单（contact:xxx）互相独立：
//   - 只配群白名单时，私聊默认全放行
//   - 只配私聊白名单时，群聊默认全放行
//   - 两者都配时，各自只放行白名单内的 key
func applyFeishuScope(searchKeys []string, chatID string, prefs Preferences) []string {
	if chatID == "" {
		return searchKeys
	}
	allowed := prefs.FeishuGroupScope[chatID]
	if len(allowed) == 0 {
		return searchKeys // 未配置 = 默认放行
	}
	out := make([]string, 0, len(searchKeys))
	for _, k := range searchKeys {
		if isFeishuKeyAllowed(k, chatID, prefs) {
			out = append(out, k)
		}
	}
	return out
}

// isFeishuKeyAllowed 判断单个 contact_key 是否在当前飞书群白名单内。
// 用于在全局共现检索等"不按 searchKeys"的路径上过滤命中项。
// 群白名单与私聊白名单互相独立：某类别未配置白名单时该类别全放行。
func isFeishuKeyAllowed(key, chatID string, prefs Preferences) bool {
	if chatID == "" {
		return true
	}
	allowed := prefs.FeishuGroupScope[chatID]
	if len(allowed) == 0 {
		return true
	}
	isGroupKey := strings.HasPrefix(key, "group:")
	isContactKey := strings.HasPrefix(key, "contact:")
	hasGroupWL, hasContactWL := false, false
	for _, k := range allowed {
		if strings.HasPrefix(k, "group:") {
			hasGroupWL = true
		} else if strings.HasPrefix(k, "contact:") {
			hasContactWL = true
		}
		if k == key {
			return true
		}
	}
	// key 不在白名单内，但如果对应类别的白名单未配置，则默认放行
	if isGroupKey && !hasGroupWL {
		return true
	}
	if isContactKey && !hasContactWL {
		return true
	}
	return false
}

// filterMemFactsByScope 过滤记忆事实，只保留 contact_key 在当前飞书群白名单内的项。
func filterMemFactsByScope(facts []MemFact, chatID string, prefs Preferences) []MemFact {
	if chatID == "" {
		return facts
	}
	out := make([]MemFact, 0, len(facts))
	for _, f := range facts {
		if isFeishuKeyAllowed(f.ContactKey, chatID, prefs) {
			out = append(out, f)
		}
	}
	return out
}

// filterVecMessagesByScope 过滤原始消息向量命中，只保留 contact_key 在允许范围内的项。
func filterVecMessagesByScope(hits []VecMessageHit, chatID string, prefs Preferences) []VecMessageHit {
	if chatID == "" {
		return hits
	}
	out := make([]VecMessageHit, 0, len(hits))
	for _, h := range hits {
		if isFeishuKeyAllowed(h.ContactKey, chatID, prefs) {
			out = append(out, h)
		}
	}
	return out
}

// injectFeishuGroupPrompt 把 chat_id 对应的群补充提示插入到 system prompt 的固定开场白之后。
// 返回注入后的 messages；未配置或提示为空时原样返回。
func injectFeishuGroupPrompt(msgs []LLMMessage, chatID string, prefs Preferences) []LLMMessage {
	if chatID == "" {
		return msgs
	}
	prompt := strings.TrimSpace(prefs.FeishuGroupPrompts[chatID])
	if prompt == "" {
		return msgs
	}
	injected := "\n\n【本群补充提示】\n" + prompt + "\n"
	// 定位机器人固定的系统开场白，补充提示紧跟其后（位于会话历史和检索上下文之前）。
	const introMarker = "使用 Markdown 排版。\n"
	for i := range msgs {
		if msgs[i].Role == "system" {
			content := msgs[i].Content
			if idx := strings.Index(content, introMarker); idx >= 0 {
				at := idx + len(introMarker)
				msgs[i].Content = content[:at] + injected + content[at:]
			} else {
				// 找不到固定开场白（如其它调用方），退回追加到 system 末尾。
				msgs[i].Content += injected
			}
			return msgs
		}
	}
	return append(msgs, LLMMessage{Role: "system", Content: injected})
}
