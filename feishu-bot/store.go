package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// sessionDump 是 session 的可序列化形态，只持久化需要跨重启保留的字段。
type sessionDump struct {
	History    []llmMessage `json:"history"`
	LastActive time.Time    `json:"last_active"`
	Version    uint64       `json:"version"`
	Entities   []string     `json:"entities,omitempty"`
}

// sessionStore 负责把内存会话持久化到 JSON 文件。
type sessionStore struct {
	path string
	mu   sync.Mutex
}

// newSessionStore 返回一个基于文件的会话存储；path 为空表示不持久化。
func newSessionStore(path string) *sessionStore {
	return &sessionStore{path: path}
}

// Load 读取全部会话历史到内存 map。
func (st *sessionStore) Load() (map[string]*session, error) {
	if st == nil || st.path == "" {
		return map[string]*session{}, nil
	}
	b, err := os.ReadFile(st.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]*session{}, nil
		}
		return nil, err
	}
	var dumps map[string]sessionDump
	if err := json.Unmarshal(b, &dumps); err != nil {
		return nil, err
	}
	out := make(map[string]*session, len(dumps))
	for k, d := range dumps {
		out[k] = &session{
			history:    d.History,
			lastActive: d.LastActive,
			version:    d.Version,
			entities:   d.Entities,
		}
	}
	return out, nil
}

// Save 把内存会话写入文件（原子写：先写临时文件再 rename）。
func (st *sessionStore) Save(sessions map[string]*session) error {
	if st == nil || st.path == "" {
		return nil
	}
	dumps := make(map[string]sessionDump, len(sessions))
	for k, s := range sessions {
		dumps[k] = sessionDump{
			History:    s.history,
			LastActive: s.lastActive,
			Version:    s.version,
			Entities:   s.entities,
		}
	}
	b, err := json.MarshalIndent(dumps, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(st.path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}


// ─── 进行中卡片持久化 ────────────────────────────────────────────────────────

// pendingItem 记录一条正在处理的卡片消息（answer 尚未完成）。
type pendingItem struct {
	MessageID string `json:"message_id"`
	ChatID    string `json:"chat_id,omitempty"`
	SessionKey string `json:"session_key"`
	Question  string `json:"question,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// pendingStore 把"正在处理的卡片"持久化到 JSON，供重启后回滚半成品消息。
type pendingStore struct {
	path string
	mu   sync.Mutex
}

// newPendingStore 返回进行中卡片的持久化存储；path 为会话存储文件路径，
// pending 文件放在同一目录（sessions.json → pending.json）。
func newPendingStore(sessionStorePath string) *pendingStore {
	if sessionStorePath == "" {
		return &pendingStore{path: ""}
	}
	dir := filepath.Dir(sessionStorePath)
	return &pendingStore{path: filepath.Join(dir, "pending.json")}
}

// Load 读取全部进行中的卡片记录。
func (p *pendingStore) Load() (map[string]pendingItem, error) {
	if p == nil || p.path == "" {
		return map[string]pendingItem{}, nil
	}
	b, err := os.ReadFile(p.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]pendingItem{}, nil
		}
		return nil, err
	}
	var items map[string]pendingItem
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// Save 原子写进行中的卡片记录。
func (p *pendingStore) Save(items map[string]pendingItem) error {
	if p == nil || p.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(p.path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}
