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
	err := db.QueryRow("SELECT COUNT(*) FROM mem_facts WHERE contact_key = ?", key).Scan(&count)
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
		"SELECT id, fact, source_from, source_to, pinned, created_at, updated_at FROM mem_facts WHERE contact_key = ? ORDER BY pinned DESC, id",
		key,
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

const memExtractChunkSize = 80 // 每批送给 LLM 的消息条数
const memExtractStride = 64   // 步进（重叠 16 条，衔接上下文）

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
	key string, msgs []rawMsg, prefs Preferences, db *sql.DB, embCfg EmbeddingConfig,
	isGroup bool, displayName string,
	startChunk int,
	onProgress func(done, total int),
	onChunkDone func(chunkIdx int),
	abortCh <-chan struct{},
) (int, error) {
	// 使用 stride 步进：每批 80 条，步进 64 条，重叠 16 条
	totalChunks := 1
	if len(msgs) > memExtractChunkSize {
		totalChunks = (len(msgs)-memExtractChunkSize+memExtractStride) / memExtractStride + 1
	}
	total := 0
	var lastErr error

	// 获取所有置顶记忆作为背景上下文，帮助 LLM 理解聊天中的人物关系
	var backgroundCtx string
	if pinned, _ := GetPinnedMemFacts(""); len(pinned) > 0 {
		var bg strings.Builder
		for _, f := range pinned {
			bg.WriteString("- ")
			bg.WriteString(f.Fact)
			bg.WriteString("\n")
		}
		backgroundCtx = bg.String()
	}

	// 跨 batch 去重：重叠窗口会在相邻 batch 产生近似重复的事实
	// 维护本轮已存储事实的 embedding 列表，每条新事实都与之比对
	const dedupThreshold = 0.88
	var storedEmbs [][]float32

	for chunkIdx := startChunk; chunkIdx < totalChunks; chunkIdx++ {
		// 检查是否被暂停
		if abortCh != nil {
			select {
			case <-abortCh:
				return total, ErrAborted
			default:
			}
		}
		i := chunkIdx * memExtractStride
		end := i + memExtractChunkSize
		if end > len(msgs) {
			end = len(msgs)
		}
		chunk := msgs[i:end]

		facts, err := extractFactsFromChunk(chunk, isGroup, displayName, memLLMPrefs(prefs), backgroundCtx)
		if err != nil {
			lastErr = err
		} else if len(facts) > 0 {
			// 取本批消息的时间范围作为 metadata，拼在 fact 前面（不进 embedding）
			chunkStart := chunk[0].DateTime
			chunkEnd := chunk[len(chunk)-1].DateTime
			timeRange := "[" + chunkStart + " ~ " + chunkEnd + "] "
			embeddings, err := GetEmbeddingsBatch(facts, embCfg)
			if err != nil {
				lastErr = err
			} else {
				// 跨 batch 去重：和本轮已存的事实比对，sim > 0.88 视为重复
				var dedupFacts []string
				var dedupEmbs [][]float32
				for j, emb := range embeddings {
					if emb == nil || j >= len(facts) {
						continue
					}
					dup := false
					for _, prev := range storedEmbs {
						if cosineSimilarity(emb, prev) > dedupThreshold {
							dup = true
							break
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
					if err == nil {
						stmt, err := tx.Prepare(
							"INSERT INTO mem_facts(contact_key, fact, source_from, source_to, embedding, created_at, updated_at) VALUES(?,?,?,?,?,?,?)")
						if err != nil {
							tx.Rollback()
						} else {
							now := time.Now().Unix()
							for j, emb := range dedupEmbs {
								factWithMeta := timeRange + dedupFacts[j]
								if _, err := stmt.Exec(key, factWithMeta, i, end-1, encodeVec(emb), now, now); err == nil {
									total++
								}
							}
							stmt.Close()
							tx.Commit()
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

// extractFactsFromChunk 调用 LLM 从一批消息中提炼事实列表。
func extractFactsFromChunk(chunk []rawMsg, isGroup bool, displayName string, prefs Preferences, backgroundCtx string) ([]string, error) {
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
		bgSection = "\n已知背景信息（用于理解聊天中的人物）：\n" + backgroundCtx + "\n"
	}

	var prompt string
	if isGroup {
		prompt = "从以下群聊记录中提取关键事实，以JSON数组格式输出。\n" +
			"规则：\n" +
			"1. 每条事实是一句完整的中文陈述，尽量补充细节（程度、频率、时间、对象、原因）\n" +
			"2. 只提取有价值的信息：喜好、经历、观点、习惯、工作、地点、人际关系等\n" +
			"3. 忽略寒暄、日常问候、无意义闲聊\n" +
			"4. 用消息中出现的发言者名字来描述事实\n" +
			"5. 如果聊天中出现了外号或简称，输出时需还原为此人的本名。例如聊天中出现'jyy称95和mmxs在一起'，应输出'蒋钰瑶称邱瀚轩和瞿茂林在一起'\n" +
			"6. 同一主题的零散信息合并成一条完整陈述\n" +
			"7. 宁愿少记也不要错记：如果某条信息缺乏主语、上下文不完整或无法确定所指对象，跳过该条事实\n" +
			"8. 只输出JSON数组，不加任何解释，例如：[\"蒋钰瑶喜欢户外运动，经常周末和朋友去爬香山\", \"钟视航在北京做程序员，主要写后端\"]\n" +
			"9. 如果没有有价值的事实，输出：[]\n" +
			bgSection +
			"\n聊天记录：\n" + sb.String() + "\n输出："
	} else {
		prompt = fmt.Sprintf("从以下聊天记录中提取关键事实，以JSON数组格式输出。\n"+
			"规则：\n"+
			"1. 每条事实是一句完整的中文陈述，尽量补充细节（程度、频率、时间、对象、原因）\n"+
			"2. 只提取有价值的信息：喜好、经历、观点、习惯、工作、地点、人际关系等\n"+
			"3. 忽略寒暄、日常问候、无意义闲聊\n"+
			"4. 用【%s】指代聊天对象\n"+
			"5. 如果聊天中出现了外号或简称，输出时需还原为此人的本名。例如聊天中出现'jyy称95和mmxs在一起'，应输出'蒋钰瑶称邱瀚轩和瞿茂林在一起'\n"+
			"6. 同一主题的零散信息合并成一条完整陈述\n"+
			"7. 宁愿少记也不要错记：如果某条信息缺乏主语、上下文不完整或无法确定所指对象，跳过该条事实\n"+
			"8. 只输出JSON数组，不加任何解释，例如：[\"%s喜欢户外运动，经常周末和朋友去爬香山\", \"%s在北京做程序员，主要写后端\"]\n"+
			"9. 如果没有有价值的事实，输出：[]\n"+
			"%s"+
			"\n聊天记录：\n%s\n输出：",
			displayName, displayName, displayName, bgSection, sb.String())
	}

	reply, err := CompleteLLM([]LLMMessage{{Role: "user", Content: prompt}}, prefs)
	if err != nil {
		return nil, err
	}

	reply = strings.TrimSpace(reply)
	// 有些模型会在 JSON 前后加文字，尝试提取 [...] 部分
	if start := strings.Index(reply, "["); start >= 0 {
		if end := strings.LastIndex(reply, "]"); end > start {
			reply = reply[start : end+1]
		}
	}

	var facts []string
	if err := json.Unmarshal([]byte(reply), &facts); err != nil {
		return nil, fmt.Errorf("解析JSON失败：%w (原文：%s)", err, truncate(reply, 120))
	}

	out := facts[:0]
	for _, f := range facts {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out, nil
}

// ─── 检索 ─────────────────────────────────────────────────────────────────────

// SearchMemFacts 对 mem_facts 执行语义检索，返回 top-K 最相关事实（带 ContactKey）。
func SearchMemFacts(key, query string, topK int, prefs Preferences) ([]MemFact, error) {
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
		rows, err = db.Query(`SELECT contact_key, fact, embedding FROM mem_facts`)
	} else {
		rows, err = db.Query(`SELECT contact_key, fact, embedding FROM mem_facts WHERE contact_key = ?`, key)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scored struct {
		fact       string
		contactKey string
		sim        float32
	}
	var candidates []scored
	for rows.Next() {
		var contactKey string
		var fact string
		var blob []byte
		rows.Scan(&contactKey, &fact, &blob)
		vec := decodeVec(blob)
		if len(vec) != len(queryVec) {
			continue
		}
		candidates = append(candidates, scored{fact, contactKey, cosineSimilarity(queryVec, vec)})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].sim > candidates[j].sim
	})
	if len(candidates) > topK {
		candidates = candidates[:topK]
	}

	out := make([]MemFact, len(candidates))
	for i, s := range candidates {
		out[i] = MemFact{
			Fact:       s.fact,
			ContactKey: s.contactKey,
		}
	}
	return out, nil
}
