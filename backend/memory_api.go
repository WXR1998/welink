package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// registerMemoryRoutes 挂载 Memory UI 相关端点。
// 目标：让用户可见、可编辑、可置顶 LLM 提炼出来的事实（mem_facts）。
func registerMemoryRoutes(api *gin.RouterGroup) {
	// 全局列表 + 筛选
	api.GET("/memory/list", func(c *gin.Context) {
		contact := c.Query("contact")        // 为空则全量
		q := strings.TrimSpace(c.Query("q")) // 关键词（fact LIKE）
		pinnedFilter := c.Query("pinned")    // "1"=只看置顶, "exclude"=不看置顶, 空=全看
		limit, _ := strconv.Atoi(c.Query("limit"))
		if limit <= 0 || limit > 500 {
			limit = 100
		}
		offset, _ := strconv.Atoi(c.Query("offset"))
		if offset < 0 {
			offset = 0
		}

		db := getAIDB()
		if db == nil {
			c.JSON(http.StatusOK, gin.H{"facts": []MemFact{}, "total": 0})
			return
		}

		whereParts := []string{"version = ?"}
		args := []interface{}{memFactVersion}
		if contact != "" {
			whereParts = append(whereParts, "contact_key = ?")
			args = append(args, contact)
		}
		if pinnedFilter == "1" {
			whereParts = append(whereParts, "pinned = 1")
		} else if pinnedFilter == "exclude" {
			whereParts = append(whereParts, "pinned = 0")
		}
		if q != "" {
			// LIKE 转义：用户搜 "100%" / "_abc" 时不应被当作通配符匹配，
			// 否则 % 会展成任意串、_ 会展成单字符，返回一堆无关结果。
			whereParts = append(whereParts, `fact LIKE ? ESCAPE '\'`)
			args = append(args, "%"+escapeLikePattern(q)+"%")
		}
		where := " WHERE " + strings.Join(whereParts, " AND ")

		// 先统计总数
		var total int
		_ = db.QueryRow("SELECT COUNT(*) FROM mem_facts"+where, args...).Scan(&total)

		// 排序：sort 决定字段，order 决定方向；置顶始终优先
		sortKey := c.DefaultQuery("sort", "id")  // id | contact_key | created_at | source_from
		order := c.DefaultQuery("order", "desc") // asc | desc
		orderDir := "DESC"
		if order == "asc" {
			orderDir = "ASC"
		}

		var rows *sql.Rows
		var err error
		if sortKey == "source_from" {
			// source_from 是 vec_messages 的 seq 偏移量（每个联系人从 0 开始），
			// 直接按它排序在不同联系人之间没有时间意义。
			// 先查全部 mem_facts（不加 LIMIT/OFFSET），再批量查 datetime，在 Go 层排序后分页。
			query := "SELECT id, contact_key, fact, source_from, source_to, pinned, created_at, updated_at FROM mem_facts" +
				where + " ORDER BY pinned DESC, id DESC"
			args = append(args[:0], args...)
			rows, err = db.Query(query, args...)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			var allFacts []MemFact
			for rows.Next() {
				var f MemFact
				var pinned int
				rows.Scan(&f.ID, &f.ContactKey, &f.Fact, &f.SourceFrom, &f.SourceTo, &pinned, &f.CreatedAt, &f.UpdatedAt)
				f.Pinned = pinned != 0
				allFacts = append(allFacts, f)
			}
			rows.Close()

			// 批量查 datetime：收集所有 (contact_key, source_from) 对
			type keySeq struct {
				key string
				seq int
			}
			keySeqs := make(map[keySeq]string)
			for _, f := range allFacts {
				keySeqs[keySeq{key: f.ContactKey, seq: f.SourceFrom}] = ""
			}
			// 一次性查所有需要的 datetime
			for ks := range keySeqs {
				var dt string
				err := db.QueryRow("SELECT datetime FROM vec_messages WHERE contact_key = ? AND seq = ? LIMIT 1", ks.key, ks.seq).Scan(&dt)
				if err == nil {
					keySeqs[ks] = dt
				}
			}
			// 给每条 fact 附上 chat_time 并排序
			type factWithTime struct {
				fact     MemFact
				chatTime string
			}
			var fwt []factWithTime
			for _, f := range allFacts {
				fwt = append(fwt, factWithTime{fact: f, chatTime: keySeqs[keySeq{key: f.ContactKey, seq: f.SourceFrom}]})
			}
			if orderDir == "ASC" {
				sort.Slice(fwt, func(i, j int) bool {
					if fwt[i].fact.Pinned != fwt[j].fact.Pinned {
						return fwt[i].fact.Pinned
					}
					if fwt[i].chatTime != fwt[j].chatTime {
						return fwt[i].chatTime < fwt[j].chatTime
					}
					return fwt[i].fact.ID < fwt[j].fact.ID
				})
			} else {
				sort.Slice(fwt, func(i, j int) bool {
					if fwt[i].fact.Pinned != fwt[j].fact.Pinned {
						return fwt[i].fact.Pinned
					}
					if fwt[i].chatTime != fwt[j].chatTime {
						return fwt[i].chatTime > fwt[j].chatTime
					}
					return fwt[i].fact.ID > fwt[j].fact.ID
				})
			}
			// 分页
			start := offset
			if start > len(fwt) {
				start = len(fwt)
			}
			end := start + limit
			if end > len(fwt) {
				end = len(fwt)
			}
			facts := make([]MemFact, 0, end-start)
			for _, f := range fwt[start:end] {
				facts = append(facts, f.fact)
			}
			c.JSON(http.StatusOK, gin.H{"facts": facts, "total": total})
		} else {
			sortCol := "id"
			switch sortKey {
			case "contact_key":
				sortCol = "contact_key"
			case "created_at":
				sortCol = "created_at"
			}
			query := "SELECT id, contact_key, fact, source_from, source_to, pinned, created_at, updated_at FROM mem_facts" +
				where + " ORDER BY pinned DESC, " + sortCol + " " + orderDir + ", id DESC LIMIT ? OFFSET ?"
			args = append(args, limit, offset)
			rows, err = db.Query(query, args...)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			defer rows.Close()
			facts := []MemFact{}
			for rows.Next() {
				var f MemFact
				var pinned int
				rows.Scan(&f.ID, &f.ContactKey, &f.Fact, &f.SourceFrom, &f.SourceTo, &pinned, &f.CreatedAt, &f.UpdatedAt)
				f.Pinned = pinned != 0
				facts = append(facts, f)
			}
			c.JSON(http.StatusOK, gin.H{"facts": facts, "total": total})
		}
	})

	// 每个 contact 的事实数量统计（填充左侧筛选面板）
	api.GET("/memory/contacts", func(c *gin.Context) {
		db := getAIDB()
		if db == nil {
			c.JSON(http.StatusOK, gin.H{"contacts": []gin.H{}})
			return
		}
		rows, err := db.Query(`
			SELECT contact_key, COUNT(*) AS n, SUM(CASE WHEN pinned = 1 THEN 1 ELSE 0 END) AS pinned
			FROM mem_facts WHERE version = ?
			GROUP BY contact_key
			ORDER BY n DESC`, memFactVersion)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer rows.Close()
		items := []gin.H{}
		for rows.Next() {
			var key string
			var n, pinned int
			rows.Scan(&key, &n, &pinned)
			items = append(items, gin.H{"contact_key": key, "count": n, "pinned_count": pinned})
		}
		c.JSON(http.StatusOK, gin.H{"contacts": items})
	})

	// 手动添加记忆
	api.POST("/memory", func(c *gin.Context) {
		var body struct {
			ContactKey string `json:"contact_key"`
			Fact       string `json:"fact"`
			Pinned     bool   `json:"pinned"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
			return
		}
		body.ContactKey = strings.TrimSpace(body.ContactKey)
		body.Fact = strings.TrimSpace(body.Fact)
		if body.ContactKey == "" || body.Fact == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "contact_key / fact 不能为空"})
			return
		}
		// 保证前缀存在：没带就当成 contact 联系人
		if !strings.Contains(body.ContactKey, ":") {
			body.ContactKey = "contact:" + body.ContactKey
		}

		db := getAIDB()
		if db == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI DB 未就绪"})
			return
		}

		// 尝试计算 embedding（配好了 embedding 才会有值）；拿不到就零向量
		var emb []byte
		prefs := loadPreferences()
		embCfg, embErr := currentEmbeddingConfig(prefs)
		if embErr == nil {
			if vecs, err := GetEmbeddingsBatch([]string{body.Fact}, embCfg); err == nil && len(vecs) == 1 && vecs[0] != nil {
				emb = encodeVec(vecs[0])
			}
		}
		if emb == nil {
			// 零向量占位（schema 要求 NOT NULL）；之后如果用户跑一次「重提炼」可以补
			emb = encodeVec(make([]float32, 1))
		}
		pinned := 0
		if body.Pinned {
			pinned = 1
		}
		now := time.Now().Unix()
		res, err := db.Exec(
			"INSERT INTO mem_facts(contact_key, fact, source_from, source_to, embedding, pinned, version, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)",
			body.ContactKey, body.Fact, 0, 0, emb, pinned, memFactVersion, now, now,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		id, _ := res.LastInsertId()
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
			"id":     id,
			"fact": MemFact{
				ID: int(id), ContactKey: body.ContactKey, Fact: body.Fact,
				Pinned: body.Pinned, CreatedAt: now, UpdatedAt: now,
			},
		})
	})

	// 编辑 fact 内容
	api.PUT("/memory/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id 非法"})
			return
		}
		var body struct {
			Fact string `json:"fact"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Fact) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "fact 不能为空"})
			return
		}
		db := getAIDB()
		if db == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI DB 未就绪"})
			return
		}
		res, err := db.Exec("UPDATE mem_facts SET fact = ?, updated_at = ? WHERE id = ? AND version = ?",
			strings.TrimSpace(body.Fact), time.Now().Unix(), id, memFactVersion)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "记忆不存在"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 删除
	api.DELETE("/memory/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id 非法"})
			return
		}
		db := getAIDB()
		if db == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI DB 未就绪"})
			return
		}
		if _, err := db.Exec("DELETE FROM mem_facts WHERE id = ? AND version = ?", id, memFactVersion); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 清除所有非置顶记忆事实 + 重置记忆提取游标
	api.DELETE("/memory/non-pinned", func(c *gin.Context) {
		db := getAIDB()
		if db == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI DB 未就绪"})
			return
		}
		tx, err := db.Begin()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// 删除所有未置顶的事实
		res, err := tx.Exec("DELETE FROM mem_facts WHERE pinned = 0 AND version = ?", memFactVersion)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		deleted, _ := res.RowsAffected()
		// 重置记忆提取游标（extract_offset = -1）
		tx.Exec("UPDATE vec_index_status SET extract_offset = -1, extract_version = ?", memFactVersion)
		if err := tx.Commit(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// 清空所有 job，让运行中/暂停的任务从列表中消失
		vecJobsMu.Lock()
		for k := range vecJobs {
			delete(vecJobs, k)
		}
		vecJobsMu.Unlock()
		c.JSON(http.StatusOK, gin.H{"status": "ok", "deleted": deleted})
	})

	// 清除所有 embedding + 向量索引游标
	api.DELETE("/memory/embeddings", func(c *gin.Context) {
		db := getAIDB()
		if db == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI DB 未就绪"})
			return
		}
		tx, err := db.Begin()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// 清空向量消息表
		tx.Exec("DELETE FROM vec_messages")
		// 清空向量索引状态表
		tx.Exec("DELETE FROM vec_index_status")
		// 重置记忆提取游标
		tx.Exec("UPDATE vec_index_status SET extract_offset = -1, extract_version = ?", memFactVersion)
		if err := tx.Commit(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// 清空所有 job，让运行中/暂停的任务从列表中消失
		vecJobsMu.Lock()
		for k := range vecJobs {
			delete(vecJobs, k)
		}
		vecJobsMu.Unlock()
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 置顶 / 取消置顶
	api.PUT("/memory/:id/pin", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id 非法"})
			return
		}
		var body struct {
			Pinned bool `json:"pinned"`
		}
		_ = c.ShouldBindJSON(&body)
		db := getAIDB()
		if db == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI DB 未就绪"})
			return
		}
		val := 0
		if body.Pinned {
			val = 1
		}
		if _, err := db.Exec("UPDATE mem_facts SET pinned = ?, updated_at = ? WHERE id = ? AND version = ?",
			val, time.Now().Unix(), id, memFactVersion); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "pinned": body.Pinned})
	})

	// 查看某条记忆的来源聊天记录（hover 预览用）
	api.GET("/memory/:id/source", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id 非法"})
			return
		}
		db := getAIDB()
		if db == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI DB 未就绪"})
			return
		}
		var contactKey string
		var sourceFrom, sourceTo int
		err = db.QueryRow("SELECT contact_key, source_from, source_to FROM mem_facts WHERE id = ? AND version = ?", id, memFactVersion).
			Scan(&contactKey, &sourceFrom, &sourceTo)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "记忆不存在"})
			return
		}

		// 查找同一段聊天记录（相同 source_from / source_to）的所有事实，
		// 用于截图标题展示完整记忆集（不受搜索结果 limit 限制）。
		relatedRows, relErr := db.Query(
			"SELECT fact FROM mem_facts WHERE contact_key = ? AND source_from = ? AND source_to = ? AND version = ? ORDER BY id",
			contactKey, sourceFrom, sourceTo, memFactVersion)
		var relatedFacts []string
		if relErr == nil {
			for relatedRows.Next() {
				var f string
				relatedRows.Scan(&f)
				relatedFacts = append(relatedFacts, f)
			}
			relatedRows.Close()
		}
		if relatedFacts == nil {
			relatedFacts = []string{}
		}
		// source_from / source_to 是 vec_messages 按 seq 排序后的索引
		if sourceFrom < 0 || sourceTo < sourceFrom {
			c.JSON(http.StatusOK, gin.H{"messages": []interface{}{}})
			return
		}
		limit := sourceTo - sourceFrom + 1
		rows, err := db.Query(
			`SELECT datetime, sender, content FROM vec_messages WHERE contact_key = ? ORDER BY seq LIMIT ? OFFSET ?`,
			contactKey, limit, sourceFrom)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer rows.Close()
		type SrcMsg struct {
			DateTime string `json:"datetime"`
			Sender   string `json:"sender"`
			Content  string `json:"content"`
		}
		var msgs []SrcMsg
		for rows.Next() {
			var m SrcMsg
			rows.Scan(&m.DateTime, &m.Sender, &m.Content)
			msgs = append(msgs, m)
		}
		if msgs == nil {
			msgs = []SrcMsg{}
		}
		c.JSON(http.StatusOK, gin.H{"messages": msgs, "range": gin.H{"from": sourceFrom, "to": sourceTo}, "related_facts": relatedFacts})
	})
}

