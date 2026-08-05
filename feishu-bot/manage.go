package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

// manageSession 是管理端点暴露的会话元数据（不含聊天正文，避免敏感内容外溢）。
type manageSession struct {
	Key        string    `json:"key"`
	ChatID     string    `json:"chat_id,omitempty"`
	UserID     string    `json:"user_id,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
	LastActive time.Time `json:"last_active"`
	MsgCount   int       `json:"msg_count"`
	Chars      int       `json:"chars"`
	Compressed bool      `json:"compressed,omitempty"`
	Entity     string    `json:"entity,omitempty"`
}

// startManageServer 启动内部管理 HTTP 服务，返回可停止的函数。
// 仅用于前端「上下文管理」查看/删除；不对外暴露完整聊天正文。
func (b *bot) startManageServer(ctx context.Context, addr string) func() {
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			b.listSessions(w, r)
		case http.MethodDelete:
			b.deleteSession(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		log.Printf("[manage] 内部管理服务监听 %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[manage] 内部管理服务错误: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	return func() { _ = srv.Close() }
}

// listSessions 返回全部会话元数据（升序，配合前端展示）。
func (b *bot) listSessions(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	keys := make([]string, 0, len(b.sessions))
	for k := range b.sessions {
		keys = append(keys, k)
	}
	items := make([]manageSession, 0, len(keys))
	for _, k := range keys {
		s := b.sessions[k]
		chatID, userID := keyParts(k)
		chars := 0
		for _, m := range s.history {
			chars += len(m.Content)
		}
		entity := ""
		if len(s.entities) > 0 {
			entity = strings.Join(s.entities, "、")
		}
		items = append(items, manageSession{
			Key:        k,
			ChatID:     chatID,
			UserID:     userID,
			CreatedAt:  s.createdAt,
			LastActive: s.lastActive,
			MsgCount:   len(s.history),
			Chars:      chars,
			Compressed: s.compressed,
			Entity:     entity,
		})
	}
	b.mu.Unlock()
	// 稳定排序：按最后活跃时间降序
	// （简单插入排序即可，条目数通常很小）
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j-1].LastActive.Before(items[j].LastActive); j-- {
			items[j-1], items[j] = items[j], items[j-1]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
}

// deleteSession 删除指定会话 key 的上下文（key 通过查询参数传入）。
func (b *bot) deleteSession(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	if _, ok := b.sessions[key]; !ok {
		b.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"deleted": false})
		return
	}
	b.sessions[key].history = nil
	b.sessions[key].createdAt = time.Now()
	b.sessions[key].lastActive = time.Now()
	b.sessions[key].compressed = false
	b.sessions[key].entities = nil
	b.saveLocked()
	b.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// keyParts 从 session key 拆出 chat_id 和 user_id（便于前端展示所属群/人）。
func keyParts(key string) (chatID, userID string) {
	if strings.HasPrefix(key, "group:") {
		rest := strings.TrimPrefix(key, "group:")
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) == 2 {
			return parts[0], parts[1]
		}
		if len(parts) == 1 {
			return parts[0], ""
		}
		return "", ""
	}
	if strings.HasPrefix(key, "p2p:") {
		return "", strings.TrimPrefix(key, "p2p:")
	}
	return "", key
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
