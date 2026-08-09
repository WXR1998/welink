package main

// enhanced_retrieval.go — 检索增强：BM25 混合检索 + 双路检索 + 查询改写 + Rerank
//
// 整合四个方案提升 AI 问答的召回率和精度：
//   方案 1: BM25 混合检索 — 向量语义检索 + FTS5 关键词检索，RRF 融合
//   方案 2: Rerank 重排 — cross-encoder 对候选精排（见 rerank.go）
//   方案 3: 双路检索 — mem_facts（压缩事实）+ vec_messages（原始消息）
//   方案 4: 查询改写 — Query Expansion 多子查询

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"welink/backend/service"
)

// formatFactForRerank 将 fact 开头的 [时间范围] 简化为只含日期的格式。
// 例如 "[2026-05-10 23:46 ~ 2026-05-11 00:28] 陈舒汀表示..." → "[2026-05-10 ~ 2026-05-11] 陈舒汀表示..."
// 同日范围简化为 "[2026-05-10] 陈舒汀表示..."
// reranker 需要日期信息来响应时间相关查询（如"2025年7月"），但具体时间是噪声。
func formatFactForRerank(fact string) string {
	if len(fact) == 0 || fact[0] != '[' {
		return fact
	}
	closeIdx := strings.Index(fact, "]")
	if closeIdx < 0 {
		return fact
	}
	// inner 格式: "2026-05-10 23:46 ~ 2026-05-11 00:28"
	inner := fact[1:closeIdx]
	parts := strings.SplitN(inner, "~", 2)
	if len(parts) != 2 {
		return fact
	}
	startDate := extractDate(parts[0])
	endDate := extractDate(parts[1])
	if startDate == "" && endDate == "" {
		return fact
	}
	var dateStr string
	if startDate == endDate || endDate == "" {
		dateStr = "[" + startDate + "]"
	} else if startDate == "" {
		dateStr = "[" + endDate + "]"
	} else {
		dateStr = "[" + startDate + " ~ " + endDate + "]"
	}
	return dateStr + strings.TrimSpace(fact[closeIdx+1:])
}

// extractDate 从 "2026-05-10 23:46" 中提取日期 "2026-05-10"
func extractDate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 10 {
		return s[:10]
	}
	return ""
}

// ─── 方案 1: BM25 混合检索 ─────────────────────────────────────────────────────

// SearchMemFactsBM25 在 mem_facts_fts 中执行 BM25 关键词检索。
// 与 SearchMemFactsFiltered（向量语义检索）互补：BM25 擅长人名、专有名词、数字的精确匹配。
// 返回按 BM25 相关性降序排列的 top-K 事实。
func SearchMemFactsBM25(key, query string, topK int, timeFrom, timeTo string) ([]MemFact, error) {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return nil, nil
	}

	ftsQuery, likeTerms := prepareFTSQuery(query)
	if ftsQuery == "" && len(likeTerms) == 0 {
		return nil, nil
	}

	type scored struct {
		fact       string
		contactKey string
		sourceFrom int
		sourceTo   int
		score      float64
	}
	var candidates []scored

	// FTS5 BM25 检索（trigram tokenizer）
	if ftsQuery != "" {
		var rows *sql.Rows
		var err error
		if key != "" {
			rows, err = db.Query(`
				SELECT m.contact_key, m.fact, m.source_from, m.source_to, bm25(mem_facts_fts) as score
				FROM mem_facts_fts
				JOIN mem_facts m ON m.id = mem_facts_fts.rowid
				WHERE mem_facts_fts MATCH ? AND mem_facts_fts.contact_key = ?
				ORDER BY score
				LIMIT ?`,
				ftsQuery, key, topK*2)
		} else {
			rows, err = db.Query(`
				SELECT m.contact_key, m.fact, m.source_from, m.source_to, bm25(mem_facts_fts) as score
				FROM mem_facts_fts
				JOIN mem_facts m ON m.id = mem_facts_fts.rowid
				WHERE mem_facts_fts MATCH ?
				ORDER BY score
				LIMIT ?`,
				ftsQuery, topK*2)
		}
		if err == nil {
			for rows.Next() {
				var s scored
				rows.Scan(&s.contactKey, &s.fact, &s.sourceFrom, &s.sourceTo, &s.score)
				// bm25() 返回负值（越小越好），取反使越大越好
				s.score = -s.score

				// 时间过滤：区间重叠判断
				if timeFrom != "" || timeTo != "" {
					factStart := factTimeStart(s.fact)
					if factStart != "" {
						factEnd := factTimeEnd(s.fact)
						if factEnd == "" {
							factEnd = factStart
						}
						if timeFrom != "" && factEnd < timeFrom+" 00:00" {
							continue
						}
						if timeTo != "" && factStart > timeTo+" 23:59" {
							continue
						}
					}
				}
				candidates = append(candidates, s)
			}
			rows.Close()
		}
	}

	// LIKE 兜底（2 字符短词）
	for _, term := range likeTerms {
		if len(candidates) >= topK*2 {
			break
		}
		pattern := "%" + term + "%"
		var rows *sql.Rows
		var err error
		if key != "" {
			rows, err = db.Query(`SELECT contact_key, fact, source_from, source_to FROM mem_facts WHERE contact_key = ? AND fact LIKE ? LIMIT ?`, key, pattern, topK)
		} else {
			rows, err = db.Query(`SELECT contact_key, fact, source_from, source_to FROM mem_facts WHERE fact LIKE ? LIMIT ?`, pattern, topK)
		}
		if err == nil {
			for rows.Next() {
				var s scored
				s.score = 1.0
				rows.Scan(&s.contactKey, &s.fact, &s.sourceFrom, &s.sourceTo)
				if timeFrom != "" || timeTo != "" {
					factStart := factTimeStart(s.fact)
					if factStart != "" {
						factEnd := factTimeEnd(s.fact)
						if factEnd == "" {
							factEnd = factStart
						}
						if timeFrom != "" && factEnd < timeFrom+" 00:00" {
							continue
						}
						if timeTo != "" && factStart > timeTo+" 23:59" {
							continue
						}
					}
				}
				candidates = append(candidates, s)
			}
			rows.Close()
		}
	}

	// 去重 + 排序
	seen := make(map[string]bool)
	var out []MemFact
	for _, c := range candidates {
		if seen[c.fact] {
			continue
		}
		seen[c.fact] = true
		out = append(out, MemFact{
			Fact:       c.fact,
			ContactKey: c.contactKey,
			SourceFrom: c.sourceFrom,
			SourceTo:   c.sourceTo,
		})
		if len(out) >= topK {
			break
		}
	}
	return out, nil
}

