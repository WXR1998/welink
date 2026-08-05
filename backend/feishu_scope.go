package main

// applyFeishuScope 根据飞书群 chat_id 对应的白名单过滤 contact_key 集合。
// 只做白名单：未配置该群的飞书群默认放行（兼容现状）。
// 白名单同时包含群（group:xxx）和私聊（contact:xxx），一视同仁。
// 命中白名单的 key 保留；不在白名单中的 key 从结果中剔除。
func applyFeishuScope(searchKeys []string, chatID string, prefs Preferences) []string {
	if chatID == "" {
		return searchKeys
	}
	allowed := prefs.FeishuGroupScope[chatID]
	if len(allowed) == 0 {
		return searchKeys // 未配置 = 默认放行
	}
	allowSet := make(map[string]bool, len(allowed))
	for _, k := range allowed {
		if k != "" {
			allowSet[k] = true
		}
	}
	out := make([]string, 0, len(searchKeys))
	for _, k := range searchKeys {
		if allowSet[k] {
			out = append(out, k)
		}
	}
	return out
}

// isFeishuKeyAllowed 判断单个 contact_key 是否在当前飞书群白名单内。
// 用于在全局共现检索等"不按 searchKeys"的路径上过滤命中项。
func isFeishuKeyAllowed(key, chatID string, prefs Preferences) bool {
	if chatID == "" {
		return true
	}
	allowed := prefs.FeishuGroupScope[chatID]
	if len(allowed) == 0 {
		return true
	}
	for _, k := range allowed {
		if k == key {
			return true
		}
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
