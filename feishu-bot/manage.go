package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// manageSession 是管理端点暴露的会话元数据（不含聊天正文，避免敏感内容外溢）。
type manageSession struct {
	Key        string    `json:"key"`
	ChatID     string    `json:"chat_id,omitempty"`
	UserID     string    `json:"user_id,omitempty"`
	ChatName   string    `json:"chat_name,omitempty"`
	UserName   string    `json:"user_name,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
	LastActive time.Time `json:"last_active"`
	MsgCount   int       `json:"msg_count"`
	Chars      int       `json:"chars"`
	Compressed bool      `json:"compressed,omitempty"`
	Entity     string    `json:"entity,omitempty"`
	// ContentPreview 是截断到 maxContextPreviewBytes 的上下文正文预览。
	ContentPreview string `json:"content_preview,omitempty"`
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
	b.ensureNames()
	b.mu.Lock()
	keys := make([]string, 0, len(b.sessions))
	for k := range b.sessions {
		keys = append(keys, k)
	}
	items := make([]manageSession, 0, len(keys))
	needSave := false
	for _, k := range keys {
		s := b.sessions[k]
		// 惰性清理：超过会话空闲 TTL 的上下文视为已过期，列出时直接清空并跳过。
		if time.Since(s.lastActive) > sessionIdleTTL {
			s.history = []llmMessage{}
			s.createdAt = time.Now()
			s.compressed = false
			s.entities = nil
			needSave = true
			continue
		}
		// 已被前端删除或从未产生问答的会话：历史为空，直接跳过，
		// 避免刷新后"空上下文"再次出现。
		if len(s.history) == 0 {
			continue
		}
		chatID, userID := keyParts(k)
		chars := 0
		for _, m := range s.history {
			chars += len(m.Content)
		}
		entity := ""
		if len(s.entities) > 0 {
			entity = strings.Join(s.entities, "、")
		}
		chatName, userName := b.displayNameFromCache(chatID, userID)
		items = append(items, manageSession{
			Key:            k,
			ChatID:         chatID,
			UserID:         userID,
			ChatName:       chatName,
			UserName:       userName,
			CreatedAt:      s.createdAt,
			LastActive:     s.lastActive,
			MsgCount:       len(s.history),
			Chars:          chars,
			Compressed:     s.compressed,
			Entity:         entity,
			ContentPreview: b.contextPreview(s),
		})
	}
	if needSave {
		b.saveLocked()
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

// maxContextPreviewBytes 限制管理端返回的上下文正文预览大小（20KB）。
const maxContextPreviewBytes = 20 * 1024

// nameCacheTTL 控制群名/成员名的缓存刷新周期，避免管理端高频调用飞书 API。
const nameCacheTTL = 5 * time.Minute

// ensureNames 确保群名/成员名缓存可用；过期时在锁外刷新一次。
func (b *bot) ensureNames() {
	b.mu.Lock()
	expired := b.nameCacheAt.IsZero() || time.Since(b.nameCacheAt) > nameCacheTTL
	b.mu.Unlock()
	if !expired {
		return
	}
	b.refreshNames()
}

// refreshNames 在锁外拉取群名与成员名，完成后加锁写回缓存。
func (b *bot) refreshNames() {
	chatNames := b.enumerateChats()
	memberNames := map[string]map[string]string{}
	for chatID := range chatNames {
		members := map[string]string{}
		pageToken := ""
		for {
			req := larkim.NewGetChatMembersReqBuilder().
				ChatId(chatID).
				MemberIdType("open_id").
				PageSize(100)
			if pageToken != "" {
				req.PageToken(pageToken)
			}
			ctxT, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			resp, err := b.client.Im.V1.ChatMembers.Get(ctxT, req.Build())
			cancel()
			if err != nil || !resp.Success() {
				log.Printf("[manage] 获取群 %s 成员失败: err=%v code=%d msg=%s", chatID, err, resp.Code, resp.Msg)
				break
			}
			for _, m := range resp.Data.Items {
				if m.MemberId == nil || m.Name == nil {
					continue
				}
				members[*m.MemberId] = *m.Name
			}
			if resp.Data.HasMore == nil || !*resp.Data.HasMore || resp.Data.PageToken == nil || *resp.Data.PageToken == "" {
				break
			}
			pageToken = *resp.Data.PageToken
		}
		memberNames[chatID] = members
	}
	b.mu.Lock()
	b.chatNames = chatNames
	b.memberNames = memberNames
	b.nameCacheAt = time.Now()
	b.mu.Unlock()
}

// displayNameFromCache 从缓存读取群名与成员名。调用方须持有 b.mu。
func (b *bot) displayNameFromCache(chatID, userID string) (chatName, userName string) {
	if chatID != "" {
		chatName = b.chatNames[chatID]
		if userID != "" && b.memberNames[chatID] != nil {
			userName = b.memberNames[chatID][userID]
		}
	}
	return chatName, userName
}

// contextPreview 把会话历史拼成可读文本并截断到 maxContextPreviewBytes 字节。
func (b *bot) contextPreview(s *session) string {
	if s == nil {
		return ""
	}
	var sb strings.Builder
	for _, m := range s.history {
		role := "AI"
		if m.Role == "user" {
			role = "用户"
		} else if m.Role == "system" {
			role = "系统"
		}
		sb.WriteString("【" + role + "】")
		sb.WriteString(m.Content)
		sb.WriteString("\n\n")
	}
	raw := sb.String()
	if len(raw) <= maxContextPreviewBytes {
		return raw
	}
	return raw[:maxContextPreviewBytes] + "\n…[已截断]"
}