// ─── RRF 融合 ──────────────────────────────────────────────────────────────────

// FuseRRF 使用 Reciprocal Rank Fusion 融合多路检索结果。
// 公式: score(doc) = Σ 1/(k + rank(doc))
// k 是平滑常数（默认 60），值越大则排名靠后的结果衰减越慢。
func FuseRRF(rankedLists [][]MemFact, k int) []MemFact {
	if k <= 0 {
		k = 60
	}
	if len(rankedLists) == 0 {
		return nil
	}

	type entry struct {
		fact MemFact
		rrf  float64
	}
	scores := make(map[string]*entry)

	for _, list := range rankedLists {
		for rank, fact := range list {
			key := fact.Fact
			if _, exists := scores[key]; !exists {
				scores[key] = &entry{fact: fact}
			}
			scores[key].rrf += 1.0 / float64(k+rank+1)
		}
	}

	result := make([]MemFact, 0, len(scores))
	for _, e := range scores {
		result = append(result, e.fact)
	}
	sort.Slice(result, func(i, j int) bool {
		return scores[result[i].Fact].rrf > scores[result[j].Fact].rrf
	})
	return result
}

// ─── 方案 3: 双路检索 (mem_facts + vec_messages) ────────────────────────────────

// VecMessageHit 是原始消息的向量检索命中。
type VecMessageHit struct {
	ContactKey string  `json:"contact_key"`
	Seq        int     `json:"seq"`
	Datetime   string  `json:"datetime"`
	Sender     string  `json:"sender"`
	Content    string  `json:"content"`
	Similarity float32 `json:"similarity"`
	SourceName string  `json:"source_name,omitempty"` // 可读来源名（如"群聊「xxx」"），由 API 层填充
}

