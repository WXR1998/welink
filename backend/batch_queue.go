package main

// batch_queue.go — 持久化批量记忆提炼任务队列
//
// 设计：
//   - batch_tasks 表持久化任务到 ai_analysis.db，Docker 重启后自动恢复
//   - 单 worker goroutine 串行处理任务，避免并发 embedding 压垮 API
//   - 每个任务自动补齐前置条件：FTS → 向量索引 → 记忆提炼

import (
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"welink/backend/service"
)

// BatchTaskStatus 任务状态
const (
	BatchTaskPending = "pending"
	BatchTaskRunning = "running"
	BatchTaskDone    = "done"
	BatchTaskError   = "error"
)

// BatchTask 持久化的批量任务
type BatchTask struct {
	ID            int64  `json:"id"`
	ContactKey    string `json:"contact_key"`
	Username      string `json:"username"`
	IsGroup       bool   `json:"is_group"`
	Status        string `json:"status"`
	CurrentStep   string `json:"current_step,omitempty"`
	ProgressDone  int    `json:"progress_done,omitempty"`
	ProgressTotal int    `json:"progress_total,omitempty"`
	Error         string `json:"error,omitempty"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

var (
	batchQueueMu       sync.Mutex
	batchWorkerRunning  bool
	batchSvc            func() *service.ContactService
	batchAbortCh        chan struct{}
)

// initBatchTaskTable 创建 batch_tasks 表（在 aiDBMu 持有期间调用）
func initBatchTaskTable() error {
	_, err := aiDB.Exec(`CREATE TABLE IF NOT EXISTS batch_tasks (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		contact_key    TEXT    NOT NULL,
		username       TEXT    NOT NULL,
		is_group       INTEGER NOT NULL DEFAULT 0,
		status         TEXT    NOT NULL DEFAULT 'pending',
		current_step   TEXT    NOT NULL DEFAULT '',
		progress_done  INTEGER NOT NULL DEFAULT 0,
		progress_total INTEGER NOT NULL DEFAULT 0,
		error          TEXT    NOT NULL DEFAULT '',
		created_at     INTEGER NOT NULL DEFAULT 0,
		updated_at     INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		return fmt.Errorf("batch_tasks: %w", err)
	}
	_, err = aiDB.Exec(`CREATE INDEX IF NOT EXISTS idx_batch_status ON batch_tasks(status)`)
	if err != nil {
		return err
	}
	// 迁移：添加 current_step 列（重复执行安全）
	_, _ = aiDB.Exec(`ALTER TABLE batch_tasks ADD COLUMN current_step TEXT NOT NULL DEFAULT ''`)
	_, _ = aiDB.Exec(`ALTER TABLE batch_tasks ADD COLUMN progress_done INTEGER NOT NULL DEFAULT 0`)
	_, _ = aiDB.Exec(`ALTER TABLE batch_tasks ADD COLUMN progress_total INTEGER NOT NULL DEFAULT 0`)
	return nil
}

// ListBatchTasks 返回所有任务（按创建时间倒序）
func ListBatchTasks() ([]BatchTask, error) {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return nil, fmt.Errorf("AI DB 未就绪")
	}
	rows, err := db.Query(
		`SELECT id, contact_key, username, is_group, status, current_step, progress_done, progress_total, error, created_at, updated_at
		 FROM batch_tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []BatchTask
	for rows.Next() {
		var t BatchTask
		var isGroup int
		rows.Scan(&t.ID, &t.ContactKey, &t.Username, &isGroup, &t.Status, &t.CurrentStep, &t.ProgressDone, &t.ProgressTotal, &t.Error, &t.CreatedAt, &t.UpdatedAt)
		t.IsGroup = isGroup != 0
		tasks = append(tasks, t)
	}
	return tasks, nil
}

// EnqueueBatchTasks 批量入队任务，并确保 worker 在运行
func EnqueueBatchTasks(tasks []BatchTask) error {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return fmt.Errorf("AI DB 未就绪")
	}
	now := time.Now().Unix()
	for _, t := range tasks {
		_, err := db.Exec(
			`INSERT INTO batch_tasks(contact_key, username, is_group, status, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
			t.ContactKey, t.Username, t.IsGroup, BatchTaskPending, now, now,
		)
		if err != nil {
			return fmt.Errorf("入队失败: %w", err)
		}
	}
	go ensureBatchWorker()
	return nil
}

// DeleteBatchTask 删除单个任务
func DeleteBatchTask(id int64) error {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return fmt.Errorf("AI DB 未就绪")
	}
	_, err := db.Exec("DELETE FROM batch_tasks WHERE id = ?", id)
	return err
}

