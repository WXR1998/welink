package main

// memory_search.go — 记忆优先的两级检索
//
// 把 mem_facts 当作语义索引层：
//   问题 → embedding → 搜索 mem_facts（Level 1：精炼事实）
//                          ↓ source_from / source_to
//                   提取源聊天记录（Level 2：vec_messages）
//                          ↓
//               facts + 源聊天记录 → 组装 context → 送 LLM
//
// 相比直接搜原始聊天记录：
//   1. mem_facts 是 LLM 已提炼、去重后的事实，搜索精度高
//   2. embedding 搜索天然处理近义词/转述
//   3. 每条 fact 的 source_from/source_to 指向 vec_messages 源消息，可精准追溯



import (
	"encoding/json"
	"strings"

	"welink/backend/service"
)

// SourceMessage 是记忆事实对应的源聊天记录中的一条消息。
type SourceMessage struct {
	Seq      int    `json:"seq"`
	Datetime string `json:"datetime"`
	Sender   string `json:"sender"`
	Content  string `json:"content"`
}

// FactSource 是一条记忆事实及其对应的源聊天记录。
type FactSource struct {
	Fact     MemFact         `json:"fact"`     // 记忆事实（含 ContactKey, SourceFrom, SourceTo）
	Messages []SourceMessage `json:"messages"` // 源聊天记录
}

// ExtractFactSources 批量提取记忆事实对应的源聊天记录。
//
// 对每条 fact，用 source_from/source_to 从 vec_messages 提取源消息。
// 手工添加的 fact（source_from=0, source_to=0）会被跳过——这类 fact 没有
// 对应的源聊天记录区间，只是用户手写的背景知识。
//
// extractAndStoreFacts 存储时：source_from = chunk 起始下标 i，
// source_to = chunk 结束下标 end-1。而 vec_messages.seq 也是按下标存的
// （vec.go: stmt.Exec(key, i+j, ...)），所以 source_from/source_to 直接
// 对应 vec_messages.seq 区间。
func ExtractFactSources(facts []MemFact) ([]FactSource, error) {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil || len(facts) == 0 {
		return nil, nil
	}

	var out []FactSource
	for _, f := range facts {
		// 跳过手工添加的 fact（source_from=0, source_to=0）
		// extractAndStoreFacts 存的 source_from=i, source_to=end-1
		// 第一批: i=0, end=80 → source_from=0, source_to=79
		// 手工添加: source_from=0, source_to=0 → 跳过
		if f.SourceFrom == 0 && f.SourceTo == 0 {
			continue
		}
		limit := f.SourceTo - f.SourceFrom + 1
		if limit <= 0 {
			continue
		}

		rows, err := db.Query(
			`SELECT seq, datetime, sender, content FROM vec_messages
			 WHERE contact_key = ? ORDER BY seq LIMIT ? OFFSET ?`,
			f.ContactKey, limit, f.SourceFrom)
		if err != nil {
			continue
		}

		var msgs []SourceMessage
		for rows.Next() {
			var m SourceMessage
			rows.Scan(&m.Seq, &m.Datetime, &m.Sender, &m.Content)
			msgs = append(msgs, m)
		}
		rows.Close()

		if len(msgs) > 0 {
			out = append(out, FactSource{
				Fact:     f,
				Messages: msgs,
			})
		}
	}
	return out, nil
}


// ─── LLM 查询分解 ─────────────────────────────────────────────────────────────

// QueryDecomposition 是 LLM 查询分解的结果。
type QueryDecomposition struct {
	NeedsMemory bool     `json:"needs_memory"` // 是否需要检索记忆
	Entities    []string `json:"entities"`     // 相关实体名（联系人/群聊名）
	Concepts    []string `json:"concepts"`     // 关键语义概念（用于 embedding 搜索）
	TimeFrom    string   `json:"time_from"`    // 时间范围起点 YYYY-MM-DD（空=不限定）
	TimeTo      string   `json:"time_to"`      // 时间范围终点 YYYY-MM-DD（空=不限定）
}

