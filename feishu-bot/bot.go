package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/channel"
	"github.com/larksuite/oapi-sdk-go/v3/channel/types"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// bot 持有飞书通道与配置，并维护会话级上下文。
type bot struct {
	cfg      *Config
	ch       types.Channel
	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	history []string // 最近若干轮 user 问题（轻量记忆，避免无限增长）
}

// newBot 构造飞书客户端与高层 channel。
func newBot(ctx context.Context, cfg *Config) (*bot, error) {
	client := lark.NewClient(cfg.FeishuAppID, cfg.FeishuAppSecret,
		lark.WithLogLevel(larkcore.LogLevelInfo),
	)
	wsClient := larkws.NewClient(cfg.FeishuAppID, cfg.FeishuAppSecret,
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)
	b := &bot{
		cfg:      cfg,
		sessions: map[string]*session{},
	}
	b.ch = channel.NewChannel(client, wsClient)

	b.ch.OnError(func(err error) {
		log.Printf("[bot] 飞书通道错误: %v", err)
	})
	b.ch.OnReconnected(func() {
		log.Printf("[bot] 飞书通道已重连")
	})
	b.ch.OnMessage(func(ctx context.Context, msg *types.NormalizedMessage) error {
		b.handleMessage(ctx, msg)
		return nil
	})
	return b, nil
}

// run 启动飞书长连接（阻塞直到退出）。
func (b *bot) run(ctx context.Context) error {
	log.Printf("[bot] 正在启动飞书长连接，app_id=%s", b.cfg.FeishuAppID)
	return b.ch.Start(ctx)
}

// handleMessage 收到消息后异步处理，避免阻塞飞书 3 秒事件约束。
func (b *bot) handleMessage(ctx context.Context, msg *types.NormalizedMessage) {
	if msg == nil || msg.Content == "" {
		return
	}
	// 群聊只在 @ 机器人时响应；单聊直接响应。
	if msg.ChatType == "group" && !msg.MentionedBot {
		return
	}
	if !b.allowed(msg) {
		b.safeSend(ctx, msg, "你没有权限使用本机器人。")
		return
	}
	question := cleanMention(msg.Content)
	if strings.TrimSpace(question) == "" {
		b.safeSend(ctx, msg, "请输入问题，例如：我和张三最近聊了什么？")
		return
	}

	go b.process(ctx, msg, question)
}