// ClearBatchTasks 清除所有已完成/出错的任务
func ClearBatchTasks() error {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return fmt.Errorf("AI DB 未就绪")
	}
	_, err := db.Exec("DELETE FROM batch_tasks WHERE status IN (?, ?)", BatchTaskDone, BatchTaskError)
	return err
}

// ── Worker ───────────────────────────────────────────────────────────────────

// ensureBatchWorker 确保有一个 worker goroutine 在处理队列
func ensureBatchWorker() {
	batchQueueMu.Lock()
	defer batchQueueMu.Unlock()
	if batchWorkerRunning {
		return
	}
	batchWorkerRunning = true
	go batchWorkerLoop()
}

// batchWorkerLoop 串行处理队列中的 pending 任务
func batchWorkerLoop() {
	batchQueueMu.Lock()
	batchWorkerRunning = true
	batchAbortCh = make(chan struct{})
	abortCh := batchAbortCh
	batchQueueMu.Unlock()

	defer func() {
		batchQueueMu.Lock()
		batchWorkerRunning = false
		batchAbortCh = nil
		batchQueueMu.Unlock()
		if r := recover(); r != nil {
			log.Printf("[BATCH] worker panic: %v", r)
		}
	}()

	for {
		task, err := claimNextBatchTask()
		if err != nil {
			log.Printf("[BATCH] claim error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if task == nil {
			return
		}

		log.Printf("[BATCH] 开始处理: %s (%s)", task.ContactKey, task.Username)
		err = processBatchTask(task, abortCh)
		if err != nil {
			log.Printf("[BATCH] 任务失败 %s: %v", task.ContactKey, err)
			updateBatchTaskStatus(task.ID, BatchTaskError, err.Error())
		} else {
			log.Printf("[BATCH] 任务完成: %s", task.ContactKey)
			updateBatchTaskStatus(task.ID, BatchTaskDone, "")
		}
	}
}

// claimNextBatchTask 原子地取出下一个 pending 任务并标记为 running
func claimNextBatchTask() (*BatchTask, error) {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return nil, fmt.Errorf("AI DB 未就绪")
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}

	var t BatchTask
	var isGroup int
	err = tx.QueryRow(
		`SELECT id, contact_key, username, is_group, status, current_step, error, created_at, updated_at
		 FROM batch_tasks WHERE status = ? ORDER BY created_at ASC LIMIT 1`,
		BatchTaskPending,
	).Scan(&t.ID, &t.ContactKey, &t.Username, &isGroup, &t.Status, &t.CurrentStep, &t.Error, &t.CreatedAt, &t.UpdatedAt)

	if err == sql.ErrNoRows {
		tx.Rollback()
		return nil, nil
	}
	if err != nil {
		tx.Rollback()
		return nil, err
	}

	t.IsGroup = isGroup != 0
	now := time.Now().Unix()
	_, err = tx.Exec(
		`UPDATE batch_tasks SET status = ?, updated_at = ? WHERE id = ?`,
		BatchTaskRunning, now, t.ID,
	)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	tx.Commit()
	return &t, nil
}

// updateBatchTaskStatus 更新任务状态
func updateBatchTaskStatus(id int64, status, errMsg string) {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return
	}
	now := time.Now().Unix()
	_, err := db.Exec(
		`UPDATE batch_tasks SET status = ?, error = ?, updated_at = ? WHERE id = ?`,
		status, errMsg, now, id,
	)
	if err != nil {
		log.Printf("[BATCH] 更新状态失败: %v", err)
	}
}

// updateBatchTaskStep 更新任务当前步骤
func updateBatchTaskStep(id int64, step string) {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return
	}
	now := time.Now().Unix()
	_, err := db.Exec(
		`UPDATE batch_tasks SET current_step = ?, updated_at = ? WHERE id = ?`,
		step, now, id,
	)
	if err != nil {
		log.Printf("[BATCH] 更新步骤失败: %v", err)
	}
}

// updateBatchTaskProgress 更新任务进度
func updateBatchTaskProgress(id int64, done, total int) {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return
	}
	now := time.Now().Unix()
	_, err := db.Exec(
		`UPDATE batch_tasks SET progress_done = ?, progress_total = ?, updated_at = ? WHERE id = ?`,
		done, total, now, id,
	)
	if err != nil {
		log.Printf("[BATCH] 更新进度失败: %v", err)
	}
}