// SearchVecMessagesFiltered 在 vec_messages 中执行向量语义检索。
// 与 SearchMemFactsFiltered 互补：mem_facts 是压缩事实（精度高但覆盖窄），
// vec_messages 是原始消息（覆盖全但噪音多）。
func SearchVecMessagesFiltered(key, query string, topK int, timeFrom, timeTo string, prefs Preferences) ([]VecMessageHit, error) {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return nil, nil
	}

	cfg, err := currentEmbeddingConfig(prefs)
	if err != nil {
		return nil, err
	}
	queryEmbs, err := GetEmbeddingsBatch([]string{query}, cfg)
	if err != nil || len(queryEmbs) == 0 || queryEmbs[0] == nil {
		return nil, err
	}
	queryVec := queryEmbs[0]

	var rows *sql.Rows
	if key != "" {
		rows, err = db.Query(`SELECT contact_key, seq, datetime, sender, content, embedding FROM vec_messages WHERE contact_key = ?`, key)
	} else {
		rows, err = db.Query(`SELECT contact_key, seq, datetime, sender, content, embedding FROM vec_messages`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scored struct {
		hit VecMessageHit
		sim float32
	}
	var candidates []scored

	for rows.Next() {
		var s scored
		var blob []byte
		if err := rows.Scan(&s.hit.ContactKey, &s.hit.Seq, &s.hit.Datetime, &s.hit.Sender, &s.hit.Content, &blob); err != nil {
			continue
		}
		vec := decodeVec(blob)
		if len(vec) != len(queryVec) {
			continue
		}
		s.sim = cosineSimilarity(queryVec, vec)

		// 时间过滤
		if timeFrom != "" && s.hit.Datetime < timeFrom+" 00:00" {
			continue
		}
		if timeTo != "" && s.hit.Datetime > timeTo+" 23:59" {
			continue
		}

		candidates = append(candidates, s)
	}

	// 过滤低相似度
	const minSim = 0.3
	filtered := candidates[:0]
	for _, c := range candidates {
		if c.sim >= minSim {
			filtered = append(filtered, c)
		}
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].sim > filtered[j].sim
	})

	if len(filtered) > topK {
		filtered = filtered[:topK]
	}

	out := make([]VecMessageHit, len(filtered))
	for i, c := range filtered {
		c.hit.Similarity = c.sim
		out[i] = c.hit
	}
	return out, nil
}

// ─── 方案 4: 查询改写 (Query Expansion) ──────────────────────────────────

// ExpandQuery 用 LLM 将原始查询扩展为多个语义子查询。
// 例如 "张三分手了" → ["张三分手的时间", "张三分手的原因", "张三分手后的状态"]
// 每个子查询分别做向量+BM25检索，结果通过 RRF 融合。
func ExpandQuery(query string, decomp *QueryDecomposition, prefs Preferences, profileID string, svc *service.ContactService) ([]string, error) {
	prompt := buildQueryExpansionPrompt(svc)

	llmMsgs := []LLMMessage{
		{Role: "system", Content: prompt},
		{Role: "user", Content: query},
	}

	type llmResult struct {
		text string
		err  error
	}
	ch := make(chan llmResult, 1)
	go func() {
		text, err := CompleteLLMFeature(llmMsgs, prefs, "query_expansion", aiQAStepProfileID(prefs, "query_expansion", profileID))
		ch <- llmResult{text, err}
	}()

	var result string
	var llmErr error
	select {
	case r := <-ch:
		result = r.text
		llmErr = r.err
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("查询扩展超时")
	}
	if llmErr != nil {
		return nil, llmErr
	}

	raw := strings.TrimSpace(result)
	if start := strings.Index(raw, "["); start >= 0 {
		if end := strings.LastIndex(raw, "]"); end > start {
			raw = raw[start : end+1]
		}
	}

	var subQueries []string
	if err := json.Unmarshal([]byte(raw), &subQueries); err != nil {
		return nil, fmt.Errorf("解析查询扩展结果失败: %w", err)
	}

	// 过滤空字符串和过长的子查询
	var out []string
	for _, sq := range subQueries {
		sq = strings.TrimSpace(sq)
		if sq != "" && len([]rune(sq)) <= 100 {
			out = append(out, sq)
		}
	}
	return out, nil
}

// buildQueryExpansionPrompt 组装查询扩展的系统提示。
// 检索事实里可能只出现真实姓名，外号/简称无法直接命中，
// 因此把置顶记忆和外号对照表一并喂给查询扩展，要求把外号还原为原名。
func buildQueryExpansionPrompt(svc *service.ContactService) string {
	pinned := pinnedMemoryBlock(svc)
	aliases := contactAliasBlock(svc)

	return fmt.Sprintf(`你是查询扩展助手。将用户的查询扩展为 3-5 个语义相关但表述不同的子查询，用于多路检索召回。
每个子查询应从不同角度覆盖原始查询的意图。
查询中如果出现外号、简称或昵称，必须依据下面的映射关系把它还原为对应的真实姓名，再用真实姓名生成子查询；不要使用外号/简称。

%s%s

输出严格 JSON 数组，不要任何解释或代码围栏：
["子查询1", "子查询2", "子查询3"]`, aliases, pinned)
}

// ─── 整合：增强检索 ─────────────────────────────────────────────────────────────

// RawExcerpt 是从原始聊天记录精确检索到的一条原文（找原文场景）。
type RawExcerpt struct {
	SourceName string `json:"source_name"`           // 联系人/群聊可读名或 contact_key
	ContactKey string `json:"contact_key,omitempty"` // 原始 contact_key，用于飞书群白名单过滤
	Datetime   string `json:"datetime"`
	Sender     string `json:"sender"`
	Content    string `json:"content"`
	Seq        int    `json:"seq,omitempty"`
}