// DecomposeQuery 用 LLM 分析用户问题，输出结构化查询分解。
//
// 这是方案2的核心：把笼统的"发生了什么事情"分解为：
//   - needs_memory: 是否真的需要去检索记忆（追问/总结类可即答）
//   - entities: 涉及哪些联系人/群聊（用于缩小搜索范围，降噪）
//   - concepts: 核心语义概念（用于 embedding 搜索 mem_facts）
//   - time_range: 时间范围（用于过滤源聊天记录）
//
// 降级策略：LLM 调用失败或解析失败时，返回 needs_memory=true + concepts=原始问题，
// 保证流程不中断（最坏情况退化为全量搜索）。
func DecomposeQuery(query string, prefs Preferences) (*QueryDecomposition, error) {
	const prompt = `你是 WeLink（微信聊天数据分析平台）的查询分析助手。
分析用户的问题，判断是否需要检索聊天记忆库。

输出严格 JSON，不要任何解释或代码围栏：
{"needs_memory": true, "entities": ["人名或群名"], "concepts": ["语义概念"], "time_from": "YYYY-MM-DD", "time_to": "YYYY-MM-DD"}

规则：
1. needs_memory: 问题需要查阅聊天记录或记忆事实才能回答时为 true；追问、总结、澄清等可从上下文即答的为 false
2. entities: 问题中明确提到的联系人名或群聊名（没有则空数组）
3. concepts: 问题的核心语义概念，2-5个词或短语，用于向量检索记忆事实
4. time_from/time_to: 问题涉及特定时间段时给出日期范围（YYYY-MM-DD）；不涉及则留空字符串
5. 如果问题提到"最近"，time_from 设为三个月前的日期；"去年"则取去年全年`

	result, err := CompleteLLM([]LLMMessage{
		{Role: "system", Content: prompt},
		{Role: "user", Content: query},
	}, prefs)
	if err != nil {
		// 降级：假设需要记忆，用原始问题做搜索
		return &QueryDecomposition{
			NeedsMemory: true,
			Concepts:    []string{query},
		}, nil
	}

	// 提取 JSON（LLM 可能在前后加文字或代码围栏）
	raw := strings.TrimSpace(result)
	if start := strings.Index(raw, "{"); start >= 0 {
		if end := strings.LastIndex(raw, "}"); end > start {
			raw = raw[start : end+1]
		}
	}

	var decomp QueryDecomposition
	if err := json.Unmarshal([]byte(raw), &decomp); err != nil {
		// 降级
		return &QueryDecomposition{
			NeedsMemory: true,
			Concepts:    []string{query},
		}, nil
	}

	// 如果 concepts 为空，用原始问题兜底
	if len(decomp.Concepts) == 0 {
		decomp.Concepts = []string{query}
	}

	return &decomp, nil
}

// ─── 实体名解析 ───────────────────────────────────────────────────────────────

// ResolvedEntity 是实体名解析的结果。
type ResolvedEntity struct {
	Name        string `json:"name"`         // 原始实体名（LLM 提取的）
	ContactKey  string `json:"contact_key"`  // 解析后的 contact_key（contact:xxx 或 group:xxx），空=未匹配
	DisplayName string `json:"display_name"` // 展示名
	IsGroup     bool   `json:"is_group"`
}

// ResolveEntities 把 LLM 提取的实体名（如"张三"）映射到 contact_key。
//
// 用 ContactService 的联系人/群聊列表做匹配：
//   - 精确匹配 Remark / Nickname / Alias（联系人）或 Name（群聊）
//   - 大小写不敏感
//   - 找不到时尝试子串模糊匹配
//   - 找不到的实体返回空 ContactKey（调用方可跳过或降级为全局搜索）
//
// 这是方案2的降噪关键：如果用户问"我和张三聊了什么"，只搜索
// contact:张三 的 mem_facts，而不是全库扫描。
func ResolveEntities(entities []string, svc *service.ContactService) []ResolvedEntity {
	if svc == nil || len(entities) == 0 {
		return nil
	}

	contacts := svc.GetCachedStats()
	groups := svc.GetGroups()

	// 建索引：lower(name) → contact_key
	contactIndex := make(map[string]string) // lower(name) → "contact:username"
	displayNames := make(map[string]string) // contact_key → display name
	for _, c := range contacts {
		// 跳过群聊和系统账号（GetCachedStats 可能包含群聊）
		if strings.HasSuffix(c.Username, "@chatroom") || strings.HasPrefix(c.Username, "gh_") {
			continue
		}
		key := "contact:" + c.Username
		name := c.Remark
		if name == "" {
			name = c.Nickname
		}
		if name != "" {
			contactIndex[strings.ToLower(name)] = key
			displayNames[key] = name
		}
		if c.Alias != "" {
			contactIndex[strings.ToLower(c.Alias)] = key
		}
		contactIndex[strings.ToLower(c.Username)] = key
	}

	groupIndex := make(map[string]string) // lower(name) → "group:username"
	for _, g := range groups {
		key := "group:" + g.Username
		if g.Name != "" {
			groupIndex[strings.ToLower(g.Name)] = key
			displayNames[key] = g.Name
		}
		groupIndex[strings.ToLower(g.Username)] = key
	}

	var out []ResolvedEntity
	for _, entity := range entities {
		entity = strings.TrimSpace(entity)
		if entity == "" {
			continue
		}
		lower := strings.ToLower(entity)

		// 1. 精确匹配联系人
		if key, ok := contactIndex[lower]; ok {
			out = append(out, ResolvedEntity{Name: entity, ContactKey: key, DisplayName: displayNames[key], IsGroup: false})
			continue
		}
		// 2. 精确匹配群聊
		if key, ok := groupIndex[lower]; ok {
			out = append(out, ResolvedEntity{Name: entity, ContactKey: key, DisplayName: displayNames[key], IsGroup: true})
			continue
		}
		// 3. 子串模糊匹配联系人（实体名是某联系人名的子串）
		found := false
		for name, key := range contactIndex {
			if strings.Contains(name, lower) || strings.Contains(lower, name) {
				out = append(out, ResolvedEntity{Name: entity, ContactKey: key, DisplayName: displayNames[key], IsGroup: false})
				found = true
				break
			}
		}
		if found {
			continue
		}
		// 4. 子串模糊匹配群聊
		for name, key := range groupIndex {
			if strings.Contains(name, lower) || strings.Contains(lower, name) {
				out = append(out, ResolvedEntity{Name: entity, ContactKey: key, DisplayName: displayNames[key], IsGroup: true})
				found = true
				break
			}
		}
		// 5. 找不到：返回空 ContactKey
		if !found {
			out = append(out, ResolvedEntity{Name: entity, ContactKey: ""})
		}
	}
	return out
}