// StopAllBatchTasks 停止所有任务并清空队列
func StopAllBatchTasks() error {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return fmt.Errorf("AI DB 未就绪")
	}
	// 关闭 abort channel，中断正在运行的提炼
	batchQueueMu.Lock()
	batchWorkerRunning = false
	if batchAbortCh != nil {
		// safe close pattern
		select {
		case <-batchAbortCh:
		default:
			close(batchAbortCh)
		}
	}
	batchAbortCh = nil
	batchQueueMu.Unlock()
	// 中止所有正在运行的 vec jobs
	vecJobsMu.Lock()
	for _, j := range vecJobs {
		j.safeAbort()
	}
	vecJobs = make(map[string]*vecBuildJob)
	vecJobsMu.Unlock()
	// 清空所有批量任务
	_, err := db.Exec("DELETE FROM batch_tasks")
	return err
}

// ── Pipeline ─────────────────────────────────────────────────────────────────

// processBatchTask 处理单个任务：FTS → 向量索引 → 记忆提炼
func processBatchTask(task *BatchTask, abortCh <-chan struct{}) error {
	svc := batchSvc()
	if svc == nil {
		return fmt.Errorf("服务不可用")
	}

	prefs := loadPreferences()
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return fmt.Errorf("AI DB 未就绪")
	}

	// ── 1. 检查 FTS 索引，不存在则构建 ──
	ftsStatus, err := GetFTSIndexStatus(task.ContactKey)
	if err != nil {
		return fmt.Errorf("检查 FTS 状态失败: %w", err)
	}
	if !ftsStatus.Built {
		updateBatchTaskStep(task.ID, "fts")
		updateBatchTaskProgress(task.ID, 0, 0)
		log.Printf("[BATCH] 构建 FTS 索引: %s", task.ContactKey)
		if err := buildFTSIndexSync(task.ContactKey, task.Username, task.IsGroup, svc); err != nil {
			return fmt.Errorf("FTS 索引构建失败: %w", err)
		}
	}

	// ── 2. 检查向量索引，不存在则构建 ──
	vecStatus, err := GetVecIndexStatus(task.ContactKey)
	if err != nil {
		return fmt.Errorf("检查向量索引状态失败: %w", err)
	}
	if !vecStatus.Built {
		updateBatchTaskStep(task.ID, "vec_index")
		updateBatchTaskProgress(task.ID, 0, 0)
		log.Printf("[BATCH] 构建向量索引: %s", task.ContactKey)
		if err := buildVecIndexSync(task.ContactKey, task.Username, task.IsGroup, svc, prefs); err != nil {
			return fmt.Errorf("向量索引构建失败: %w", err)
		}
	}

	// ── 3. 运行记忆提炼 ──
	updateBatchTaskStep(task.ID, "mem_extraction")
	log.Printf("[BATCH] 记忆提炼: %s", task.ContactKey)
	progressCb := func(done, total int) {
		updateBatchTaskProgress(task.ID, done, total)
	}
	chunkDoneCb := func(chunkIdx int) {
		// 每批完成后写检查点，服务重启后可续传
		db.Exec(`UPDATE vec_index_status SET extract_offset = ?, extract_version = ? WHERE contact_key = ?`,
			chunkIdx, memFactVersion, task.ContactKey)
	}
	if err := runMemExtractionSyncWithProgress(task.ContactKey, task.Username, task.IsGroup, svc, prefs, db, progressCb, chunkDoneCb, abortCh); err != nil {
		return fmt.Errorf("记忆提炼失败: %w", err)
	}

	return nil
}

