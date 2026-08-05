package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ─── 表初始化 ─────────────────────────────────────────────────────────────────

// initMemTables 在 aiDB 中创建记忆事实表。
// 必须在 aiDBMu 持有期间调用（由 InitAIDB 调用）。
func initMemTables() error {
	_, err := aiDB.Exec(`CREATE TABLE IF NOT EXISTS mem_facts (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		contact_key TEXT    NOT NULL,
		fact        TEXT    NOT NULL,
		source_from INTEGER NOT NULL DEFAULT 0,
		source_to   INTEGER NOT NULL DEFAULT 0,
		embedding   BLOB    NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("mem: mem_facts: %w", err)
	}
	_, err = aiDB.Exec(`CREATE INDEX IF NOT EXISTS idx_mem_contact ON mem_facts(contact_key)`)
	if err != nil {
		return fmt.Errorf("mem: idx_mem_contact: %w", err)
	}
	// 迁移：给老库补 pinned / created_at / updated_at 列
	if err := addColumnIfMissing("mem_facts", "pinned", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("mem: pinned col: %w", err)
	}
	if err := addColumnIfMissing("mem_facts", "created_at", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("mem: created_at col: %w", err)
	}
	if err := addColumnIfMissing("mem_facts", "updated_at", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("mem: updated_at col: %w", err)
	}
	if err := addColumnIfMissing("mem_facts", "version", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return fmt.Errorf("mem: version col: %w", err)
	}

	// FTS5 虚拟表：对 mem_facts.fact 做 BM25 关键词检索（混合检索用）
	if _, err := aiDB.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS mem_facts_fts USING fts5(
		fact,
		contact_key UNINDEXED,
		content='mem_facts',
		content_rowid='id',
		tokenize = 'trigram'
	)`); err != nil {
		return fmt.Errorf("mem: mem_facts_fts: %w", err)
	}
	// 触发器：保持 mem_facts_fts 与 mem_facts 同步
	for _, t := range []string{
		`CREATE TRIGGER IF NOT EXISTS mem_facts_ai AFTER INSERT ON mem_facts BEGIN
			INSERT INTO mem_facts_fts(rowid, fact, contact_key) VALUES (new.id, new.fact, new.contact_key);
		END`,
		`CREATE TRIGGER IF NOT EXISTS mem_facts_ad AFTER DELETE ON mem_facts BEGIN
			INSERT INTO mem_facts_fts(mem_facts_fts, rowid, fact, contact_key) VALUES ('delete', old.id, old.fact, old.contact_key);
		END`,
		`CREATE TRIGGER IF NOT EXISTS mem_facts_au AFTER UPDATE ON mem_facts BEGIN
			INSERT INTO mem_facts_fts(mem_facts_fts, rowid, fact, contact_key) VALUES ('delete', old.id, old.fact, old.contact_key);
			INSERT INTO mem_facts_fts(rowid, fact, contact_key) VALUES (new.id, new.fact, new.contact_key);
		END`,
	} {
		if _, err := aiDB.Exec(t); err != nil {
			return fmt.Errorf("mem: trigger: %w", err)
		}
	}
	// 回填：把已有的 mem_facts 行灌入 FTS 表（首次创建时）
	var ftsCount int
	if err := aiDB.QueryRow("SELECT COUNT(*) FROM mem_facts_fts").Scan(&ftsCount); err == nil && ftsCount == 0 {
		var srcCount int
		if err := aiDB.QueryRow("SELECT COUNT(*) FROM mem_facts").Scan(&srcCount); err == nil && srcCount > 0 {
			aiDB.Exec(`INSERT INTO mem_facts_fts(rowid, fact, contact_key) SELECT id, fact, contact_key FROM mem_facts`)
		}
	}

	return nil
}

// addColumnIfMissing 给已有表加列（SQLite 不支持 ADD COLUMN IF NOT EXISTS）。
func addColumnIfMissing(table, column, decl string) error {
	rows, err := aiDB.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	_, err = aiDB.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl))
	return err
}

// ─── 状态查询 ─────────────────────────────────────────────────────────────────

// GetMemFactsCount 返回指定 key 已提炼的事实数量。
func GetMemFactsCount(key string) (int, error) {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return 0, nil
	}
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM mem_facts WHERE contact_key = ? AND version = ?", key, memFactVersion).Scan(&count)
	return count, err
}

// MemFact 是单条记忆事实的展示结构。
type MemFact struct {
	ID         int    `json:"id"`
	ContactKey string `json:"contact_key,omitempty"`
	Fact       string `json:"fact"`
	SourceFrom int    `json:"source_from"`
	SourceTo   int    `json:"source_to"`
	Pinned     bool   `json:"pinned"`
	CreatedAt  int64  `json:"created_at,omitempty"`
	UpdatedAt  int64  `json:"updated_at,omitempty"`
	SourceName string `json:"source_name,omitempty"` // 可读来源名（如"群聊「xxx」"），由 API 层填充
}

