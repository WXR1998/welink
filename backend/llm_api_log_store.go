package main

import (
	"fmt"
	"time"
)

// initLLMApiLogTable 在 aiDB 中创建 LLM API 调用日志表。
// 必须在 aiDBMu 持有期间调用（由 InitAIDB 调用），保证 aiDB 已初始化。
func initLLMApiLogTable() error {
	_, err := aiDB.Exec(`CREATE TABLE IF NOT EXISTS llm_api_logs (
		id           INTEGER PRIMARY KEY,
		timestamp    TEXT    NOT NULL,
		method       TEXT    NOT NULL,
		url          TEXT    NOT NULL,
		provider     TEXT    NOT NULL DEFAULT '',
		model        TEXT    NOT NULL DEFAULT '',
		feature      TEXT    NOT NULL DEFAULT '',
		request_body TEXT    NOT NULL DEFAULT '',
		status       INTEGER NOT NULL DEFAULT 0,
		response_body TEXT   NOT NULL DEFAULT '',
		first_token_ms INTEGER NOT NULL DEFAULT 0,
		duration_ms  INTEGER NOT NULL DEFAULT 0,
		error        TEXT    NOT NULL DEFAULT ''
	)`)
	if err != nil {
		return fmt.Errorf("llm_api_log: create table: %w", err)
	}
	if err := ensureLLMApiLogFirstTokenColumn(); err != nil {
		return fmt.Errorf("llm_api_log: add first token column: %w", err)
	}
	return nil
}

func ensureLLMApiLogFirstTokenColumn() error {
	rows, err := aiDB.Query(`PRAGMA table_info(llm_api_logs)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == "first_token_ms" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = aiDB.Exec(`ALTER TABLE llm_api_logs ADD COLUMN first_token_ms INTEGER NOT NULL DEFAULT 0`)
	return err
}

// seedLLMApiLogsFromDB 把数据库里保存的最近日志灌入内存环形缓冲区。
// 必须在 aiDBMu 持有期间调用（由 InitAIDB 在 initLLMApiLogTable 之后调用）。
func seedLLMApiLogsFromDB() {
	rows, err := aiDB.Query(`SELECT id, timestamp, method, url, provider, model, feature,
		request_body, status, response_body, first_token_ms, duration_ms, error
		FROM llm_api_logs ORDER BY id DESC LIMIT ?`, maxLLMApiLogs)
	if err != nil {
		return
	}
	defer rows.Close()

	logs := make([]LLMApiLogEntry, 0, maxLLMApiLogs)
	for rows.Next() {
		var e LLMApiLogEntry
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.Method, &e.URL, &e.Provider, &e.Model, &e.Feature,
			&e.RequestBody, &e.Status, &e.ResponseBody, &e.FirstTokenMs, &e.DurationMs, &e.Error); err != nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			e.Timestamp = t
		}
		logs = append(logs, e)
	}
	// DB 按 id 降序返回（最新在前），这里转成升序，与内存逻辑保持一致。
	for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 {
		logs[i], logs[j] = logs[j], logs[i]
	}
	llmApiLogs = logs
	if len(logs) > 0 {
		llmApiLogSeq = logs[len(logs)-1].ID
	} else {
		llmApiLogSeq = 0
	}
}

// persistLLMApiLog 把一条日志写入数据库。
func persistLLMApiLog(e LLMApiLogEntry) {
	aiDBMu.Lock()
	defer aiDBMu.Unlock()
	if aiDB == nil {
		return
	}
	_, _ = aiDB.Exec(`INSERT INTO llm_api_logs (id, timestamp, method, url, provider, model, feature,
		request_body, status, response_body, first_token_ms, duration_ms, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Timestamp.Format(time.RFC3339Nano), e.Method, e.URL, e.Provider, e.Model, e.Feature,
		e.RequestBody, e.Status, e.ResponseBody, e.FirstTokenMs, e.DurationMs, e.Error)
}

// trimLLMApiLogsDB 只保留数据库里最新的 maxLLMApiLogs 条，防止无限增长。
func trimLLMApiLogsDB() {
	aiDBMu.Lock()
	defer aiDBMu.Unlock()
	if aiDB == nil {
		return
	}
	_, _ = aiDB.Exec(`
		DELETE FROM llm_api_logs WHERE id NOT IN (
			SELECT id FROM llm_api_logs ORDER BY id DESC LIMIT ?
		)`, maxLLMApiLogs)
}

// clearLLMApiLogsDB 清空数据库里的全部 LLM API 日志。
func clearLLMApiLogsDB() {
	aiDBMu.Lock()
	defer aiDBMu.Unlock()
	if aiDB == nil {
		return
	}
	_, _ = aiDB.Exec(`DELETE FROM llm_api_logs`)
}