// buildFTSIndexSync 同步构建 FTS 索引（非 SSE 版本，复用 service 层）
func buildFTSIndexSync(key, username string, isGroup bool, svc *service.ContactService) error {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return fmt.Errorf("AI DB 未就绪")
	}

	var msgs []rawMsg
	if isGroup {
		for _, m := range svc.ExportGroupMessagesAll(username) {
			if len(m.Content) > 0 && m.Content[0] == '[' {
				continue
			}
			msgs = append(msgs, rawMsg{m.Date + " " + m.Time, m.Speaker, m.Content})
		}
	} else {
		for _, m := range svc.ExportContactMessagesAll(username) {
			if len(m.Content) > 0 && m.Content[0] == '[' {
				continue
			}
			sender := "对方"
			if m.IsMine {
				sender = "我"
			}
			msgs = append(msgs, rawMsg{m.Date + " " + m.Time, sender, m.Content})
		}
	}

	total := len(msgs)
	if total == 0 {
		return fmt.Errorf("该联系人暂无可索引的文本消息")
	}

	if _, err := db.Exec("DELETE FROM msg_fts WHERE contact_key = ?", key); err != nil {
		return fmt.Errorf("清理旧 FTS 索引失败: %w", err)
	}

	const batchSize = 500
	for i := 0; i < total; i += batchSize {
		end := i + batchSize
		if end > total {
			end = total
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		stmt, err := tx.Prepare(
			"INSERT INTO msg_fts(content, sender, datetime, contact_key, seq) VALUES(?,?,?,?,?)")
		if err != nil {
			tx.Rollback()
			return err
		}
		for j := i; j < end; j++ {
			m := msgs[j]
			if _, err := stmt.Exec(m.Content, m.Sender, m.DateTime, key, j); err != nil {
				stmt.Close()
				tx.Rollback()
				return err
			}
		}
		stmt.Close()
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	_, err := db.Exec(
		`INSERT INTO fts_index_status(contact_key, msg_count, built_at) VALUES(?,?,?)
		 ON CONFLICT(contact_key) DO UPDATE SET msg_count=excluded.msg_count, built_at=excluded.built_at`,
		key, total, time.Now().Unix(),
	)
	return err
}

// buildVecIndexSync 同步构建向量索引
func buildVecIndexSync(key, username string, isGroup bool, svc *service.ContactService, prefs Preferences) error {
	buildVecIndexCore(key, username, isGroup, svc, prefs, func(vecIndexProgress) {})
	return nil
}

// runMemExtractionSync 同步运行记忆提炼
func runMemExtractionSync(key, username string, isGroup bool, svc *service.ContactService, prefs Preferences, db *sql.DB) error {
	return runMemExtractionSyncWithProgress(key, username, isGroup, svc, prefs, db, nil, nil, nil)
}

func runMemExtractionSyncWithProgress(key, username string, isGroup bool, svc *service.ContactService, prefs Preferences, db *sql.DB, onProgress func(done, total int), onChunkDone func(chunkIdx int), abortCh <-chan struct{}) error {
	rows, err := db.Query(
		`SELECT datetime, sender, content FROM vec_messages WHERE contact_key = ? ORDER BY seq`, key)
	if err != nil {
		return err
	}
	var msgs []rawMsg
	for rows.Next() {
		var m rawMsg
		rows.Scan(&m.DateTime, &m.Sender, &m.Content)
		msgs = append(msgs, m)
	}
	rows.Close()

	if len(msgs) == 0 {
		return fmt.Errorf("语义向量索引为空")
	}

	// ── 检查点：判断是续传还是全新开始 ──────────────────────────────────
	startChunk := 0
	var prevOffset int = -1
	var prevVersion int
	db.QueryRow("SELECT extract_offset, extract_version FROM vec_index_status WHERE contact_key = ?", key).Scan(&prevOffset, &prevVersion)
	// 版本不匹配时忽略旧检查点
	if prevVersion != memFactVersion {
		prevOffset = -1
	}
	if prevOffset >= 0 {
		// 上次中断于 prevOffset 批（已完成），从下一批续传
		startChunk = prevOffset + 1
	} else {
		// 全新开始：清空旧事实
		if _, err := db.Exec("DELETE FROM mem_facts WHERE contact_key = ? AND version = ?", key, memFactVersion); err != nil {
			return fmt.Errorf("清理旧记忆失败: %w", err)
		}
	}

	embConfigs := embeddingConfigs(prefs)
	_, err = extractAndStoreFacts(key, msgs, prefs, db, embConfigs,
		isGroup, username,
		startChunk,
		onProgress, onChunkDone, abortCh,
	)
	if err == nil {
		// 全部完成，清除检查点
		db.Exec(`UPDATE vec_index_status SET extract_offset = -1, extract_version = ? WHERE contact_key = ?`, memFactVersion, key)
	}
	return err
}

// ResumeBatchTasks 在服务启动时恢复未完成的任务
func ResumeBatchTasks() {
	aiDBMu.Lock()
	db := aiDB
	aiDBMu.Unlock()
	if db == nil {
		return
	}

	now := time.Now().Unix()
	_, err := db.Exec(
		`UPDATE batch_tasks SET status = ?, updated_at = ? WHERE status = ?`,
		BatchTaskPending, now, BatchTaskRunning,
	)
	if err != nil {
		log.Printf("[BATCH] 恢复任务失败: %v", err)
		return
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM batch_tasks WHERE status = ?", BatchTaskPending).Scan(&count)
	if count > 0 {
		log.Printf("[BATCH] 恢复 %d 个待处理任务", count)
		go ensureBatchWorker()
	}
}
