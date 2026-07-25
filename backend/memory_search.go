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
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

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
	Groups      []string `json:"groups"`       // 用户明确指定的群聊名（"在XXX群里..."）
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
func DecomposeQuery(query string, prefs Preferences) (*QueryDecomposition, []LLMMessage, *StreamUsage, error) {
	today := time.Now().Format("2006-01-02")
	prompt := fmt.Sprintf(`你是 WeLink（微信聊天数据分析平台）的查询分析助手。
分析用户的问题，判断是否需要检索聊天记忆库。

今天是 %s。输出严格 JSON，不要任何解释或代码围栏：
{"needs_memory": true, "entities": ["人名或群名"], "concepts": ["语义概念"], "time_from": "YYYY-MM-DD", "time_to": "YYYY-MM-DD", "groups": ["群聊名"]}

规则：
1. needs_memory: 问题需要查阅聊天记录或记忆事实才能回答时为 true；追问、总结、澄清等可从上下文即答的为 false
2. entities: 问题中明确提到的联系人名或群聊名（没有则空数组）
3. concepts: 问题的核心语义概念，2-5个词或短语，用于向量检索记忆事实
4. time_from/time_to: 问题涉及特定时间段时给出日期范围（YYYY-MM-DD）；不涉及则留空字符串
5. 如果问题提到"最近"，time_from 设为三个月前的日期；"去年"则取去年全年
6. groups: 如果用户明确提到"在XXX群里"或指定了某个群聊，把群名放入 groups；否则空数组`, today)

	llmMsgs := []LLMMessage{
		{Role: "system", Content: prompt},
		{Role: "user", Content: query},
	}
	promptTokens := estimateMsgTokens(llmMsgs)

	result, err := CompleteLLM(llmMsgs, prefs)
	if err != nil {
		// 降级：假设需要记忆，用原始问题做搜索
		return &QueryDecomposition{
				NeedsMemory: true,
				Concepts:    []string{query},
			}, llmMsgs, &StreamUsage{
				PromptTokens: promptTokens,
				OutputTokens: 0,
				TotalTokens:  promptTokens,
			}, nil
	}

	outputTokens := estimateTokens(result)

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
			}, llmMsgs, &StreamUsage{
				PromptTokens: promptTokens,
				OutputTokens: outputTokens,
				TotalTokens:  promptTokens + outputTokens,
			}, nil
	}

	// 如果 concepts 为空，用原始问题兜底
	if len(decomp.Concepts) == 0 {
		decomp.Concepts = []string{query}
	}

	return &decomp, llmMsgs, &StreamUsage{
		PromptTokens:  promptTokens,
		OutputTokens:  outputTokens,
		TotalTokens:   promptTokens + outputTokens,
	}, nil
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

// FindGroupsContainingContact 返回包含指定联系人（wxid）的所有群聊 username。
// 用于"我问关于 A 的问题"时，也在包含 A 的群里搜索记忆。
func FindGroupsContainingContact(contactWxid string, svc *service.ContactService) []string {
	if svc == nil || contactWxid == "" {
		return nil
	}
	memberships := svc.GetAllRoomMemberships()
	var groups []string
	for groupU, members := range memberships {
		for _, m := range members {
			if m == contactWxid {
				groups = append(groups, groupU)
				break
			}
		}
	}
	return groups
}

// ResolveGroupName 把群聊名映射到 group:username。
func ResolveGroupName(groupName string, svc *service.ContactService) string {
	if svc == nil || groupName == "" {
		return ""
	}
	lower := strings.ToLower(groupName)
	for _, g := range svc.GetGroups() {
		if strings.ToLower(g.Name) == lower || strings.ToLower(g.Username) == lower {
			return "group:" + g.Username
		}
	}
	// 模糊匹配
	for _, g := range svc.GetGroups() {
		if strings.Contains(strings.ToLower(g.Name), lower) || strings.Contains(lower, strings.ToLower(g.Name)) {
			return "group:" + g.Username
		}
	}
	return ""
}

// ─── /api/ai/memory-search 端点 ───────────────────────────────────────────────

// MemorySearchResponse 是 /api/ai/memory-search 的响应。
type MemorySearchResponse struct {
	Decomposition    *QueryDecomposition `json:"decomposition"`      // LLM 查询分解结果
	ResolvedEntities []ResolvedEntity    `json:"resolved_entities"`  // 实体名 → contact_key 解析结果
	Facts            []MemFact           `json:"facts"`              // 匹配到的记忆事实
	Sources          []FactSource        `json:"sources"`            // 记忆事实对应的源聊天记录
	PinnedFacts      []MemFact           `json:"pinned_facts"`       // 置顶事实（始终注入）
	TokenUsage       *StreamUsage        `json:"token_usage"`        // DecomposeQuery 消耗的 token
	DecomposePrompt  []LLMMessage        `json:"decompose_prompt"`   // DecomposeQuery 发给 LLM 的原始 prompt
}

