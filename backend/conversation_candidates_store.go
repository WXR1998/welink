package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// initConversationCandidatesTable 在 aiDB 中创建按会话保存“原文候选”的表。
// 由 InitAIDB 在持有 aiDBMu 时调用。
func initConversationCandidatesTable() error {
	_, err := aiDB.Exec(`CREATE TABLE IF NOT EXISTS ai_conversation_candidates (
		conversation_key TEXT PRIMARY KEY,
		candidates       TEXT NOT NULL DEFAULT '[]',
		updated_at       INTEGER NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("conversation_candidates: create table: %w", err)
	}
	return nil
}

// saveConversationCandidates 覆盖保存一个会话的原文候选（RawExcerpt 列表）。
func saveConversationCandidates(convKey string, candidates []RawExcerpt) {
	if convKey == "" {
		return
	}
	raw, err := json.Marshal(candidates)
	if err != nil {
		return
	}
	aiDBMu.Lock()
	defer aiDBMu.Unlock()
	if aiDB == nil {
		return
	}
	_, _ = aiDB.Exec(`
		INSERT INTO ai_conversation_candidates (conversation_key, candidates, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(conversation_key) DO UPDATE SET candidates = excluded.candidates, updated_at = excluded.updated_at`,
		convKey, string(raw), time.Now().Unix())
}

// getConversationCandidates 返回一个会话已保存的原文候选。
func getConversationCandidates(convKey string) []RawExcerpt {
	if convKey == "" {
		return nil
	}
	aiDBMu.Lock()
	defer aiDBMu.Unlock()
	if aiDB == nil {
		return nil
	}
	var raw string
	err := aiDB.QueryRow(`SELECT candidates FROM ai_conversation_candidates WHERE conversation_key = ?`, convKey).Scan(&raw)
	if err == sql.ErrNoRows || err != nil {
		return nil
	}
	var out []RawExcerpt
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

// deleteConversationCandidates 删除一个会话的候选（随会话清空/重置时调用）。
func deleteConversationCandidates(convKey string) {
	if convKey == "" {
		return
	}
	aiDBMu.Lock()
	defer aiDBMu.Unlock()
	if aiDB == nil {
		return
	}
	_, _ = aiDB.Exec(`DELETE FROM ai_conversation_candidates WHERE conversation_key = ?`, convKey)
}
