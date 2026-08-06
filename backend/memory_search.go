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
	"sort"
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
	Fact       MemFact         `json:"fact"`        // 记忆事实（含 ContactKey, SourceFrom, SourceTo）
	Messages   []SourceMessage `json:"messages"`    // 源聊天记录
	SourceName string          `json:"source_name"` // 可读来源名（如"群聊「xxx」"或"与「xxx」的私聊"）
}

// factSubjectName 把一条记忆的 contact_key 解析为文首主语（优先备注、其次昵称）。
// 优先使用 API 层已解析好的 DisplayName，其次回退到 ContactService 查询。
func factSubjectName(f MemFact, svc *service.ContactService) string {
	if name := strings.TrimSpace(f.DisplayName); name != "" {
		return name
	}
	if name := strings.TrimSpace(f.SourceName); name != "" {
		return name
	}
	return subjectDisplayName(f.ContactKey, svc)
}

// subjectDisplayName 把一条记忆的 contact_key 解析为联系人的显示名（优先备注、其次昵称）。
// 与 factSubjectName 类似但直接接受 contact_key，便于单独填充 MemFact.DisplayName。
func subjectDisplayName(contactKey string, svc *service.ContactService) string {
	if svc == nil || !strings.HasPrefix(contactKey, "contact:") {
		return ""
	}
	uname := strings.TrimPrefix(contactKey, "contact:")
	for _, s := range svc.GetCachedStats() {
		if s.Username == uname {
			if s.Remark != "" {
				return s.Remark
			}
			if s.Nickname != "" {
				return s.Nickname
			}
		}
	}
	return uname
}

// pinnedFactLine 把一条置顶记忆拼成带主语的前缀行，避免主语缺失。
func pinnedFactLine(f MemFact, svc *service.ContactService) string {
	name := factSubjectName(f, svc)
	fact := strings.TrimSpace(f.Fact)
	if name == "" {
		return "- " + fact
	}
	return "- " + name + "：" + fact
}

// pinnedMemoryBlock 生成带主语、带外号映射的置顶背景事实片段，供分解/查询扩展等 LLM 调用使用。
func pinnedMemoryBlock(svc *service.ContactService) string {
	pinnedFacts, _ := GetPinnedMemFacts("")
	if len(pinnedFacts) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n── 用户置顶的背景事实（包含外号、简称等映射关系，用于理解问题中的人名）──\n")
	for _, f := range pinnedFacts {
		fmt.Fprintf(&sb, "%s\n", pinnedFactLine(f, svc))
	}
	return sb.String()
}

// contactAliasBlock 生成联系人外号对照表，供 LLM 把问题中的外号/简称还原为真实姓名。
func contactAliasBlock(svc *service.ContactService) string {
	allAliases, _ := GetAllContactAliases()
	if len(allAliases) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n── 联系人外号对照表（用于把问题中的外号/简称还原为真实姓名）──\n")
	mainName := func(key string) string {
		if strings.HasPrefix(key, extraContactKeyPrefix) {
			uname := strings.TrimPrefix(key, "contact:")
			if svc != nil {
				for _, s := range svc.GetCachedStats() {
					if s.Username == uname && s.Remark != "" {
						return s.Remark
					}
				}
			}
			return uname
		}
		if svc == nil {
			return strings.TrimPrefix(key, "contact:")
		}
		uname := strings.TrimPrefix(key, "contact:")
		for _, s := range svc.GetCachedStats() {
			if s.Username == uname {
				if s.Remark != "" {
					return s.Remark
				}
				if s.Nickname != "" {
					return s.Nickname
				}
			}
		}
		return uname
	}
	keys := make([]string, 0, len(allAliases))
	for k := range allAliases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		aliases := allAliases[k]
		if len(aliases) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "- %s 又称：%s\n", mainName(k), strings.Join(aliases, "、"))
	}
	return sb.String()
}