// getAIDB 并发安全地取 aiDB 快照；nil 表示未就绪。
func getAIDB() *sql.DB {
	aiDBMu.Lock()
	defer aiDBMu.Unlock()
	return aiDB
}

// GetPinnedMemFacts 返回所有 pinned 的事实（可选按 contact_key 过滤）。
// 用于 AI 对话时自动 prepend 到 system prompt。
func GetPinnedMemFacts(contactKey string) ([]MemFact, error) {
	db := getAIDB()
	if db == nil {
		return nil, nil
	}
	var rows *sql.Rows
	var err error
	if contactKey != "" {
		rows, err = db.Query(
			"SELECT id, contact_key, fact, source_from, source_to, created_at, updated_at FROM mem_facts WHERE pinned = 1 AND contact_key = ? AND version = ? ORDER BY updated_at DESC",
			contactKey, memFactVersion)
	} else {
		rows, err = db.Query(
			"SELECT id, contact_key, fact, source_from, source_to, created_at, updated_at FROM mem_facts WHERE pinned = 1 AND version = ? ORDER BY updated_at DESC", memFactVersion)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemFact
	for rows.Next() {
		var f MemFact
		rows.Scan(&f.ID, &f.ContactKey, &f.Fact, &f.SourceFrom, &f.SourceTo, &f.CreatedAt, &f.UpdatedAt)
		f.Pinned = true
		out = append(out, f)
	}
	return out, nil
}

// BuildPinnedMemoryBlock 把当前置顶事实拼成一段 system prompt 片段。
// 为空字符串表示没有置顶事实可插入。
func BuildPinnedMemoryBlock(contactKey string) string {
	facts, err := GetPinnedMemFacts(contactKey)
	if err != nil || len(facts) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n── 用户置顶的背景事实（始终记住这些）──\n")
	for _, f := range facts {
		fmt.Fprintf(&sb, "%s\n", pinnedFactLineForBuild(f))
	}
	return sb.String()
}

// pinnedFactLineForBuild 在没有 ContactService 可用的场景下，
// 用 MemFact 已解析的 DisplayName/SourceName 补主语，避免缺少主语。
func pinnedFactLineForBuild(f MemFact) string {
	name := strings.TrimSpace(f.DisplayName)
	if name == "" {
		name = strings.TrimSpace(f.SourceName)
	}
	if name == "" {
		return "- " + strings.TrimSpace(f.Fact)
	}
	return "- " + name + "：" + strings.TrimSpace(f.Fact)
}

// escapeLikePattern 给用户输入的 LIKE 关键词转义 SQLite 通配符（% _ \）。
// 调用方拼接 "%" + escape(q) + "%" 并配合 `ESCAPE '\'` 使用，
// 避免用户搜 "100%" / "a_b" 时被当作通配符匹配。
func escapeLikePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
