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

	cfg := defaultEmbeddingConfig(prefs)
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
func ExpandQuery(query string, decomp *QueryDecomposition, prefs Preferences, profileID string) ([]string, error) {
	prompt := `你是查询扩展助手。将用户的查询扩展为 3-5 个语义相关但表述不同的子查询，用于多路检索召回。
每个子查询应从不同角度覆盖原始查询的意图。

输出严格 JSON 数组，不要任何解释或代码围栏：
["子查询1", "子查询2", "子查询3"]`

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
		text, err := CompleteLLMFeature(llmMsgs, prefs, "query_expansion", profileID)
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

// ─── 整合：增强检索 ─────────────────────────────────────────────────────────────

// EnhancedRetrievalResult 是增强检索的结果。
type EnhancedRetrievalResult struct {
	Facts           []MemFact         // RRF 融合 + rerank 后的 top-K 事实
	VecMessages     []VecMessageHit   // 双路检索：原始消息命中
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

// EnhancedRetrieval 整合 BM25 + 双路检索 + 查询改写 + Rerank 的增强检索。
//
// 流程：
//   1. 查询改写（可选）：ExpandQuery 生成子查询
//   2. 多路检索：对每个 searchKey，同时做向量语义检索 + BM25 关键词检索 + 原始消息检索
//   3. RRF 融合：将向量+BM25 两路结果融合
//   4. Rerank 精排（可选）：对融合后的 top-50 偙选做 cross-encoder 精排
//   5. 返回 top-K 事实 + 原始消息命中
func EnhancedRetrieval(
	query string,
	decomp *QueryDecomposition,
	searchKeys []string,
	timeFrom, timeTo string,
	prefs Preferences,
	profileID string,
) (*EnhancedRetrievalResult, error) {
	result := &EnhancedRetrievalResult{}

	// 确定检索用的查询词
	searchQ := query
	if decomp != nil && len(decomp.Concepts) > 0 {
		searchQ = strings.Join(decomp.Concepts, " ")
	}

	// ── Step 1: 查询改写（可选）──
	// 只有配置了 LLM 时才做查询改写
	hasLLM := prefs.LLMProvider != "" || len(prefs.LLMProfiles) > 0
	if hasLLM {
		// Query Expansion: 生成子查询
		subQueries, err := ExpandQuery(query, decomp, prefs, profileID)
		if err == nil && len(subQueries) > 0 {
			result.ExpandedQueries = subQueries
		}
	}

	// ── Step 2: 多路检索 ──
	const perKeyVecTopK = 50
	const perKeyBM25TopK = 50
	const perKeyVecMsgTopK = 100
	const maxFacts = 100
	const finalTopK = 50

	var allVecFacts []MemFact
	var allBM25Facts []MemFact
	var allVecMessages []VecMessageHit

	step2Start := time.Now()

	// 对每个 searchKey 做多路检索
	for _, key := range searchKeys {
		keyStart := time.Now()
		// 向量语义检索（concepts）
		vecFacts, _ := SearchMemFactsFiltered(key, searchQ, perKeyVecTopK, timeFrom, timeTo, prefs)
		allVecFacts = append(allVecFacts, vecFacts...)

		// 向量语义检索（原始 query，补充关键词维度）
		if query != searchQ {
			origVecFacts, _ := SearchMemFactsFiltered(key, query, perKeyVecTopK, timeFrom, timeTo, prefs)
			allVecFacts = append(allVecFacts, origVecFacts...)
		}

		// BM25 关键词检索（原始 query，保留专有名词/数字）
		bm25Facts, _ := SearchMemFactsBM25(key, query, perKeyBM25TopK, timeFrom, timeTo)
		allBM25Facts = append(allBM25Facts, bm25Facts...)

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

	// 对扩展子查询也做一路向量检索（结果合并到 allVecFacts）
	if len(result.ExpandedQueries) > 0 {
		expStart := time.Now()
		for _, sq := range result.ExpandedQueries {
			for _, key := range searchKeys {
				facts, _ := SearchMemFactsFiltered(key, sq, 20, timeFrom, timeTo, prefs)
				allVecFacts = append(allVecFacts, facts...)
			}
			if len(searchKeys) == 0 {
				facts, _ := SearchMemFactsFiltered("", sq, 20, timeFrom, timeTo, prefs)
				allVecFacts = append(allVecFacts, facts...)
			}
		}
		log.Printf("[enhanced] expanded queries retrieval: %d subQueries, %dms", len(result.ExpandedQueries), time.Since(expStart).Milliseconds())
	}

	log.Printf("[enhanced] Step 2 total: vec=%d bm25=%d vecMsg=%d, %dms",
		len(allVecFacts), len(allBM25Facts), len(allVecMessages), time.Since(step2Start).Milliseconds())

	result.VectorHits = len(allVecFacts)
	result.BM25Hits = len(allBM25Facts)
	result.VecMessageHits = len(allVecMessages)

	// ── Step 3: RRF 融合 ──
	// 融合两路结果：向量检索 + BM25 检索
	fusedFacts := FuseRRF([][]MemFact{allVecFacts, allBM25Facts}, 60)
	if len(fusedFacts) > maxFacts {
		fusedFacts = fusedFacts[:maxFacts]
	}

	// ── Step 4: Rerank 精排（可选）──
	rerankCfgs := rerankConfigs(prefs)
	log.Printf("[enhanced] rerank configs=%d, fusedFacts=%d, vecHits=%d, bm25Hits=%d, vecMsgHits=%d",
		len(rerankCfgs), len(fusedFacts), result.VectorHits, result.BM25Hits, result.VecMessageHits)
	if len(rerankCfgs) > 0 && len(fusedFacts) > 1 {
		docs := make([]string, len(fusedFacts))
		for i, f := range fusedFacts {
			docs[i] = formatFactForRerank(f.Fact)
		}
		// 在 rerank query 前加上当前日期，让 reranker 理解相对时间词（如"最近"）
		today := time.Now().Format("2006-01-02")
		rerankQuery := fmt.Sprintf("[当前日期: %s] %s", today, query)
		rerankResults, err := RerankCandidatesWithFallback(rerankQuery, docs, rerankCfgs)
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

	return result, nil
}