// EnhancedRetrievalResult 是增强检索的结果。
type EnhancedRetrievalResult struct {
	Facts           []MemFact         // RRF 融合 + rerank 后的 top-K 事实
	VecMessages     []VecMessageHit   // 双路检索：原始消息命中
	RawHits         []RawExcerpt      // 找原文场景：原始消息精确命中
	ExpandedQueries []string          // 查询改写：扩展的子查询
	RerankUsed      bool              // 是否使用了 rerank
	RerankResults   []RerankScoreItem // rerank 精排结果（按分数降序）
	VectorHits      int               // 向量检索命中数
	BM25Hits        int               // BM25 检索命中数
	VecMessageHits  int               // 原始消息检索命中数
}

// RerankScoreItem 是 rerank 精排的单条结果（分数 + 对应文本）。
type RerankScoreItem struct {
	Score float32 `json:"score"`
	Text  string  `json:"text"`
}

// searchRawMessages 在原始聊天记录中精确查找 query 出现的原文（找原文场景）。
// 联系人走 svc.SearchMessages（含自己发的消息），群聊走 svc.SearchGroupMessages。
func searchRawMessages(query string, searchKeys []string, svc *service.ContactService) []RawExcerpt {
	if svc == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	var out []RawExcerpt
	seen := make(map[string]bool)
	for _, key := range searchKeys {
		var excerpt RawExcerpt
		sourceName := key
		if strings.HasPrefix(key, "contact:") {
			uname := strings.TrimPrefix(key, "contact:")
			sourceName = resolveSourceName(key, svc)
			for _, m := range svc.SearchMessages(uname, query, true) {
				if m.Content == "" {
					continue
				}
				excerpt = RawExcerpt{SourceName: sourceName, Datetime: m.Date + " " + m.Time, Sender: "我"}
				if !m.IsMine {
					excerpt.Sender = resolveContactName(svc, uname)
				}
				excerpt.Content = m.Content
				dedupe := excerpt.SourceName + "|" + excerpt.Datetime + "|" + excerpt.Content
				if seen[dedupe] {
					continue
				}
				seen[dedupe] = true
				out = append(out, excerpt)
			}
		}
		if strings.HasPrefix(key, "group:") {
			uname := strings.TrimPrefix(key, "group:")
			sourceName = resolveSourceName(key, svc)
			for _, m := range svc.SearchGroupMessages(uname, query, "") {
				if m.Content == "" {
					continue
				}
				excerpt = RawExcerpt{SourceName: sourceName, Datetime: m.Date + " " + m.Time, Sender: "我"}
				if !m.IsMine {
					excerpt.Sender = m.Speaker
				} else {
					excerpt.Sender = "我"
				}
				excerpt.Content = m.Content
				dedupe := excerpt.SourceName + "|" + excerpt.Datetime + "|" + excerpt.Sender + "|" + excerpt.Content
				if seen[dedupe] {
					continue
				}
				seen[dedupe] = true
				out = append(out, excerpt)
			}
		}
	}
	return out
}

// resolveContactName 返回联系人可读名（备注/昵称优先）。
func resolveContactName(svc *service.ContactService, username string) string {
	for _, s := range svc.GetCachedStats() {
		if s.Username == username {
			if s.Remark != "" {
				return s.Remark
			}
			if s.Nickname != "" {
				return s.Nickname
			}
		}
	}
	return username
}

// searchRawMessagesFTS 在没有可解析 key 时，用 msg_fts 对全库做原文精确检索。
func searchRawMessagesFTS(query string, svc *service.ContactService) []RawExcerpt {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil || strings.TrimSpace(query) == "" {
		return nil
	}

	rows, err := db.Query(`SELECT DISTINCT contact_key FROM msg_fts`)
	if err != nil {
		return nil
	}
	var keys []string
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil && k != "" {
			keys = append(keys, k)
		}
	}
	rows.Close()

	var out []RawExcerpt
	seen := make(map[string]bool)
	for _, key := range keys {
		ftsResults, _, err := SearchFTS(key, query, 20)
		if err != nil {
			continue
		}
		sourceName := resolveSourceName(key, svc)
		for _, r := range ftsResults {
			if r.Content == "" {
				continue
			}
			e := RawExcerpt{
				SourceName: sourceName,
				Datetime:   r.Datetime,
				Sender:     r.Sender,
				Content:    r.Content,
				Seq:        r.Seq,
			}
			dedupe := e.SourceName + "|" + e.Datetime + "|" + e.Sender + "|" + e.Content
			if seen[dedupe] {
				continue
			}
			seen[dedupe] = true
			out = append(out, e)
			if len(out) >= 100 {
				return out
			}
		}
	}
	return out
}

