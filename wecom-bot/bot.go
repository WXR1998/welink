package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type llmMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type answerFunc func(context.Context, string, []llmMessage) (string, error)

type bot struct {
	cfg      *Config
	send     func(commandFrame) error
	answer   answerFunc
	nextID   func() string
	mu       sync.Mutex
	sessions map[string][]llmMessage
	busy     map[string]bool
	seen     map[string]time.Time
}

func newBot(cfg *Config, send func(commandFrame) error) *bot {
	b := &bot{
		cfg:      cfg,
		send:     send,
		nextID:   newID,
		sessions: map[string][]llmMessage{},
		busy:     map[string]bool{},
		seen:     map[string]time.Time{},
	}
	b.answer = b.answerWeLink
	return b
}

func (b *bot) dispatch(msg incomingGroupText) {
	if !b.markSeen(msg.MessageID) {
		return
	}
	streamID := b.nextID()
	if !b.allowed(msg.UserID) {
		_ = b.send(newStreamReply(msg.RequestID, streamID, "你没有权限使用本机器人。", true))
		return
	}

	question := cleanMention(msg.Content, b.cfg.BotName)
	if question == "" {
		_ = b.send(newStreamReply(msg.RequestID, streamID, "请输入问题，例如：我和张三最近聊了什么？", true))
		return
	}

	key := sessionKeyFor(msg)
	history, ok := b.acquire(key)
	if !ok {
		_ = b.send(newStreamReply(msg.RequestID, streamID, "你上一个问题还在处理中，请稍候再提问。", true))
		return
	}
	go b.process(msg, key, question, history, streamID)
}

func (b *bot) process(msg incomingGroupText, key, question string, history []llmMessage, streamID string) {
	defer b.release(key)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := b.send(newStreamReply(msg.RequestID, streamID, "正在分析你的问题...", false)); err != nil {
		return
	}
	answer, err := b.answer(ctx, key, append(history, llmMessage{Role: "user", Content: question}))
	if err != nil {
		_ = b.send(newStreamReply(msg.RequestID, streamID, "生成回答失败，请稍后重试。", true))
		return
	}
	if strings.TrimSpace(answer) == "" {
		answer = "没有生成可展示的回答。"
	}
	b.remember(key, question, answer)
	_ = b.send(newStreamReply(msg.RequestID, streamID, answer, true))
}

func (b *bot) allowed(userID string) bool {
	if len(b.cfg.AllowedUsers) == 0 {
		return true
	}
	for _, allowed := range b.cfg.AllowedUsers {
		if allowed == userID {
			return true
		}
	}
	return false
}

func (b *bot) acquire(key string) ([]llmMessage, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.busy[key] {
		return nil, false
	}
	b.busy[key] = true
	return append([]llmMessage(nil), b.sessions[key]...), true
}

func (b *bot) release(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.busy, key)
}

func (b *bot) remember(key, question, answer string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	history := append(b.sessions[key], llmMessage{Role: "user", Content: question}, llmMessage{Role: "assistant", Content: answer})
	if len(history) > 24 {
		history = history[len(history)-24:]
	}
	b.sessions[key] = history
}

func (b *bot) markSeen(messageID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.seen[messageID]; exists {
		return false
	}
	b.seen[messageID] = time.Now()
	if len(b.seen) > 2048 {
		cutoff := time.Now().Add(-time.Hour)
		for id, at := range b.seen {
			if at.Before(cutoff) {
				delete(b.seen, id)
			}
		}
	}
	return true
}

func sessionKeyFor(msg incomingGroupText) string {
	return fmt.Sprintf("wecom:group:%s:%s", msg.ChatID, msg.UserID)
}

func cleanMention(content, botName string) string {
	content = strings.TrimSpace(content)
	if botName != "" {
		content = strings.TrimSpace(strings.TrimPrefix(content, "@"+botName))
	}
	if strings.HasPrefix(content, "@") {
		if i := strings.IndexAny(content, " \t\n"); i >= 0 {
			content = strings.TrimSpace(content[i:])
		}
	}
	return strings.TrimSpace(strings.TrimPrefix(content, "/ai"))
}