// GetMemFacts 返回指定 key 的所有事实（按 id 升序）。
func GetMemFacts(key string) ([]MemFact, error) {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query(
		"SELECT id, fact, source_from, source_to, pinned, created_at, updated_at FROM mem_facts WHERE contact_key = ? AND version = ? ORDER BY pinned DESC, id",
		key, memFactVersion,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var facts []MemFact
	for rows.Next() {
		var f MemFact
		var pinned int
		rows.Scan(&f.ID, &f.Fact, &f.SourceFrom, &f.SourceTo, &pinned, &f.CreatedAt, &f.UpdatedAt)
		f.Pinned = pinned != 0
		facts = append(facts, f)
	}
	if facts == nil {
		facts = []MemFact{}
	}
	return facts, nil
}

// ─── LLM 提炼 ─────────────────────────────────────────────────────────────────

const memExtractChunkSize = 150      // 每段最多 150 条消息（上限，实际段大小由时间和条数共同决定）
const memMaxTimeGap = 24 * time.Hour // 每段最多跨越 24 小时；超过此间隔的消息强行切分到新段

// memFactVersion 是当前记忆提取管线生成的记忆版本号。
// v1 = 旧管线（固定 80 条分段）；v2 = 新管线（摘要链 + 动态分段）。
const memFactVersion = 2

// ─── 上下文摘要链 ─────────────────────────────────────────────────────────────

// memContextSummary 是上一段分析传递给下一段的结构化上下文摘要。
// 用于跨批次消解"他/她/那个事"等指代，避免张冠李戴。
type memContextSummary struct {
	Entities     []memEntity `json:"entities"`              // 人物表
	ActiveTopics []string    `json:"active_topics"`         // 当前话题
	Unresolved   []string    `json:"unresolved_references"` // 未消解指代
}

// memEntity 是上下文摘要中的人物条目。
type memEntity struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
}

// memExtractResult 是 LLM 单批提取的完整结果：事实列表 + 上下文摘要。
type memExtractResult struct {
	Facts          []string          `json:"facts"`
	ContextSummary memContextSummary `json:"context_summary"`
}

// formatPriorSummary 把上一段的上下文摘要格式化为 prompt 可读文本。
func formatPriorSummary(s memContextSummary) string {
	if len(s.Entities) == 0 && len(s.ActiveTopics) == 0 && len(s.Unresolved) == 0 {
		return "（本段是第一段，无前文上下文）"
	}
	var sb strings.Builder
	if len(s.Entities) > 0 {
		sb.WriteString("已知人物：")
		for i, e := range s.Entities {
			if i > 0 {
				sb.WriteString("、")
			}
			sb.WriteString(e.Name)
			if len(e.Aliases) > 0 {
				sb.WriteString("（又称：")
				sb.WriteString(strings.Join(e.Aliases, "、"))
				sb.WriteString("）")
			}
		}
		sb.WriteString("\n")
	}
	if len(s.ActiveTopics) > 0 {
		sb.WriteString("当前话题：")
		sb.WriteString(strings.Join(s.ActiveTopics, "；"))
		sb.WriteString("\n")
	}
	if len(s.Unresolved) > 0 {
		sb.WriteString("未消解指代：")
		sb.WriteString(strings.Join(s.Unresolved, "；"))
		sb.WriteString("\n")
	}
	return sb.String()
}

// capContextSummary 限制摘要体积，防止随批次无限膨胀。
func capContextSummary(s memContextSummary) memContextSummary {
	if len(s.Entities) > 15 {
		s.Entities = s.Entities[:15]
	}
	if len(s.ActiveTopics) > 8 {
		s.ActiveTopics = s.ActiveTopics[:8]
	}
	if len(s.Unresolved) > 5 {
		s.Unresolved = s.Unresolved[:5]
	}
	return s
}

// parseExtractResult 解析 LLM 返回的提取结果，兼容新旧两种格式：
//   - 新格式：{"facts": [...], "context_summary": {...}}
//   - 旧格式：["fact1", "fact2"]
func parseExtractResult(reply string) (memExtractResult, error) {
	reply = strings.TrimSpace(reply)
	var result memExtractResult

	// 优先尝试对象格式（新格式）
	if objStart := strings.Index(reply, "{"); objStart >= 0 {
		objEnd := strings.LastIndex(reply, "}")
		if objEnd > objStart {
			if err := json.Unmarshal([]byte(reply[objStart:objEnd+1]), &result); err == nil {
				return result, nil
			}
		}
	}

	// 回退到数组格式（旧格式）
	if arrStart := strings.Index(reply, "["); arrStart >= 0 {
		arrEnd := strings.LastIndex(reply, "]")
		if arrEnd > arrStart {
			var facts []string
			if err := json.Unmarshal([]byte(reply[arrStart:arrEnd+1]), &facts); err == nil {
				result.Facts = facts
				return result, nil
			}
		}
	}

	return result, fmt.Errorf("解析JSON失败：未找到有效JSON (原文：%s)", truncate(reply, 120))
}