// conversationMemorySearchData 是 AI 会话消息里记忆检索详情的 JSON 透传结构，
// 只读取与本功能相关的原文候选字段。
type conversationMemorySearchData struct {
	Sources []struct {
		SourceName string `json:"source_name"`
		Messages   []struct {
			Seq      int    `json:"seq"`
			Datetime string `json:"datetime"`
			Sender   string `json:"sender"`
			Content  string `json:"content"`
		} `json:"messages"`
	} `json:"sources"`
	VecMessages []struct {
		ContactKey string  `json:"contact_key"`
		Seq        int     `json:"seq"`
		Datetime   string  `json:"datetime"`
		Sender     string  `json:"sender"`
		Content    string  `json:"content"`
		Similarity float32 `json:"similarity"`
	} `json:"vec_messages"`
	RawHits []RawExcerpt `json:"raw_hits"`
}

// collectConversationRawCandidates 从已保存的 AI 会话里，把此前各轮
// memorySearchData 中已检索到的原文候选（raw_hits / vec_messages / sources）
// 收集出来，作为本轮“找原文”追问的可引用原文。数据唯一来源在后端。
func collectConversationRawCandidates(convKey string) []RawExcerpt {
	if convKey == "" {
		return nil
	}
	msgs, err := GetAIConversation(convKey)
	if err != nil || len(msgs) == 0 {
		return nil
	}
	out := make([]RawExcerpt, 0, 64)
	seen := make(map[string]bool)
	add := func(sourceName, dt, sender, content string) {
		if content == "" {
			return
		}
		key := sourceName + "|" + dt + "|" + sender + "|" + content
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, RawExcerpt{SourceName: sourceName, Datetime: dt, Sender: sender, Content: content})
	}
	for _, m := range msgs {
		if m.Role != "assistant" || len(m.MemorySearchData) == 0 {
			continue
		}
		var data conversationMemorySearchData
		if json.Unmarshal(m.MemorySearchData, &data) != nil {
			continue
		}
		for _, rh := range data.RawHits {
			add(rh.SourceName, rh.Datetime, rh.Sender, rh.Content)
		}
		for _, vm := range data.VecMessages {
			sourceName := vm.ContactKey
			if sourceName == "" {
				sourceName = "未知"
			}
			add(sourceName, vm.Datetime, vm.Sender, vm.Content)
		}
		for _, src := range data.Sources {
			for _, msg := range src.Messages {
				add(src.SourceName, msg.Datetime, msg.Sender, msg.Content)
			}
		}
		if len(out) >= 200 {
			break
		}
	}
	return out
}

