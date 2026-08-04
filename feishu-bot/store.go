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