// parseMsgTime 解析消息的 DateTime 字符串（格式 "2006-01-02 15:04"）。
func parseMsgTime(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// memSegment 是动态计算的消息分段。
type memSegment struct {
	Start    int  // 起始消息下标（含）
	End      int  // 结束消息下标（不含）
	GapSplit bool // true 表示本段因时间间隔超过 24h 而从上一段切分出来
}

// computeSegments 根据消息条数上限和时间间隔动态计算分段。
// 切分条件（任一满足即切分）：
//   - 段内消息数达到 memExtractChunkSize
//   - 相邻消息间隔超过 24h（强行切分，新段不继承未消解指代）
//   - 段总时间跨度超过 24h
//
// 时间解析失败时退化为纯条数切分。
func computeSegments(msgs []rawMsg) []memSegment {
	var segments []memSegment
	i := 0
	gapSplit := false // 第一段不是 gap split
	for i < len(msgs) {
		start := i
		end := i + 1
		startTime := parseMsgTime(msgs[start].DateTime)
		for end < len(msgs) && (end-start) < memExtractChunkSize {
			prevTime := parseMsgTime(msgs[end-1].DateTime)
			currTime := parseMsgTime(msgs[end].DateTime)
			if prevTime.IsZero() || currTime.IsZero() {
				end++
				continue
			}
			if currTime.Sub(prevTime) > memMaxTimeGap {
				break
			}
			if !startTime.IsZero() && currTime.Sub(startTime) > memMaxTimeGap {
				break
			}
			end++
		}
		segments = append(segments, memSegment{
			Start:    start,
			End:      end,
			GapSplit: gapSplit,
		})
		gapSplit = false
		if end < len(msgs) && end > 0 {
			prevEndTime := parseMsgTime(msgs[end-1].DateTime)
			nextStartTime := parseMsgTime(msgs[end].DateTime)
			if !prevEndTime.IsZero() && !nextStartTime.IsZero() {
				if nextStartTime.Sub(prevEndTime) > memMaxTimeGap {
					gapSplit = true
				}
			}
		}
		i = end
	}
	return segments
}

// extractAndStoreFacts 将消息分批送给 LLM 提炼事实，再对事实做 embedding 存库。
//
//   - startChunk：从哪个批次开始（0 = 全新，>0 = 续传）。调用方负责在续传时不清空 mem_facts。
//   - onProgress：每批完成后回调 (当前已完成批数, 总批数)，可为 nil。
//   - onChunkDone：每批完成后回调该批的索引（用于写检查点），可为 nil。
//   - abortCh：关闭此 channel 时提前结束（暂停），返回 ErrAborted。可传 nil 表示不支持中止。
//
// 返回本次新增的事实数；若所有批次均失败则同时返回最后一个错误。
var ErrAborted = fmt.Errorf("mem: 提炼已暂停")

func extractAndStoreFacts(
	key string, msgs []rawMsg, prefs Preferences, db *sql.DB, embConfigs []EmbeddingConfig,
	isGroup bool, displayName string,
	startChunk int,
	onProgress func(done, total int),
	onChunkDone func(chunkIdx int),
	abortCh <-chan struct{},
) (int, error) {
	// 动态计算分段：综合考虑条数上限（150 条）和时间间隔（24h）
	segments := computeSegments(msgs)
	totalChunks := len(segments)
	if totalChunks < 1 {
		totalChunks = 1
	}
	total := 0
	var lastErr error

	// 获取所有置顶记忆作为背景上下文，帮助 LLM 理解聊天中的人物关系
	var backgroundCtx string
	pinnedFacts, _ := GetPinnedMemFacts("")
	if len(pinnedFacts) > 0 {
		var bg strings.Builder
		for _, f := range pinnedFacts {
			bg.WriteString("- ")
			bg.WriteString(f.Fact)
			bg.WriteString("\n")
		}
		backgroundCtx = bg.String()
	}

	// 预计算置顶记忆的 embedding，用于过滤与置顶记忆高度相似的新事实
	const pinnedDedupThreshold = 0.85
	var pinnedEmbs [][]float32
	if len(pinnedFacts) > 0 {
		pinnedTexts := make([]string, len(pinnedFacts))
		for i, f := range pinnedFacts {
			pinnedTexts[i] = f.Fact
		}
		pinnedVecs, err := GetEmbeddingsBatchWithFallback(pinnedTexts, embConfigs)
		if err != nil {
			fmt.Printf("[MEM-EXTRACT] ⚠️ 置顶记忆 embedding 失败，中止提炼: %v\n", err)
			return 0, fmt.Errorf("embedding 服务不可用: %w", err)
		}
		pinnedEmbs = make([][]float32, 0, len(pinnedVecs))
		for _, v := range pinnedVecs {
			if v != nil {
				pinnedEmbs = append(pinnedEmbs, v)
			}
		}
	}

	// 跨 batch 去重：重叠窗口会在相邻 batch 产生近似重复的事实
	// 维护本轮已存储事实的 embedding 列表，每条新事实都与之比对
	const dedupThreshold = 0.88
	var storedEmbs [][]float32

	// 上下文摘要链：每批分析后生成结构化摘要，传递给下一批
	// 用于跨批次消解"他/她/那个事"等指代，避免张冠李戴
	var runningSummary memContextSummary

	// 断点续传时，先回退处理上一个 segment 以重建上下文摘要，
	// 这样续传的第一个 segment 也能拿到前文上下文。
	// 回退 segment 的事实不重复入库（之前已提取过）。
	if startChunk > 0 && startChunk <= totalChunks {
		lookbackIdx := startChunk - 1
		if lookbackIdx < len(segments) {
			lookbackSeg := segments[lookbackIdx]
			lookbackChunk := msgs[lookbackSeg.Start:lookbackSeg.End]
			if lookbackSeg.GapSplit {
				runningSummary.Unresolved = nil
				runningSummary.ActiveTopics = nil
			}
			lbResult, lbErr := extractFactsFromChunk(lookbackChunk, isGroup, displayName, memLLMPrefs(prefs), backgroundCtx, runningSummary)
			if lbErr == nil {
				runningSummary = lbResult.ContextSummary
			}
		}
	}

	for chunkIdx := startChunk; chunkIdx < totalChunks; chunkIdx++ {
		// 检查是否被暂停
		if abortCh != nil {
			select {
			case <-abortCh:
				return total, ErrAborted
			default:
			}
		}
		seg := segments[chunkIdx]
		chunk := msgs[seg.Start:seg.End]

		// 如果本段因时间间隔过大而从上一段切分，清除未消解指代和当前话题
		// 但保留人物表，因为群聊中的人物可能仍然相关
		if seg.GapSplit {
			runningSummary.Unresolved = nil
			runningSummary.ActiveTopics = nil
		}

		result, err := extractFactsFromChunk(chunk, isGroup, displayName, memLLMPrefs(prefs), backgroundCtx, runningSummary)
		if err != nil {
			lastErr = err
		} else {
			// 更新上下文摘要，供下一段使用
			runningSummary = result.ContextSummary
		}
		facts := result.Facts
		if len(facts) > 0 {
			// 取本批消息的时间范围作为 metadata，拼在 fact 前面（不进 embedding）
			chunkStart := chunk[0].DateTime
			chunkEnd := chunk[len(chunk)-1].DateTime
			timeRange := "[" + chunkStart + " ~ " + chunkEnd + "] "
			embeddings, err := GetEmbeddingsBatchWithFallback(facts, embConfigs)
			if err != nil {
				fmt.Printf("[MEM-EXTRACT] ⚠️ 事实 embedding 失败，中止提炼: %v\n", err)
				return total, fmt.Errorf("embedding 服务不可用: %w", err)
			} else {
				// 跨 batch 去重：和本轮已存的事实比对，sim > 0.88 视为重复
				var dedupFacts []string
				var dedupEmbs [][]float32
				for j, emb := range embeddings {
					if emb == nil || j >= len(facts) {
						continue
					}
					dup := false
					// 和本轮已存事实比对
					for _, prev := range storedEmbs {
						if cosineSimilarity(emb, prev) > dedupThreshold {
							dup = true
							break
						}
					}
					// 和置顶记忆比对：高度相似说明是已有背景知识，不需重复提取
					if !dup {
						for _, pEmb := range pinnedEmbs {
							if cosineSimilarity(emb, pEmb) > pinnedDedupThreshold {
								dup = true
								break
							}
						}
					}
					if !dup {
						dedupFacts = append(dedupFacts, facts[j])
						dedupEmbs = append(dedupEmbs, emb)
					}
				}
				// 把本轮保留的事实 embedding 加入全局列表
				storedEmbs = append(storedEmbs, dedupEmbs...)
				if len(dedupFacts) > 0 {
					tx, err := db.Begin()
					if err != nil {
						fmt.Printf("[MEM-EXTRACT] ⚠️ db.Begin 失败: %v\n", err)
					} else {
						stmt, err := tx.Prepare(
							"INSERT INTO mem_facts(contact_key, fact, source_from, source_to, embedding, version, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?)")
						if err != nil {
							tx.Rollback()
							return total, fmt.Errorf("准备插入语句失败: %w", err)
						}
						now := time.Now().Unix()
						for j, emb := range dedupEmbs {
							factWithMeta := timeRange + dedupFacts[j]
							if _, err := stmt.Exec(key, factWithMeta, seg.Start, seg.End-1, encodeVec(emb), memFactVersion, now, now); err != nil {
								stmt.Close()
								tx.Rollback()
								return total, fmt.Errorf("插入事实失败: %w", err)
							}
							total++
						}
						stmt.Close()
						if err := tx.Commit(); err != nil {
							return total, fmt.Errorf("提交事务失败: %w", err)
						}
					}
				}
			}
		}
		// 每批完成后先写检查点，再上报进度
		if onChunkDone != nil {
			onChunkDone(chunkIdx)
		}
		if onProgress != nil {
			onProgress(chunkIdx+1, totalChunks) // chunkIdx+1 = 含本批在内的已完成批数
		}
	}
	if total == 0 && lastErr != nil {
		return 0, lastErr
	}
	return total, nil
}

// memLLMPrefs 返回用于记忆提炼的 Preferences 副本。
// - 若用户配置了 MemLLMBaseURL 或 MemLLMModel，则使用专用配置。
//   - 填写了 MemLLMAPIKey → 使用云端模型（OpenAI 兼容，如 OpenRouter / DeepSeek 等）
//   - 未填写 MemLLMAPIKey → 使用本地 Ollama（隐私保护，数据不出本机）
//
// - 若两者均为空，则直接复用主 LLM 配置（与 AI 分析使用同一模型）。
func memLLMPrefs(prefs Preferences) Preferences {
	if prefs.MemLLMBaseURL == "" && prefs.MemLLMModel == "" {
		return prefs
	}
	p := prefs
	if prefs.MemLLMAPIKey != "" {
		// 云端模型：使用用户提供的 API Key，保持主 LLM 的 provider
		p.LLMAPIKey = prefs.MemLLMAPIKey
	} else {
		// 本地 Ollama：不需要 API Key
		p.LLMProvider = "ollama"
		p.LLMAPIKey = ""
	}
	if prefs.MemLLMBaseURL != "" {
		p.LLMBaseURL = prefs.MemLLMBaseURL
	} else if prefs.MemLLMAPIKey == "" {
		p.LLMBaseURL = "http://localhost:11434/v1"
	}
	if prefs.MemLLMModel != "" {
		p.LLMModel = prefs.MemLLMModel
	} else if prefs.MemLLMAPIKey == "" {
		p.LLMModel = "qwen2.5:7b"
	}
	return p
}

// extractFactsFromChunk 调用 LLM 从一批消息中提炼事实列表，并生成上下文摘要供下一段使用。
func extractFactsFromChunk(chunk []rawMsg, isGroup bool, displayName string, prefs Preferences, backgroundCtx string, priorSummary memContextSummary) (memExtractResult, error) {
	var sb strings.Builder
	for _, m := range chunk {
		sb.WriteString(m.DateTime)
		sb.WriteString(" ")
		sb.WriteString(m.Sender)
		sb.WriteString(": ")
		sb.WriteString(truncateRunes(m.Content, 80))
		sb.WriteString("\n")
	}

	bgSection := ""
	if backgroundCtx != "" {
		now := time.Now()
		monthStr := fmt.Sprintf("%d年%d月", now.Year(), int(now.Month()))
		bgSection = "\n已知背景信息（反映 " + monthStr + " 的当前状态，仅用于理解聊天中的人物关系，不代表历史状态）：\n" +
			"以下规则必须严格遵守：\n" +
			"  - 背景信息仅反映当前时间点的人物关系和状态，如果本段聊天记录发生在更早的时间，背景信息中的关系可能并不成立，绝对不能出现时间上错位的问题\n" +
			"  - 如果本段聊天记录中没有有用的信息，不要把背景信息的内容写进输出的 facts\n" +
			backgroundCtx + "\n"
	}

	priorSection := "\n前文上下文（来自上一段分析的摘要，用于消解\"他/她/那个\"等指代）：\n" + formatPriorSummary(priorSummary) + "\n"

	accuracyRule := "【最重要原则】宁愿少记也不要错记。记忆一旦出错会误导后续所有判断，因此：\n" +
		"  - 如果某条信息缺乏主语、上下文不完整或无法确定所指对象，跳过该条\n" +
		"  - 不要根据片段猜测、脑补或推断\n" +
		"  - 宁可遗漏一条可能有价值的信息，也不要记录一条可能错误的信息\n"

	outputFormat := "输出格式（JSON对象，不加任何解释）：\n" +
		"{\n" +
		"  \"facts\": [\"事实1\", \"事实2\"],\n" +
		"  \"context_summary\": {\n" +
		"    \"entities\": [{\"name\": \"本名\", \"aliases\": [\"外号\", \"简称\"]}],\n" +
		"    \"active_topics\": [\"本段结束时仍在讨论的话题\"],\n" +
		"    \"unresolved_references\": [\"本段结束时仍未消解的指代，供下一段参考\"]\n" +
		"  }\n" +
		"}\n" +
		"如果没有有价值的事实，facts 输出 []，但仍需输出 context_summary。\n"

	var prompt string
	if isGroup {
		prompt = "你是一个记忆提炼专家。从以下群聊记录中提取关键事实，并生成供下一段分析使用的上下文摘要。\n" +
			"\n" + accuracyRule +
			"\n规则：\n" +
			"1. 每条事实是一句完整的中文陈述，尽量补充细节（程度、频率、时间、对象、原因）\n" +
			"2. 只提取有价值的信息：喜好、经历、观点、习惯、工作、地点、人际关系等\n" +
			"3. 忽略寒暄、日常问候、无意义闲聊\n" +
			"4. 用消息中出现的发言者名字来描述事实\n" +
			"5. 如果聊天中出现了外号或简称，输出时需还原为此人的本名。例如聊天中出现'jyy称95和mmxs在一起'，应输出'蒋钰瑶称邱瀚轩和瞿茂林在一起'\n" +
			"6. 同一主题的零散信息合并成一条完整陈述\n" +
			"7. 不要重复提取已知背景信息中已经存在的事实\n" +
			"8. 不要在事实文本中添加具体日期或时间，只有当时间本身是关键信息（如'下个月要吃饺'、'五天前有考试'这种相对时间虚指）时才保留\n" +
			"9. 参考前文上下文摘要来理解\"他/她/那个事\"等指代；如果仍无法确定所指对象，跳过该条事实\n" +
			"\n" + outputFormat +
			bgSection +
			priorSection +
			"\n聊天记录：\n" + sb.String() + "\n输出："
	} else {
		prompt = fmt.Sprintf("你是一个记忆提炼专家。从以下聊天记录中提取关键事实，并生成供下一段分析使用的上下文摘要。\n"+
			"\n%s"+
			"\n规则：\n"+
			"1. 每条事实是一句完整的中文陈述，尽量补充细节（程度、频率、时间、对象、原因）\n"+
			"2. 只提取有价值的信息：喜好、经历、观点、习惯、工作、地点、人际关系等\n"+
			"3. 忽略寒暄、日常问候、无意义闲聊\n"+
			"4. 用【%s】指代聊天对象\n"+
			"5. 如果聊天中出现了外号或简称，输出时需还原为此人的本名。例如聊天中出现'jyy称95和mmxs在一起'，应输出'蒋钰瑶称邱瀚轩和瞿茂林在一起'\n"+
			"6. 同一主题的零散信息合并成一条完整陈述\n"+
			"7. 不要重复提取已知背景信息中已经存在的事实\n"+
			"8. 不要在事实文本中添加具体日期或时间，只有当时间本身是关键信息（如'下个月要吃饺'、'五天前有考试'这种相对时间虚指）时才保留\n"+
			"9. 参考前文上下文摘要来理解\"他/她/那个事\"等指代；如果仍无法确定所指对象，跳过该条事实\n"+
			"\n%s"+
			"%s"+
			"%s"+
			"\n聊天记录：\n%s\n输出：",
			accuracyRule,
			displayName,
			outputFormat,
			bgSection,
			priorSection,
			sb.String())
	}

	reply, err := completeMemLLMWithFallback([]LLMMessage{{Role: "user", Content: prompt}}, memLLMConfigs(prefs))
	if err != nil {
		return memExtractResult{}, err
	}

	result, err := parseExtractResult(reply)
	if err != nil {
		return memExtractResult{}, err
	}

	// 清理事实文本
	cleanedFacts := make([]string, 0, len(result.Facts))
	for _, f := range result.Facts {
		if f = strings.TrimSpace(f); f != "" {
			cleanedFacts = append(cleanedFacts, f)
		}
	}
	result.Facts = cleanedFacts

	// 限制摘要体积
	result.ContextSummary = capContextSummary(result.ContextSummary)

	return result, nil
}

// ─── 检索 ─────────────────────────────────────────────────────────────────────

// SearchMemFacts 对 mem_facts 执行语义检索，返回 top-K 最相关事实（带 ContactKey）。
// factTimeStart 从事实文本中提取时间范围的起点。
// 事实文本格式: "[2026-02-08 00:25 ~ 2026-02-08 10:30] 实际事实内容"
// 返回 "2026-02-08 00:25" 或空字符串（无法解析时）。
func factTimeStart(fact string) string {
	if !strings.HasPrefix(fact, "[") {
		return ""
	}
	end := strings.Index(fact, "]")
	if end < 0 {
		return ""
	}
	inner := fact[1:end]
	tilde := strings.Index(inner, "~")
	if tilde < 0 {
		return ""
	}
	return strings.TrimSpace(inner[:tilde])
}

// factTimeEnd 从事实文本中提取时间范围的终点。
// 事实文本格式: "[2026-02-08 00:25 ~ 2026-02-08 10:30] 实际事实内容"
// 返回 "2026-02-08 10:30" 或空字符串（无法解析时）。
func factTimeEnd(fact string) string {
	if !strings.HasPrefix(fact, "[") {
		return ""
	}
	end := strings.Index(fact, "]")
	if end < 0 {
		return ""
	}
	inner := fact[1:end]
	tilde := strings.Index(inner, "~")
	if tilde < 0 {
		return ""
	}
	return strings.TrimSpace(inner[tilde+1:])
}

func SearchMemFacts(key, query string, topK int, prefs Preferences) ([]MemFact, error) {
	return SearchMemFactsFiltered(key, query, topK, "", "", prefs)
}

// SearchMemFactsFiltered 在支持时间过滤的版本上搜索记忆事实。
// timeFrom/timeTo 格式为 "YYYY-MM-DD"（空=不限定）。
// 事实文本包含时间范围前缀 [start ~ end]，用 start 做时间过滤。
func SearchMemFactsFiltered(key, query string, topK int, timeFrom, timeTo string, prefs Preferences) ([]MemFact, error) {
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
	if key == "" {
		// key 为空时搜索所有联系人的记忆（如 AI 首页跨联系人问答）
		rows, err = db.Query(`SELECT contact_key, fact, embedding, source_from, source_to FROM mem_facts WHERE version = ?`, memFactVersion)
	} else {
		rows, err = db.Query(`SELECT contact_key, fact, embedding, source_from, source_to FROM mem_facts WHERE contact_key = ? AND version = ?`, key, memFactVersion)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scored struct {
		fact       string
		contactKey string
		sourceFrom int
		sourceTo   int
		sim        float32
	}
	var candidates []scored
	for rows.Next() {
		var s scored
		var blob []byte
		rows.Scan(&s.contactKey, &s.fact, &blob, &s.sourceFrom, &s.sourceTo)
		vec := decodeVec(blob)
		if len(vec) != len(queryVec) {
			continue
		}
		s.sim = cosineSimilarity(queryVec, vec)

		// 时间过滤：区间重叠判断，只有 fact 整个时间范围都在查询范围之外时才跳过
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

	// 按相似度降序
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].sim > candidates[j].sim
	})
	// 过滤掉相似度过低的结果（噪声）
	const minSim = 0.3
	filtered := candidates[:0]
	for _, c := range candidates {
		if c.sim >= minSim {
			filtered = append(filtered, c)
		}
	}
	candidates = filtered
	if len(candidates) > topK {
		candidates = candidates[:topK]
	}

	out := make([]MemFact, len(candidates))
	for i, s := range candidates {
		out[i] = MemFact{
			Fact:       s.fact,
			ContactKey: s.contactKey,
			SourceFrom: s.sourceFrom,
			SourceTo:   s.sourceTo,
		}
	}
	return out, nil
}