// EnhancedRetrieval 整合 BM25 + 双路检索 + 查询改写 + Rerank 的增强检索。
//
// 流程：
//  1. 查询改写（可选）：ExpandQuery 生成子查询
//  2. 多路检索：对每个 searchKey，同时做向量语义检索 + BM25 关键词检索 + 原始消息检索
//  3. RRF 融合：将向量+BM25 两路结果融合
//  4. Rerank 精排（可选）：对融合后的 top-50 偙选做 cross-encoder 精排
//  5. 返回 top-K 事实 + 原始消息命中
func EnhancedRetrieval(
	query string,
	decomp *QueryDecomposition,
	searchKeys []string,
	timeFrom, timeTo string,
	prefs Preferences,
	profileID string,
	svc *service.ContactService,
	onProgress func(step, detail string),
) (*EnhancedRetrievalResult, error) {
	progress := func(step, detail string) {
		if onProgress != nil {
			onProgress(step, detail)
		}
	}
	result := &EnhancedRetrievalResult{}

	// 确定检索用的查询词
	searchQ := query
	if decomp != nil && len(decomp.Concepts) > 0 {
		searchQ = strings.Join(decomp.Concepts, " ")
	}

	// ── Step 1: 查询改写（可选）──
	// 只有配置了 LLM 时才做查询改写
	hasLLM := hasLLMConfig(prefs)
	if hasLLM {
		modelName := aiQAStepModelNames(prefs, profileID).QueryExpansion
		progress("query_expansion", fmt.Sprintf("正在使用 %s 扩展子查询...", modelName))
		// Query Expansion: 生成子查询
		subQueries, err := ExpandQuery(query, decomp, prefs, profileID, svc)
		if err == nil && len(subQueries) > 0 {
			result.ExpandedQueries = subQueries
			progress("query_expansion_result", "查询扩展结果："+strings.Join(subQueries, "；"))
		}
	}

	// ── Step 2: 多路检索 ──
	// 提高候选规模：人物级问答/实体共现场景下，若 topK 太小，
	// 真正“人名 × 事件”同时命中的事实会在 RRF/rerank 前就被挤掉。
	const perKeyVecTopK = 200
	const perKeyBM25TopK = 200
	const perKeyVecMsgTopK = 200
	const maxFacts = 400
	const finalTopK = 50

	var allVecFacts []MemFact
	var allBM25Facts []MemFact
	var allVecMessages []VecMessageHit

	// 实体（人名）+ 概念（语义事件）共现召回，保证这类事实不被淹没。
	// 概念词除 LLM 分解出的 concepts 外，还纳入 LLM 生成的 search_terms（近义词/口语），
	// 让 LIKE 共现能覆盖群友的不同说法（如“锐评”“吐槽”“怎么看”）。
	var entities []string
	var conceptTerms []string
	if decomp != nil {
		entities = decomp.Entities
		conceptTerms = mergeSearchTerms(decomp.Concepts, decomp.SearchTerms)
	}

	step2Start := time.Now()

	// 对每个 searchKey 做多路检索
	for ki, key := range searchKeys {
		keyStart := time.Now()
		progress("vector_search", fmt.Sprintf("[%d/%d] 向量检索 (concepts: %s)", ki+1, len(searchKeys), truncate(searchQ, 40)))
		// 向量语义检索（concepts）
		vecFacts, _ := SearchMemFactsFiltered(key, searchQ, perKeyVecTopK, timeFrom, timeTo, prefs)
		allVecFacts = append(allVecFacts, vecFacts...)

		// 向量语义检索（原始 query，补充关键词维度）
		if query != searchQ {
			progress("vector_search", fmt.Sprintf("[%d/%d] 向量检索 (原始query: %s)", ki+1, len(searchKeys), truncate(query, 40)))
			origVecFacts, _ := SearchMemFactsFiltered(key, query, perKeyVecTopK, timeFrom, timeTo, prefs)
			allVecFacts = append(allVecFacts, origVecFacts...)
		}

		progress("bm25_search", fmt.Sprintf("[%d/%d] BM25 关键词检索 (query: %s)", ki+1, len(searchKeys), truncate(query, 40)))
		// BM25 关键词检索（原始 query，保留专有名词/数字）
		bm25Facts, _ := SearchMemFactsBM25(key, query, perKeyBM25TopK, timeFrom, timeTo)
		allBM25Facts = append(allBM25Facts, bm25Facts...)

		progress("vecmsg_search", fmt.Sprintf("[%d/%d] 原始消息向量检索 (query: %s)", ki+1, len(searchKeys), truncate(searchQ, 40)))
		// 双路检索：原始消息向量检索
		vecMsgs, _ := SearchVecMessagesFiltered(key, searchQ, perKeyVecMsgTopK, timeFrom, timeTo, prefs)
		allVecMessages = append(allVecMessages, vecMsgs...)
		log.Printf("[enhanced] key=%s: vec=%d bm25=%d vecMsg=%d, %dms", key, len(vecFacts), len(bm25Facts), len(vecMsgs), time.Since(keyStart).Milliseconds())
	}

	// 无 searchKey 时做全局搜索
	if len(searchKeys) == 0 {
		vecFacts, _ := SearchMemFactsFiltered("", searchQ, perKeyVecTopK, timeFrom, timeTo, prefs)
		allVecFacts = append(allVecFacts, vecFacts...)

		if query != searchQ {
			origVecFacts, _ := SearchMemFactsFiltered("", query, perKeyVecTopK, timeFrom, timeTo, prefs)
			allVecFacts = append(allVecFacts, origVecFacts...)
		}

		bm25Facts, _ := SearchMemFactsBM25("", query, perKeyBM25TopK, timeFrom, timeTo)
		allBM25Facts = append(allBM25Facts, bm25Facts...)

		vecMsgs, _ := SearchVecMessagesFiltered("", searchQ, perKeyVecMsgTopK, timeFrom, timeTo, prefs)
		allVecMessages = append(allVecMessages, vecMsgs...)
	}

	// 对扩展子查询同时做向量 + BM25 检索（结果合并到候选池）。
	// 查询扩展会把外号还原为真实姓名（如“土鲫鱼”→“李佳轩”），
	// 但原始 query 的 BM25 仍用外号检索，无法命中“李佳轩”字样的记忆；
	// 因此必须用扩展后的真实姓名再走一次 BM25 关键词检索，否则实体会被漏掉。
	if len(result.ExpandedQueries) > 0 {
		expStart := time.Now()
		for si, sq := range result.ExpandedQueries {
			progress("expanded_search", fmt.Sprintf("[%d/%d] 扩展子查询检索: %s", si+1, len(result.ExpandedQueries), truncate(sq, 40)))
			for _, key := range searchKeys {
				facts, _ := SearchMemFactsFiltered(key, sq, 20, timeFrom, timeTo, prefs)
				allVecFacts = append(allVecFacts, facts...)
				bm25Facts, _ := SearchMemFactsBM25(key, sq, 20, timeFrom, timeTo)
				allBM25Facts = append(allBM25Facts, bm25Facts...)
			}
			if len(searchKeys) == 0 {
				facts, _ := SearchMemFactsFiltered("", sq, 20, timeFrom, timeTo, prefs)
				allVecFacts = append(allVecFacts, facts...)
				bm25Facts, _ := SearchMemFactsBM25("", sq, 20, timeFrom, timeTo)
				allBM25Facts = append(allBM25Facts, bm25Facts...)
			}
		}
		log.Printf("[enhanced] expanded queries retrieval: %d subQueries, %dms", len(result.ExpandedQueries), time.Since(expStart).Milliseconds())
	}

	// 实体 × 概念共现召回（全局一次）：优先保证“邓凯文 + 装逼/炫耀”这类
	// 事实进入候选池。一个 LIKE 查询覆盖全部 contact_key，避免按 key 重复扫描。
	if len(entities) > 0 && len(conceptTerms) > 0 {
		progress("comention_search", "实体×概念共现检索（全局）")
		comention := SearchMemFactsCoMention("", entities, conceptTerms, 300, timeFrom, timeTo)
		allVecFacts = append(allVecFacts, comention...)
	}

	log.Printf("[enhanced] Step 2 total: vec=%d bm25=%d vecMsg=%d, %dms",
		len(allVecFacts), len(allBM25Facts), len(allVecMessages), time.Since(step2Start).Milliseconds())

	result.VectorHits = len(allVecFacts)
	result.BM25Hits = len(allBM25Facts)
	result.VecMessageHits = len(allVecMessages)

	// ── Step 3: RRF 融合 ──
	progress("rrf_fusion", fmt.Sprintf("RRF 融合 %d 条向量 + %d 条BM25 候选...", len(allVecFacts), len(allBM25Facts)))
	// 融合两路结果：向量检索 + BM25 检索
	fusedFacts := FuseRRF([][]MemFact{allVecFacts, allBM25Facts}, 120)
	if len(fusedFacts) > maxFacts {
		fusedFacts = fusedFacts[:maxFacts]
	}

	// ── Step 4: Rerank 精排（可选）──
	rerankCfgs := rerankConfigs(prefs)
	log.Printf("[enhanced] rerank configs=%d, fusedFacts=%d, vecHits=%d, bm25Hits=%d, vecMsgHits=%d",
		len(rerankCfgs), len(fusedFacts), result.VectorHits, result.BM25Hits, result.VecMessageHits)
	if len(rerankCfgs) > 0 && len(fusedFacts) > 1 {
		progress("rerank", fmt.Sprintf("正在用 rerank 精排 %d 条候选...", len(fusedFacts)))
		docs := make([]string, len(fusedFacts))
		for i, f := range fusedFacts {
			docs[i] = formatFactForRerank(f.Fact)
		}
		// 结构化 rerank query：明示实体（主体）与概念（事件）约束，
		// 避免 reranker 只被高频字面词（如"装逼"）带偏，忽略“谁的故事”。
		today := time.Now().Format("2006-01-02")
		rankQuery := query
		if decomp != nil && (len(decomp.Entities) > 0 || len(decomp.Concepts) > 0) {
			var parts []string
			if len(decomp.Entities) > 0 {
				parts = append(parts, "主体人物/群: "+strings.Join(decomp.Entities, "、"))
			}
			if len(decomp.Concepts) > 0 {
				parts = append(parts, "核心事件/主题: "+strings.Join(decomp.Concepts, "、"))
			}
			lookup := ""
			if decomp.LookupRaw {
				lookup = "，候选须是能作为原始佐证的聊天记录"
			}
			rankQuery = fmt.Sprintf("%s。约束：%s%s", query, strings.Join(parts, "；"), lookup)
		}
		rerankQuery := fmt.Sprintf("[当前日期: %s] %s", today, rankQuery)
		rerankResults, err := RerankCandidatesForCurrentProfile(rerankQuery, docs, rerankCfgs)
		if err == nil && len(rerankResults) > 0 {
			type indexed struct {
				idx   int
				score float32
			}
			order := make([]indexed, len(rerankResults))
			for i, r := range rerankResults {
				order[i] = indexed{idx: r.Index, score: r.Score}
			}
			sort.Slice(order, func(i, j int) bool {
				return order[i].score > order[j].score
			})
			reranked := make([]MemFact, 0, len(order))
			rerankScoreItems := make([]RerankScoreItem, 0, len(order))
			for _, o := range order {
				if o.idx >= 0 && o.idx < len(fusedFacts) {
					reranked = append(reranked, fusedFacts[o.idx])
					rerankScoreItems = append(rerankScoreItems, RerankScoreItem{
						Score: o.score,
						Text:  docs[o.idx],
					})
				}
			}
			fusedFacts = reranked
			result.RerankUsed = true
			result.RerankResults = rerankScoreItems
		}
	}

	if len(fusedFacts) > finalTopK {
		fusedFacts = fusedFacts[:finalTopK]
	}

	result.Facts = fusedFacts

	// vec_messages 按 similarity 取 top-100
	const maxVecMessages = 100
	if len(allVecMessages) > maxVecMessages {
		sort.Slice(allVecMessages, func(i, j int) bool {
			return allVecMessages[i].Similarity > allVecMessages[j].Similarity
		})
		allVecMessages = allVecMessages[:maxVecMessages]
	}
	result.VecMessages = allVecMessages

	// 找原文场景：额外走原始聊天记录精确检索，拿到能直接引用/展示的原文。
	// 只有用户明确要求“找原文/原话/贴出来”时才做整句精确检索（开销较大）。
	needRawLookup := decomp != nil && decomp.LookupRaw
	if needRawLookup {
		progress("raw_lookup", fmt.Sprintf("按原文精确检索 query: %s", truncate(query, 40)))
		rawHits := searchRawMessages(query, searchKeys, svc)
		if len(searchKeys) == 0 {
			// 没有可解析的 key 时做全库精确检索（msg_fts）
			rawHits = append(rawHits, searchRawMessagesFTS(query, svc)...)
		}
		// 兜底：即使整句关键词没精确命中，也从命中的记忆事实里取 source 原文。
		if len(rawHits) == 0 && len(result.Facts) > 0 {
			rawHits = append(rawHits, extractRawFromFacts(result.Facts, svc)...)
		}
		result.RawHits = rawHits
	} else if needsEvidenceRaw(decomp) && len(result.Facts) > 0 {
		// 评价/看法/开放性类问题：即使用户没明说“找原文”，也要把
		// 命中的记忆事实对应的聊天记录原文带上，作为回答的依据。
		// 否则 LLM 只能看到压缩摘要，看不到“谁怎么评价谁”的具体原文。
		progress("raw_lookup", "从命中的记忆事实提取源聊天记录作为评价佐证")
		result.RawHits = extractRawFromFacts(result.Facts, svc)
	}

	return result, nil
}