// registerMemorySearchRoutes 注册 /api/ai/memory-search 端点。
//
// 这个端点编排完整的两级检索流程：
//   1. DecomposeQuery — LLM 分解问题（needs_memory gate + 实体/概念/时间提取）
//   2. 如果 needs_memory=false → 直接返回（问题可即答，省 token）
//   3. ResolveEntities — 把实体名映射到 contact_key（缩小搜索范围，降噪）
//   4. SearchMemFacts — 用 concepts 做 embedding 搜索 mem_facts
//      - 有实体 → 按 contact_key 过滤搜索
//      - 无实体 → 全局搜索
//   5. ExtractFactSources — 从 mem_facts 的 source_from/source_to 提取源聊天记录
//   6. 时间过滤 — 如果分解出时间范围，过滤源聊天记录
func registerMemorySearchRoutes(api *gin.RouterGroup, getSvc func() *service.ContactService) {
	api.POST("/ai/memory-search", func(c *gin.Context) {
		var body struct {
			Query     string `json:"query"`
			ProfileID string `json:"profile_id"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Query) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "query 必填"})
			return
		}

		prefs := loadPreferences()
		cfg := llmConfigForProfile(body.ProfileID, prefs)
		if cfg.provider == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请先在设置中配置 AI 接口"})
			return
		}

		// Step 1: LLM 查询分解
		decomp, decompPrompt, decompUsage, _ := DecomposeQuery(body.Query, prefs)

		// needs_memory=false → 直接返回（问题可即答，不消耗检索 token）
		if decomp != nil && !decomp.NeedsMemory {
			c.JSON(http.StatusOK, MemorySearchResponse{
				Decomposition:   decomp,
				TokenUsage:      decompUsage,
				DecomposePrompt: decompPrompt,
			})
			return
		}

		// Step 2: 实体名解析
		svc := getSvc()
		var resolvedEntities []ResolvedEntity
		if decomp != nil && len(decomp.Entities) > 0 && svc != nil {
			resolvedEntities = ResolveEntities(decomp.Entities, svc)
		}

		// Step 2b: 解析用户指定的群聊 + 找到包含联系人的群聊
		var groupKeys []string // group:username 列表
		if decomp != nil && len(decomp.Groups) > 0 && svc != nil {
			for _, gn := range decomp.Groups {
				gk := ResolveGroupName(gn, svc)
				if gk != "" {
					groupKeys = append(groupKeys, gk)
				}
			}
		}
		// 对于每个成功解析的联系人，找到包含 ta 的群聊
		for _, re := range resolvedEntities {
			if re.ContactKey == "" || strings.HasPrefix(re.ContactKey, "group:") {
				continue
			}
			// re.ContactKey = "contact:wxid_xxx"
			contactWxid := strings.TrimPrefix(re.ContactKey, "contact:")
			groupsForContact := FindGroupsContainingContact(contactWxid, svc)
			for _, g := range groupsForContact {
				gk := "group:" + g
				// 去重
				found := false
				for _, ek := range groupKeys {
					if ek == gk {
						found = true
						break
					}
				}
				if !found {
					groupKeys = append(groupKeys, gk)
				}
			}
		}

		// Step 3: 搜索 mem_facts
		// 用 concepts 作为 embedding 搜索 query（多概念用空格拼接）
		searchQ := body.Query
		if decomp != nil && len(decomp.Concepts) > 0 {
			searchQ = strings.Join(decomp.Concepts, " ")
		}

		var allFacts []MemFact
		var pinnedFacts []MemFact

		// 检查是否有成功解析的实体
		hasResolvedEntity := false
		for _, re := range resolvedEntities {
			if re.ContactKey != "" {
				hasResolvedEntity = true
				break
			}
		}

		// 收集所有要搜索的 contact_key
		var searchKeys []string
		if hasResolvedEntity {
			for _, re := range resolvedEntities {
				if re.ContactKey != "" {
					searchKeys = append(searchKeys, re.ContactKey)
				}
			}
		}
		// 加上群聊 key
		searchKeys = append(searchKeys, groupKeys...)

		if len(searchKeys) > 0 {
			// 有实体/群聊 → 按 contact_key 过滤搜索（降噪）
			for _, sk := range searchKeys {
				facts, _ := SearchMemFacts(sk, searchQ, 10, prefs)
				allFacts = append(allFacts, facts...)
				pf, _ := GetPinnedMemFacts(sk)
				pinnedFacts = append(pinnedFacts, pf...)
			}
		} else {
			// 无实体 → 全局搜索
			facts, _ := SearchMemFacts("", searchQ, 20, prefs)
			allFacts = append(allFacts, facts...)
			pf, _ := GetPinnedMemFacts("")
			pinnedFacts = append(pinnedFacts, pf...)
		}

		// Step 4: 提取源聊天记录
		sources, _ := ExtractFactSources(allFacts)

		// Step 5: 时间过滤
		if decomp != nil && (decomp.TimeFrom != "" || decomp.TimeTo != "") {
			sources = filterSourcesByTime(sources, decomp.TimeFrom, decomp.TimeTo)
		}

		c.JSON(http.StatusOK, MemorySearchResponse{
			Decomposition:    decomp,
			ResolvedEntities: resolvedEntities,
			Facts:            allFacts,
			Sources:          sources,
			PinnedFacts:      pinnedFacts,
			TokenUsage:       decompUsage,
			DecomposePrompt:  decompPrompt,
		})
	})
}

// filterSourcesByTime 按时间范围过滤源聊天记录。
// vec_messages.datetime 格式为 "YYYY-MM-DD HH:MM"。
// timeFrom/timeTo 格式为 "YYYY-MM-DD"。
func filterSourcesByTime(sources []FactSource, timeFrom, timeTo string) []FactSource {
	var out []FactSource
	for _, s := range sources {
		var filtered []SourceMessage
		for _, m := range s.Messages {
			if timeFrom != "" && m.Datetime < timeFrom+" 00:00" {
				continue
			}
			if timeTo != "" && m.Datetime > timeTo+" 23:59" {
				continue
			}
			filtered = append(filtered, m)
		}
		if len(filtered) > 0 {
			out = append(out, FactSource{
				Fact:     s.Fact,
				Messages: filtered,
			})
		}
	}
	return out
}