// process 执行一次问答，按配置选择 Markdown 或卡片流式回复。
func (b *bot) process(ctx context.Context, msg *types.NormalizedMessage, question string) {
	sessionKey := sessionKeyFor(msg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if b.cfg.StreamMode == "card" {
		b.processCard(ctx, msg, question, sessionKey)
		return
	}
	b.processMarkdown(ctx, msg, question, sessionKey)
}

// processMarkdown 用飞书 Markdown 富文本（post）流式逐段更新，适合长回答。
func (b *bot) processMarkdown(ctx context.Context, msg *types.NormalizedMessage, question, sessionKey string) {
	stream, err := b.ch.Stream(ctx, &types.SendInput{
		ChatID:   msg.ChatID,
		Title:    "AI 回答",
		Markdown: "⏳ 正在分析你的问题…",
	})
	if err != nil {
		log.Printf("[bot] 创建 Markdown 流式消息失败: %v", err)
		return
	}
	defer func() { _ = stream.Close(context.Background()) }()

	_ = stream.Append(ctx, "\n\n🔎 正在检索聊天记录…")
	out, errMsg := b.runQuery(ctx, sessionKey, question)
	if errMsg != "" {
		_ = stream.Append(ctx, "\n\n❌ "+errMsg)
		return
	}
	if out.Answer == "" {
		_ = stream.Append(ctx, "\n\n（没有生成可展示的回答，可能没有检索到相关内容。）")
		return
	}

	_ = stream.Append(ctx, "\n\n✍️ 正在整理回答…")
	_ = stream.Append(ctx, "\n\n"+out.Answer)
	b.remember(sessionKey, question)
	_ = stream.Flush(ctx)
}

// processCard 用飞书卡片流式更新，适合需要组件级进度/步骤的展示。
func (b *bot) processCard(ctx context.Context, msg *types.NormalizedMessage, question, sessionKey string) {
	stream, err := b.ch.Stream(ctx, &types.SendInput{
		ChatID: msg.ChatID,
		Title:  "AI 回答",
		Card:   cardWithText("⏳ 正在分析你的问题…"),
	})
	if err != nil {
		log.Printf("[bot] 创建卡片流式消息失败: %v", err)
		return
	}
	defer func() { _ = stream.Close(context.Background()) }()

	_ = stream.UpdateCard(ctx, cardWithText("🔎 正在检索聊天记录…\n\n处理中…"))
	out, errMsg := b.runQuery(ctx, sessionKey, question)
	if errMsg != "" {
		_ = stream.UpdateCard(ctx, cardWithText("❌ "+errMsg))
		return
	}
	if out.Answer == "" {
		_ = stream.UpdateCard(ctx, cardWithText("（没有生成可展示的回答，可能没有检索到相关内容。）"))
		return
	}

	_ = stream.UpdateCard(ctx, cardWithText("✍️ 正在整理回答…\n\n🔎 检索完成，正在生成回答：\n\n"+out.Answer))
	b.remember(sessionKey, question)
}

// runQuery 执行一次 RAG 检索并返回结果；errMsg 非空表示有可展示给用户的错误。
func (b *bot) runQuery(ctx context.Context, sessionKey, question string) (*ragOutcome, string) {
	history := b.historyOf(sessionKey)
	if !b.keyAllowed(b.keyFor()) {
		return nil, "当前配置的 AI key 不在 allow 列表内，无法检索。"
	}
	out, err := askRAG(ctx, b.cfg, b.keyFor(), question, history)
	if err != nil {
		return nil, "调用 AI 接口失败，请稍后重试。\n\n" + err.Error()
	}
	if out.Error != "" {
		return nil, out.Error
	}
	return out, ""
}

// safeSend 发送一条普通文本（用于权限/引导等简单回复）。
func (b *bot) safeSend(ctx context.Context, msg *types.NormalizedMessage, text string) {
	if msg == nil {
		return
	}
	ctxT, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _ = b.ch.Send(ctxT, &types.SendInput{
		ChatID:  msg.ChatID,
		MsgType: "text",
		Text:    text,
	})
}

func (b *bot) allowed(msg *types.NormalizedMessage) bool {
	if len(b.cfg.AllowedUsers) == 0 {
		return true // 未配置白名单，默认放行（README 提示需配置）
	}
	for _, u := range b.cfg.AllowedUsers {
		if u == msg.UserID {
			return true
		}
	}
	return false
}

// keyAllowed 若配置了 ALLOWED_KEYS，则只放行命中项；未配置时放行。
func (b *bot) keyAllowed(key string) bool {
	if len(b.cfg.AllowedKeys) == 0 {
		return true
	}
	for _, k := range b.cfg.AllowedKeys {
		if k == key {
			return true
		}
	}
	return false
}

func sessionKeyFor(msg *types.NormalizedMessage) string {
	if msg.ChatType == "group" {
		return fmt.Sprintf("group:%s:%s", msg.ChatID, msg.UserID)
	}
	return fmt.Sprintf("p2p:%s", msg.UserID)
}

// keyFor 决定调用 WeLink 时使用的 contact key。
func (b *bot) keyFor() string {
	if b.cfg.DefaultKey != "" {
		return b.cfg.DefaultKey
	}
	return ""
}

func (b *bot) historyOf(key string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[key]
	if s == nil {
		return ""
	}
	return strings.Join(s.history, "\n")
}

func (b *bot) remember(key, q string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sessions[key] == nil {
		b.sessions[key] = &session{}
	}
	s := b.sessions[key]
	s.history = append(s.history, q)
	if len(s.history) > 6 {
		s.history = s.history[len(s.history)-6:]
	}
}

// cleanMention 去掉消息里的 @ 机器人 文本，避免把它塞进问题。
func cleanMention(content string) string {
	s := strings.ReplaceAll(content, "@_user_1", "")
	s = strings.ReplaceAll(s, "@_user_2", "")
	s = strings.ReplaceAll(s, "<at id=xxx>", "")
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "/ai")
	return strings.TrimSpace(s)
}

// cardWithText 构造一个最简单的飞书卡片 JSON 2.0 文本卡片，便于卡片流式模式使用。
func cardWithText(text string) string {
	return fmt.Sprintf(`{"config":{"streaming_mode":true},"header":{"title":{"tag":"plain_text","content":"AI 回答"}},"elements":[{"tag":"markdown","content":%q}]}`, text)
}