// extractRawFromFacts 从命中的记忆事实提取对应的源聊天记录，去重后返回。
func extractRawFromFacts(facts []MemFact, svc *service.ContactService) []RawExcerpt {
	if len(facts) == 0 {
		return nil
	}
	sources, _ := ExtractFactSources(facts, svc)
	var out []RawExcerpt
	seen := make(map[string]bool)
	for _, src := range sources {
		for _, m := range src.Messages {
			excerpt := RawExcerpt{
				SourceName: src.SourceName,
				ContactKey: src.Fact.ContactKey,
				Datetime:   m.Datetime,
				Sender:     m.Sender,
				Content:    m.Content,
				Seq:        m.Seq,
			}
			dedupe := excerpt.SourceName + "|" + excerpt.Datetime + "|" + excerpt.Sender + "|" + excerpt.Content
			if seen[dedupe] {
				continue
			}
			seen[dedupe] = true
			out = append(out, excerpt)
			if len(out) >= 100 {
				return out
			}
		}
	}
	return out
}

// needsEvidenceRaw 判断该类型的查询即使没明说“找原文”，也应附带评价原文佐证。
func needsEvidenceRaw(decomp *QueryDecomposition) bool {
	if decomp == nil {
		return false
	}
	for _, c := range decomp.Concepts {
		if containsAny(c, []string{"锐评", "评价", "看法", "吐槽", "直言", "怎么样", "如何", "怎么", "评价怎么样"}) {
			return true
		}
	}
	return false
}

// mergeSearchTerms 把 LLM 分解出的 concepts 与 search_terms 合并去重，
// 供 LIKE 共现检索使用。search_terms 往往带口语/近义词，能覆盖硬编码词表
// 无法穷举的各种说法。
func mergeSearchTerms(concepts, searchTerms []string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(terms []string) {
		for _, t := range terms {
			t = strings.TrimSpace(t)
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
		}
	}
	add(concepts)
	add(searchTerms)
	return out
}

// containsAny 判断 s 是否包含 candidates 中的任意子串。
func containsAny(s string, candidates []string) bool {
	for _, c := range candidates {
		if c != "" && strings.Contains(s, c) {
			return true
		}
	}
	return false
}