// conceptLikeTerms 把一个概念拆成可用于 LIKE 共现匹配的词项。
// LLM 分解出的概念可能是完整短语（如“装逼故事”），而事实文本往往只含
// 其中一部分（“装逼”“炫耀”）。这里保留整词并补上 2 字子串，扩大召回，
// 实体约束会把范围限制在“谁的故事”内，避免过度泛化。
func conceptLikeTerms(concept string) []string {
	r := []rune(concept)
	if len(r) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	add := func(t string) {
		t = strings.TrimSpace(t)
		if len([]rune(t)) >= 2 && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	// 整词
	add(concept)
	// 连续 2 字子串，覆盖“装逼故事”→“装逼”“逼故”“故事”
	for i := 0; i+2 <= len(r); i++ {
		add(string(r[i : i+2]))
	}
	return out
}

// SearchMemFactsCoMention 按“实体 × 概念”共现召回记忆事实。
//
// 与纯向量/BM25 检索互补：当问题里同时出现实体（人名）和语义概念时，
// 用 LIKE 显式要求 fact 同时命中两者，避免相关度计算被无关的字面命中
// （例如“装逼”）稀释掉真正需要“邓凯文 + 装逼/炫耀”共同出现的事实。
func SearchMemFactsCoMention(key string, entities, concepts []string, topK int, timeFrom, timeTo string) []MemFact {
	for i := range entities {
		entities[i] = strings.TrimSpace(entities[i])
	}
	for i := range concepts {
		concepts[i] = strings.TrimSpace(concepts[i])
	}
	var entityTerms []string
	for _, e := range entities {
		if e != "" {
			entityTerms = append(entityTerms, e)
		}
	}
	var conceptTerms []string
	seenConcept := make(map[string]bool)
	for _, c := range concepts {
		for _, t := range conceptLikeTerms(c) {
			if !seenConcept[t] {
				seenConcept[t] = true
				conceptTerms = append(conceptTerms, t)
			}
		}
	}
	if len(entityTerms) == 0 || len(conceptTerms) == 0 {
		return nil
	}

	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return nil
	}

	// 构造 WHERE：(entity1 OR entity2) AND (concept1 OR concept2)
	var conds []string
	var args []interface{}
	for _, e := range entityTerms {
		conds = append(conds, "LOWER(fact) LIKE LOWER(?)")
		args = append(args, "%"+e+"%")
	}
	entityClause := "(" + strings.Join(conds, " OR ") + ")"

	var conceptConds []string
	for _, c := range conceptTerms {
		conceptConds = append(conceptConds, "LOWER(fact) LIKE LOWER(?)")
		args = append(args, "%"+c+"%")
	}
	conceptClause := "(" + strings.Join(conceptConds, " OR ") + ")"

	base := "SELECT contact_key, fact, source_from, source_to FROM mem_facts WHERE version = ? AND " + entityClause + " AND " + conceptClause
	baseArgs := append([]interface{}{memFactVersion}, args...)

	rows, err := func() (*sql.Rows, error) {
		if key != "" {
			return db.Query(base+" AND contact_key = ? ORDER BY id DESC LIMIT ?", append(baseArgs, key, topK*3)...)
		}
		return db.Query(base+" ORDER BY id DESC LIMIT ?", append(baseArgs, topK*3)...)
	}()
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []MemFact
	seen := make(map[string]bool)
	for rows.Next() {
		var m MemFact
		if err := rows.Scan(&m.ContactKey, &m.Fact, &m.SourceFrom, &m.SourceTo); err != nil {
			continue
		}
		if timeFrom != "" || timeTo != "" {
			factStart := factTimeStart(m.Fact)
			if factStart != "" {
				factEnd := factTimeEnd(m.Fact)
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
		if seen[m.Fact] {
			continue
		}
		seen[m.Fact] = true
		out = append(out, m)
		if len(out) >= topK {
			break
		}
	}
	return out
}

// memLLMConfigs 从 Preferences 构造 []Preferences（多提供商 fallback）。
// 优先使用 MemLLMProfiles；为空时回退到单字段配置。
func memLLMConfigs(prefs Preferences) []Preferences {
	if len(prefs.MemLLMProfiles) > 0 {
		configs := make([]Preferences, 0, len(prefs.MemLLMProfiles))
		for _, p := range prefs.MemLLMProfiles {
			cfg := prefs
			cfg.LLMProvider = p.Provider
			cfg.LLMAPIKey = p.APIKey
			cfg.LLMBaseURL = p.BaseURL
			cfg.LLMModel = p.Model
			configs = append(configs, cfg)
		}
		return configs
	}
	return []Preferences{memLLMPrefs(prefs)}
}

// completeMemLLMWithFallback 按多提供商顺序尝试记忆提炼 LLM 调用，带粘性回退。
// 只有所有提供商都失败才返回错误。
func completeMemLLMWithFallback(msgs []LLMMessage, prefsList []Preferences) (string, error) {
	if len(prefsList) == 0 {
		return "", fmt.Errorf("未配置记忆提炼模型")
	}
	numProviders := len(prefsList)
	activeIdx := memLLMFallback.getActiveIndex(numProviders)

	var lastErr error
	for i := 0; i < numProviders; i++ {
		idx := (activeIdx + i) % numProviders
		result, err := CompleteLLM(msgs, prefsList[idx])
		if err == nil {
			memLLMFallback.recordSuccess()
			return result, nil
		}
		lastErr = err
		memLLMFallback.recordFailure(idx, numProviders)
	}
	return "", fmt.Errorf("所有记忆提炼 LLM 提供商均失败，最后错误: %w", lastErr)
}