// resolveSourceName 把 contact_key 解析为可读来源名称。
func resolveSourceName(contactKey string, svc *service.ContactService) string {
	if svc == nil || contactKey == "" {
		return contactKey
	}
	if strings.HasPrefix(contactKey, extraContactKeyPrefix) {
		uname := strings.TrimPrefix(contactKey, "contact:")
		for _, s := range svc.GetCachedStats() {
			if s.Username == uname && s.Remark != "" {
				return "占位联系人「" + s.Remark + "」"
			}
		}
		return "占位联系人「" + uname + "」"
	}
	if strings.HasPrefix(contactKey, "group:") {
		uname := strings.TrimPrefix(contactKey, "group:")
		for _, g := range svc.GetGroups() {
			if g.Username == uname {
				if g.Nickname != "" && g.Nickname != g.Name {
					return fmt.Sprintf("群聊「%s」（原名「%s」）", g.Name, g.Nickname)
				}
				return "群聊「" + g.Name + "」"
			}
		}
		return "群聊「" + uname + "」"
	}
	if strings.HasPrefix(contactKey, "contact:") {
		uname := strings.TrimPrefix(contactKey, "contact:")
		for _, s := range svc.GetCachedStats() {
			if s.Username == uname {
				if s.Remark != "" {
					return "与「" + s.Remark + "」的私聊"
				}
				if s.Nickname != "" {
					return "与「" + s.Nickname + "」的私聊"
				}
			}
		}
		return "与「" + uname + "」的私聊"
	}
	return contactKey
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
func ExtractFactSources(facts []MemFact, svc *service.ContactService) ([]FactSource, error) {
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
				Fact:       f,
				Messages:   msgs,
				SourceName: resolveSourceName(f.ContactKey, svc),
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
	LookupRaw   bool     `json:"lookup_raw"`   // 是否“找原文/原话/原句/直接贴出来”
}

// looksLikeRawLookup 判断查询是否属于"找原文/原话/原句/贴出来"这类意图。
// 用于 LLM 分解失败 / 超时降级时仍能启用原始消息精确检索。
func looksLikeRawLookup(q string) bool {
	lower := strings.ToLower(q)
	keywords := []string{
		"找原文", "原文", "原话", "原句", "怎么说的", "怎么说", "原封不动",
		"直接贴", "贴出来", "贴出", "原样", "逐字", "一字不差", "聊天记录原文",
	}
	for _, k := range keywords {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
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
func DecomposeQuery(query string, prevDecomp *QueryDecomposition, prefs Preferences, profileID string, svc *service.ContactService) (*QueryDecomposition, []LLMMessage, *StreamUsage, error) {
	today := time.Now().Format("2006-01-02")

	// 置顶记忆 + 外号对照表：帮助 LLM 理解问题里外号/简称与真实姓名的映射
	pinnedBlock := pinnedMemoryBlock(svc)
	aliasBlock := contactAliasBlock(svc)

	// 构建上一轮分解结果的上下文（用于连续问答时沿用实体/概念/时间）
	var prevBlock string
	if prevDecomp != nil {
		var sb strings.Builder
		sb.WriteString("\n\n── 上一轮查询分解结果 ──\n")
		if len(prevDecomp.Entities) > 0 {
			fmt.Fprintf(&sb, "实体: %s\n", strings.Join(prevDecomp.Entities, ", "))
		}
		if len(prevDecomp.Concepts) > 0 {
			fmt.Fprintf(&sb, "概念: %s\n", strings.Join(prevDecomp.Concepts, ", "))
		}
		if prevDecomp.TimeFrom != "" || prevDecomp.TimeTo != "" {
			fmt.Fprintf(&sb, "时间范围: %s ~ %s\n", prevDecomp.TimeFrom, prevDecomp.TimeTo)
		}
		if len(prevDecomp.Groups) > 0 {
			fmt.Fprintf(&sb, "指定群聊: %s\n", strings.Join(prevDecomp.Groups, ", "))
		}
		sb.WriteString("如果本轮问题是追问（如'那后来呢'、'还有呢'、'他呢'），请沿用上一轮的实体和概念。\n")
		prevBlock = sb.String()
	}

	prompt := fmt.Sprintf(`你是 WeLink（微信聊天数据分析平台）的查询分析助手。
分析用户的问题，判断是否需要检索聊天记忆库。%s%s%s

今天是 %s。输出严格 JSON，不要任何解释或代码围栏：
{"needs_memory": true, "entities": ["人名或群名"], "concepts": ["语义概念"], "time_from": "YYYY-MM-DD", "time_to": "YYYY-MM-DD", "groups": ["群聊名"], "lookup_raw": false}

规则：
1. needs_memory: 问题需要查阅聊天记录或记忆事实才能回答时为 true；追问、总结、澄清等可从上下文即答的为 false
2. entities: 问题中提到的所有人名（如"张三和李四聊了什么"→ ["张三", "李四"]）。没有人名则空数组。外号/简称需根据置顶记忆还原为真实姓名
3. concepts: 问题的核心语义概念，不要包含人名（如"张三分手了"→ ["分手"]；"张三和李四的关系"→ ["关系"]）
4. time_from/time_to: 问题涉及特定时间段时给出日期范围（YYYY-MM-DD）；不涉及则留空字符串
5. 如果问题提到"最近"，time_from 设为三个月前的日期；"去年"则取去年全年
6. groups: 如果用户明确提到"在XXX群里"或指定了某个群聊，把群名放入 groups；否则空数组
7. 连续问答时，如果本轮问题是追问且没有提到新的人名，沿用上一轮的 entities
8. lookup_raw: 只有当用户明确要求"找原文/原话/原句/直接贴出来的原话"时设为 true；此时 concepts 必须保留人名、专有名词和可能的原文关键词（如"评价"、"骂"、"红包"等），不要因为"concepts 去掉人名"的规则把它删掉


示例：
- "张三什么时候分手的？" → {"needs_memory": true, "entities": ["张三"], "concepts": ["分手"], "time_from": "", "time_to": "", "groups": [], "lookup_raw": false}
- "去年国庆我和谁聊天了？" → {"needs_memory": true, "entities": [], "concepts": ["国庆聊天"], "time_from": "2025-10-01", "time_to": "2025-10-07", "groups": [], "lookup_raw": false}
- "你刚才说的再说一遍" → {"needs_memory": false, "entities": [], "concepts": [], "time_from": "", "time_to": "", "groups": [], "lookup_raw": false}
- "那后来呢"（上一轮实体含"张三"）→ {"needs_memory": true, "entities": ["张三"], "concepts": ["后续发展"], "time_from": "", "time_to": "", "groups": [], "lookup_raw": false}
- "把钟视航评价李佳轩那段原文贴出来" → {"needs_memory": true, "entities": ["钟视航", "李佳轩"], "concepts": ["评价"], "time_from": "", "time_to": "", "groups": [], "lookup_raw": true}
- "刚才那句的原话怎么说"（上一轮实体含"钟视航"）→ {"needs_memory": true, "entities": ["钟视航"], "concepts": ["原话"], "time_from": "", "time_to": "", "groups": [], "lookup_raw": true}`, aliasBlock, pinnedBlock, prevBlock, today)

	llmMsgs := []LLMMessage{
		{Role: "system", Content: prompt},
		{Role: "user", Content: query},
	}
	promptTokens := estimateMsgTokens(llmMsgs)

	// 带 30 秒超时调用 LLM，防止 nginx 反向代理 504 超时
	type llmResult struct {
		text string
		err  error
	}
	ch := make(chan llmResult, 1)
	go func() {
		text, err := CompleteLLMFeature(llmMsgs, prefs, "query_decomposition", profileID)
		ch <- llmResult{text, err}
	}()

	var result string
	var llmErr error
	select {
	case r := <-ch:
		result = r.text
		llmErr = r.err
	case <-time.After(30 * time.Second):
		llmErr = fmt.Errorf("LLM 响应超时（30s）")
	}

	if llmErr != nil {
		// 降级：假设需要记忆，用原始问题做搜索
		return &QueryDecomposition{
				NeedsMemory: true,
				Concepts:    []string{query},
				LookupRaw:   looksLikeRawLookup(query),
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
				LookupRaw:   looksLikeRawLookup(query),
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
		PromptTokens: promptTokens,
		OutputTokens: outputTokens,
		TotalTokens:  promptTokens + outputTokens,
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

// getContactAliasIndex 读取所有联系人在外号表里的别名映射。
// 返回 contact_key"contact:xxx" → ["外号", ...]，供实体解析和上下文注入共用。
func getContactAliasIndex() map[string][]string {
	all, err := GetAllContactAliases()
	if err != nil {
		return nil
	}
	return all
}

func ResolveEntities(entities []string, svc *service.ContactService) []ResolvedEntity {
	if svc == nil || len(entities) == 0 {
		return nil
	}

	contacts := svc.GetCachedStats()
	aliasIndex := getContactAliasIndex()
	extraContacts := extraContactsBySvc(svc)
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

	// 外号表：手动维护的联系人别名也纳入精确索引。
	for key, aliases := range aliasIndex {
		// 别名只用于联系人（key 以 contact: 开头）；display 名保持主名。
		for _, al := range aliases {
			t := strings.TrimSpace(al)
			if t == "" {
				continue
			}
			contactIndex[strings.ToLower(t)] = key
		}
	}

	// 占位联系人：没有聊天记录，但可作为关系背景的实体（如“xxx 的女友”）。
	for _, ec := range extraContacts {
		n := strings.TrimSpace(ec.DisplayName)
		if n == "" {
			continue
		}
		contactIndex[strings.ToLower(n)] = ec.ContactKey
		if displayNames[ec.ContactKey] == "" {
			displayNames[ec.ContactKey] = n
		}
	}

	groupIndex := make(map[string]string) // lower(name) → "group:username"
	for _, g := range groups {
		key := "group:" + g.Username
		if g.Name != "" {
			groupIndex[strings.ToLower(g.Name)] = key
			displayName := g.Name
			if g.Nickname != "" && g.Nickname != g.Name {
				displayName = g.Name + "（" + g.Nickname + "）"
			}
			displayNames[key] = displayName
		}
		if g.Nickname != "" && g.Nickname != g.Name {
			groupIndex[strings.ToLower(g.Nickname)] = key
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
		if strings.ToLower(g.Name) == lower ||
			strings.ToLower(g.Username) == lower ||
			(g.Nickname != "" && strings.ToLower(g.Nickname) == lower) {
			return "group:" + g.Username
		}
	}
	// 模糊匹配
	for _, g := range svc.GetGroups() {
		nameLower := strings.ToLower(g.Name)
		nickLower := strings.ToLower(g.Nickname)
		if strings.Contains(nameLower, lower) || strings.Contains(lower, nameLower) ||
			(g.Nickname != "" && (strings.Contains(nickLower, lower) || strings.Contains(lower, nickLower))) {
			return "group:" + g.Username
		}
	}
	return ""
}

// GetGroupKeysWithFacts 返回所有有记忆事实的群聊 contact_key（group:xxx）。
// 用于跨群聊搜索：即使某个群不包含查询中的联系人，
// 群成员也可能在群里讨论该联系人的事情。
func GetGroupKeysWithFacts() []string {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return nil
	}
	rows, err := db.Query(`SELECT DISTINCT contact_key FROM mem_facts WHERE contact_key LIKE 'group:%' AND version = ?`, memFactVersion)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		rows.Scan(&k)
		keys = append(keys, k)
	}
	return keys
}

// ─── /api/ai/memory-search 端点 ───────────────────────────────────────────────

// MemorySearchResponse 是 /api/ai/memory-search 的响应。
type MemorySearchResponse struct {
	Decomposition        *QueryDecomposition  `json:"decomposition"`                    // LLM 查询分解结果
	ResolvedEntities     []ResolvedEntity     `json:"resolved_entities"`                // 实体名 → contact_key 解析结果
	Facts                []MemFact            `json:"facts"`                            // 匹配到的记忆事实
	Sources              []FactSource         `json:"sources"`                          // 记忆事实对应的源聊天记录
	PinnedFacts          []MemFact            `json:"pinned_facts"`                     // 置顶事实（始终注入）
	PinnedContactAliases []PinnedContactAlias `json:"pinned_contact_aliases,omitempty"` // 注入置顶记忆的联系人外号，供 LLM 辨识人物
	TokenUsage           *StreamUsage         `json:"token_usage"`                      // DecomposeQuery 消耗的 token
	DecomposePrompt      []LLMMessage         `json:"decompose_prompt"`                 // DecomposeQuery 发给 LLM 的原始 prompt
	NormalizedQuery      string               `json:"normalized_query"`                 // 外号还原为原名后的提问，供最终回答使用
	// 增强检索结果
	VecMessages     []VecMessageHit   `json:"vec_messages"`     // 双路检索：原始消息命中
	ExpandedQueries []string          `json:"expanded_queries"` // 查询改写：扩展的子查询
	RerankUsed      bool              `json:"rerank_used"`      // 是否使用了 rerank
	RerankResults   []RerankScoreItem `json:"rerank_results"`   // rerank 精排结果（按分数降序）
	RawHits         []RawExcerpt      `json:"raw_hits"`         // 找原文场景：原始消息精确命中
	VectorHits      int               `json:"vector_hits"`      // 向量检索命中数
	BM25Hits        int               `json:"bm25_hits"`        // BM25 检索命中数
	VecMessageHits  int               `json:"vec_message_hits"` // 原始消息检索命中数
}

// registerMemorySearchRoutes 注册 /api/ai/memory-search 端点。
//
// 这个端点编排完整的两级检索流程：
//  1. DecomposeQuery — LLM 分解问题（needs_memory gate + 实体/概念/时间提取）
//  2. 如果 needs_memory=false → 直接返回（问题可即答，省 token）
//  3. ResolveEntities — 把实体名映射到 contact_key（缩小搜索范围，降噪）
//  4. SearchMemFacts — 用 concepts 做 embedding 搜索 mem_facts
//     - 有实体 → 按 contact_key 过滤搜索
//     - 无实体 → 全局搜索
//  5. ExtractFactSources — 从 mem_facts 的 source_from/source_to 提取源聊天记录
//  6. 时间过滤 — 如果分解出时间范围，过滤源聊天记录
func registerMemorySearchRoutes(api *gin.RouterGroup, getSvc func() *service.ContactService) {
	api.POST("/ai/memory-search", func(c *gin.Context) {
		var body struct {
			Query                 string              `json:"query"`
			ProfileID             string              `json:"profile_id"`
			ConversationKey       string              `json:"conversation_key"`
			PreviousDecomposition *QueryDecomposition `json:"previous_decomposition"`
			Model                 string              `json:"model"`             // 可选：覆盖 profile 中的模型
			ChatID                string              `json:"chat_id,omitempty"` // 飞书群 chat_id，用于白名单过滤
		}
		if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Query) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "query 必填"})
			return
		}

		prefs := loadPreferences()
		cfg := llmConfigForProfile(body.ProfileID, prefs)
		if body.Model != "" {
			cfg.model = body.Model
		}
		if cfg.provider == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请先在设置中配置 AI 接口"})
			return
		}

		// SSE 流式响应：每个步骤推送进度 + 最终推送完整结果
		flusher, ok := c.Writer.(http.Flusher)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "不支持流式响应"})
			return
		}
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("X-Accel-Buffering", "no")

		// 立即发一行 keepalive，防止 nginx 在等待 LLM 时超时
		fmt.Fprintf(c.Writer, ": keepalive\n\n")
		flusher.Flush()

		// keepalive 心跳：每 15 秒发一行 SSE 注释
		keepaliveDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					fmt.Fprintf(c.Writer, ": keepalive\n\n")
					flusher.Flush()
				case <-keepaliveDone:
					return
				}
			}
		}()

		sendProgress := func(step string, detail string) {
			data, _ := json.Marshal(map[string]string{"type": "progress", "step": step, "detail": detail})
			fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			flusher.Flush()
		}

		sendResult := func(resp MemorySearchResponse) {
			data, _ := json.Marshal(map[string]any{"type": "result", "data": resp})
			fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			flusher.Flush()
		}

		sendDone := func() {
			data, _ := json.Marshal(map[string]string{"type": "done"})
			fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			flusher.Flush()
		}

		// Step 1: LLM 查询分解
		sendProgress("decompose", "正在用 LLM 分解问题...")
		svc := getSvc()
		// 先把原文提问里的外号还原为原名，让后续整条链路都围绕原名进行。
		body.Query = normalizeEntityNames(body.Query, svc)
		decomp, decompPrompt, decompUsage, _ := DecomposeQuery(body.Query, body.PreviousDecomposition, prefs, body.ProfileID, svc)

		// needs_memory=false → 直接返回（问题可即答，不消耗检索 token）
		if decomp != nil && !decomp.NeedsMemory {
			close(keepaliveDone)
			sendResult(MemorySearchResponse{
				Decomposition:   decomp,
				TokenUsage:      decompUsage,
				DecomposePrompt: decompPrompt,
			})
			sendDone()
			return
		}

		// Step 2: 实体名解析
		entityNames := ""
		if decomp != nil {
			entityNames = strings.Join(decomp.Entities, "、")
		}
		sendProgress("resolve_entities", fmt.Sprintf("解析实体名: %s", entityNames))
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

		// Step 3: 确定检索查询词
		searchQ := body.Query
		if decomp != nil && len(decomp.Concepts) > 0 {
			searchQ = strings.Join(decomp.Concepts, " ")
		}

		// 收集所有要搜索的 contact_key
		hasResolvedEntity := false
		for _, re := range resolvedEntities {
			if re.ContactKey != "" {
				hasResolvedEntity = true
				break
			}
		}

		// 报告实体解析命中结果，让网关在慢检索前精确判断
		if len(decomp.Entities) > 0 || len(decomp.Groups) > 0 {
			anyResolved := hasResolvedEntity || len(groupKeys) > 0
			if anyResolved {
				sendProgress("resolve_entities", "实体解析结果: 已命中")
			} else {
				sendProgress("resolve_entities", "实体解析结果: 未命中")
			}
		}

		var searchKeys []string
		if hasResolvedEntity {
			for _, re := range resolvedEntities {
				if re.ContactKey != "" {
					searchKeys = append(searchKeys, re.ContactKey)
				}
			}
		}
		searchKeys = append(searchKeys, groupKeys...)

		// 加上所有有记忆总结的群聊 facts
		groupKeysWithFacts := GetGroupKeysWithFacts()
		for _, gk := range groupKeysWithFacts {
			found := false
			for _, ek := range searchKeys {
				if ek == gk {
					found = true
					break
				}
			}
			if !found {
				searchKeys = append(searchKeys, gk)
			}
		}

		// 飞书群白名单过滤：只保留该飞书群允许访问的 contact_key
		searchKeys = applyFeishuScope(searchKeys, body.ChatID, prefs)

		// ── 增强检索：BM25 + 双路 + 查询改写 + Rerank ──
		enhancedResult, err := EnhancedRetrieval(body.Query, decomp, searchKeys, decomp.TimeFrom, decomp.TimeTo, prefs, body.ProfileID, svc, func(step, detail string) {
			sendProgress(step, detail)
		})

		var allFacts []MemFact
		var pinnedFacts []MemFact
		var vecMessages []VecMessageHit

		if err != nil || enhancedResult == nil {
			// 降级：用原始向量检索逻辑
			sendProgress("search_facts", "向量检索记忆事实...")
			for _, key := range searchKeys {
				facts, _ := SearchMemFactsFiltered(key, searchQ, 10, decomp.TimeFrom, decomp.TimeTo, prefs)
				allFacts = append(allFacts, facts...)
			}
			if len(allFacts) == 0 {
				facts, _ := SearchMemFactsFiltered("", searchQ, 50, decomp.TimeFrom, decomp.TimeTo, prefs)
				allFacts = append(allFacts, facts...)
			}
			pf, _ := GetPinnedMemFacts("")
			pinnedFacts = append(pinnedFacts, pf...)
		} else {
			allFacts = enhancedResult.Facts
			vecMessages = enhancedResult.VecMessages
			// 置顶事实始终全量注入（之后再按飞书群白名单过滤），
			// 不能只收集本次解析出的实体，否则多数联系人的背景会被漏掉。
			pf, _ := GetPinnedMemFacts("")
			pinnedFacts = append(pinnedFacts, pf...)
		}

		// 飞书群白名单过滤：剔除不在允许范围内的记忆事实、置顶事实与原始消息命中
		allFacts = filterMemFactsByScope(allFacts, body.ChatID, prefs)
		pinnedFacts = filterMemFactsByScope(pinnedFacts, body.ChatID, prefs)
		vecMessages = filterVecMessagesByScope(vecMessages, body.ChatID, prefs)

		// 截断到 50
		if len(allFacts) > 50 {
			allFacts = allFacts[:50]
		}

		// 为 Facts / PinnedFacts / VecMessages 填充可读来源名，
		// 避免 LLM 上下文里出现 group:xxx@chatroom 这样的原始 key。
		for i := range allFacts {
			allFacts[i].SourceName = resolveSourceName(allFacts[i].ContactKey, svc)
		}
		for i := range pinnedFacts {
			pinnedFacts[i].SourceName = resolveSourceName(pinnedFacts[i].ContactKey, svc)
			pinnedFacts[i].DisplayName = subjectDisplayName(pinnedFacts[i].ContactKey, svc)
		}
		for i := range vecMessages {
			vecMessages[i].SourceName = resolveSourceName(vecMessages[i].ContactKey, svc)
		}

		// Step 4: 提取源聊天记录
		sendProgress("extract_sources", fmt.Sprintf("从 %d 条记忆事实中提取源聊天记录...", len(allFacts)))
		sources, _ := ExtractFactSources(allFacts, svc)

		// Step 5: 时间过滤
		if decomp != nil && (decomp.TimeFrom != "" || decomp.TimeTo != "") {
			sources = filterSourcesByTime(sources, decomp.TimeFrom, decomp.TimeTo)
		}

		// 把本轮检索到的原文候选按会话持久化到后端，供后续“找原文”追问直接使用。
		// 前端不再需要保留这些大数组，数据统一存放在聊天 session 侧。
		if body.ConversationKey != "" {
			var candidates []RawExcerpt
			seen := make(map[string]bool)
			add := func(src, dt, sender, content string) {
				if content == "" || seen[src+"|"+dt+"|"+sender+"|"+content] {
					return
				}
				seen[src+"|"+dt+"|"+sender+"|"+content] = true
				candidates = append(candidates, RawExcerpt{SourceName: src, Datetime: dt, Sender: sender, Content: content})
			}
			if enhancedResult != nil {
				for _, rh := range enhancedResult.RawHits {
					add(rh.SourceName, rh.Datetime, rh.Sender, rh.Content)
				}
			}
			for _, vm := range vecMessages {
				src := vm.SourceName
				if src == "" {
					src = vm.ContactKey
					if src == "" {
						src = "未知"
					}
				}
				add(src, vm.Datetime, vm.Sender, vm.Content)
			}
			for _, src := range sources {
				for _, m := range src.Messages {
					add(src.SourceName, m.Datetime, m.Sender, m.Content)
				}
			}
			saveConversationCandidates(body.ConversationKey, candidates)
		}

		pinnedContactAliases := buildPinnedContactAliases(pinnedFacts, func(key string) string {
			if svc == nil {
				return ""
			}
			uname := strings.TrimPrefix(key, "contact:")
			for _, s := range svc.GetCachedStats() {
				if s.Username == uname {
					if s.Remark != "" {
						return s.Remark
					}
					if s.Nickname != "" {
						return s.Nickname
					}
				}
			}
			return ""
		})
		// 推送最终结果
		close(keepaliveDone)
		resp := MemorySearchResponse{
			Decomposition:        decomp,
			ResolvedEntities:     resolvedEntities,
			Facts:                allFacts,
			Sources:              sources,
			PinnedFacts:          pinnedFacts,
			PinnedContactAliases: pinnedContactAliases,
			TokenUsage:           decompUsage,
			DecomposePrompt:      decompPrompt,
			NormalizedQuery:      body.Query,
			VecMessages:          vecMessages,
		}
		if enhancedResult != nil {
			resp.ExpandedQueries = enhancedResult.ExpandedQueries
			resp.RerankUsed = enhancedResult.RerankUsed
			resp.RerankResults = enhancedResult.RerankResults
			resp.RawHits = enhancedResult.RawHits
			resp.VectorHits = enhancedResult.VectorHits
			resp.BM25Hits = enhancedResult.BM25Hits
			resp.VecMessageHits = enhancedResult.VecMessageHits
		}
		sendResult(resp)
		sendDone()
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

// registerLLMLogRoutes 注册 /api/ai/llm-logs 端点，用于查看后端 LLM API 调用日志。
func registerLLMLogRoutes(api *gin.RouterGroup) {
	api.GET("/ai/llm-logs", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"logs": getLLMApiLogs()})
	})

	api.DELETE("/ai/llm-logs", func(c *gin.Context) {
		llmApiLogMu.Lock()
		llmApiLogs = nil
		llmApiLogSeq = 0
		llmApiLogMu.Unlock()
		clearLLMApiLogsDB()
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}
