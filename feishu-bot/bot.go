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
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const (
	maxHistoryMsgs = 12  // 上下文达到该消息数时触发压缩
	maxHistoryChars = 24000 // 上下文文本达到该字符数时触发压缩
	sessionIdleTTL = 2 * time.Hour // 2 小时无新提问自动新开会话
)

// bot 持有飞书通道与配置，并维护每个用户的独立会话。
type bot struct {
	cfg      *Config
	ch       types.Channel
	store    *sessionStore
	mu       sync.Mutex
	sessions map[string]*session
}

// session 表示单个用户在单个群聊/单聊中的独立上下文。
type session struct {
	history    []llmMessage // 完整问答历史（user + assistant）
	lastActive time.Time    // 最近一次提问时间
	compressed bool         // 是否已做过压缩摘要（供日志/调试）
	busy       bool         // 该会话是否正在回答中（冷却锁）
	version    uint64       // 每次追加问答递增，用于压缩写回时的并发保护
	entities   []string     // 最近成功解析出的实体展示名（追问时沿用）
}

// newBot 构造飞书客户端与高层 channel。
func newBot(ctx context.Context, cfg *Config) (*bot, error) {
	client := lark.NewClient(cfg.FeishuAppID, cfg.FeishuAppSecret,
		lark.WithLogLevel(larkcore.LogLevelInfo),
	)
	eventDispatcher := dispatcher.NewEventDispatcher("", "")
	wsClient := larkws.NewClient(cfg.FeishuAppID, cfg.FeishuAppSecret,
		larkws.WithLogLevel(larkcore.LogLevelInfo),
		larkws.WithEventHandler(eventDispatcher),
	)
	st := newSessionStore(cfg.SessionStorePath)
	loaded, err := st.Load()
	if err != nil {
		return nil, fmt.Errorf("加载会话历史失败: %w", err)
	}
	b := &bot{
		cfg:      cfg,
		store:    st,
		sessions: loaded,
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
	// 只有 @ 机器人的消息才会被视为提问（单聊始终响应）。
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

	// 回答中：同人再次提问直接拒绝
	sessionKey := sessionKeyFor(msg)
	if !b.acquireBusy(sessionKey) {
		b.safeSend(ctx, msg, "你上一个问题还在处理中，请稍候再提问。")
		return
	}

	go b.process(ctx, msg, question, sessionKey)
}

// acquireBusy 尝试锁定会话开始一次回答；已在回答中则返回 false。
func (b *bot) acquireBusy(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessionLocked(key)
	if s.busy {
		return false
	}
	s.busy = true
	return true
}

// releaseBusy 回答完成后释放会话锁。
func (b *bot) releaseBusy(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.sessions[key]; s != nil {
		s.busy = false
	}
}

// sessionLocked 返回（或创建）会话，调用方须持有 b.mu。
func (b *bot) sessionLocked(key string) *session {
	s := b.sessions[key]
	if s == nil {
		s = &session{history: []llmMessage{}, lastActive: time.Now()}
		b.sessions[key] = s
	}
	// 超过 2 小时没新提问 → 丢弃旧上下文，新起一个会话
	if time.Since(s.lastActive) > sessionIdleTTL {
		s.history = []llmMessage{}
		s.compressed = false
	}
	return s
}

// process 执行跨联系人问答并维护会话历史。
func (b *bot) process(ctx context.Context, msg *types.NormalizedMessage, question, sessionKey string) {
	defer b.releaseBusy(sessionKey)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	b.processCard(ctx, msg, question, sessionKey)
}

// processCard 用卡片流式更新，回复时引用用户的原始提问。
func (b *bot) processCard(ctx context.Context, msg *types.NormalizedMessage, question, sessionKey string) {
	stream, err := b.ch.Stream(ctx, &types.SendInput{
		ChatID:         msg.ChatID,
		ReplyMessageID: msg.MessageID,
		Card:           cardWithText("⏳ 正在分析你的问题…"),
	})
	if err != nil {
		log.Printf("[bot] 创建卡片流式消息失败: %v", err)
		return
	}
	defer func() { _ = stream.Close(context.Background()) }()

	_ = stream.UpdateCard(ctx, cardWithText("🔎 正在检索跨联系人聊天记录…\n\n处理中…"))
	answer, errMsg := b.answer(ctx, sessionKey, question)
	if errMsg != "" {
		_ = stream.UpdateCard(ctx, cardWithText("❌ "+errMsg))
		return
	}
	if answer == "" {
		_ = stream.UpdateCard(ctx, cardWithText("（没有生成可展示的回答，可能没有检索到相关内容。）"))
		return
	}

	_ = stream.UpdateCard(ctx, cardWithText("✍️ 正在整理回答…\n\n🔎 检索完成，正在生成回答：\n\n"+answer))
	b.remember(sessionKey, question, answer)
	go b.maybeCompress(context.Background(), sessionKey)
}

// answer 执行一次跨联系人问答：先 memory-search，再 analyze。
func (b *bot) answer(ctx context.Context, sessionKey, question string) (string, string) {
	// 取会话历史与前序解析出的实体
	history := b.historyOf(sessionKey)
	priorEntities := b.entitiesOf(sessionKey)
	hasPriorEntity := len(priorEntities) > 0

	convKey := "feishu:" + sessionKey
	// 1. memory-search 跨联系人检索（进度回调打印日志；无实体且上下文无实体则中止）
	data, err := memorySearch(ctx, b.cfg, question, convKey, hasPriorEntity,
		func(step, detail string) {
			log.Printf("[bot] %s 检索进度: %s - %s", sessionKey, step, detail)
		},
		func(names []string) {
			// 本轮解析出了实体，记录下来供后续追问沿用
			if len(names) > 0 {
				b.setEntities(sessionKey, names)
			}
		},
	)
	if err != nil {
		if err == errMissingEntity {
			return "", "请指定要查询的联系人/群名（例如“我和邓凯文最近聊了什么？”），或先在本会话指定一次实体对象。"
		}
		if err == errEntityNotFound {
			return "", "未找到你指定的联系人/群名，请确认姓名后重试。"
		}
		return "", "跨联系人检索失败，请稍后重试。\n\n" + err.Error()
	}

	// 若本轮结果里解析出了实体，也记录下来
	if data.hasResolvedEntity() {
		var names []string
		for _, e := range data.ResolvedEntities {
			if e.ContactKey != "" {
				names = append(names, e.DisplayName)
			}
		}
		if len(names) > 0 {
			b.setEntities(sessionKey, names)
		}
	}

	// 2. 把检索结果拼成上下文
	dataContext := buildDataContext(data)

	// 3. analyze 生成回答（带上历史 + 检索上下文）
	answer, err := analyzeQuestion(ctx, b.cfg, question, convKey, history, dataContext)
	if err != nil {
		return "", "生成回答失败，请稍后重试。\n\n" + err.Error()
	}
	return answer, ""
}

func (b *bot) historyOf(key string) []llmMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessionLocked(key)
	return append([]llmMessage(nil), s.history...)
}

// entitiesOf 返回会话最近解析出的实体展示名。
func (b *bot) entitiesOf(key string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.sessions[key]; s != nil {
		return append([]string(nil), s.entities...)
	}
	return nil
}

// setEntities 记录会话最近解析出的实体展示名。
func (b *bot) setEntities(key string, names []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessionLocked(key)
	s.entities = append([]string(nil), names...)
}

// remember 追加问答到会话并更新活跃时间。
func (b *bot) remember(key, question, answer string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessionLocked(key)
	s.history = append(s.history, llmMessage{Role: "user", Content: question})
	s.history = append(s.history, llmMessage{Role: "assistant", Content: answer})
	s.lastActive = time.Now()
	s.version++
	b.saveLocked()
}

// maybeCompress 若会话达到压缩阈值，则异步调用 LLM 做摘要压缩。
func (b *bot) maybeCompress(ctx context.Context, key string) {
	b.mu.Lock()
	s := b.sessions[key]
	if s == nil || !b.shouldCompressLocked(s) {
		b.mu.Unlock()
		return
	}
	// 记录触发压缩时的版本与历史快照，避免压缩期间新问答被覆盖
	triggerVersion := s.version
	msgs := make([]llmMessage, 0, len(s.history)+2)
	msgs = append(msgs, llmMessage{
		Role:    "system",
		Content: "你是一个会话摘要器。请把下面的用户和 AI 的历史问答压缩成一段简洁的中文摘要，保留关键人物、时间、事件、结论，供后续多轮追问使用。只输出摘要本身。",
	})
	msgs = append(msgs, s.history...)
	b.mu.Unlock()

	log.Printf("[bot] %s 会话上下文过长，开始异步压缩 (version=%d)", key, triggerVersion)
	cctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	b.compress(cctx, key, triggerVersion, msgs)
}

// shouldCompressLocked 判断是否达到压缩阈值，调用方须持有 b.mu。
func (b *bot) shouldCompressLocked(s *session) bool {
	if len(s.history) >= maxHistoryMsgs {
		return true
	}
	total := 0
	for _, m := range s.history {
		total += len(m.Content)
	}
	return total >= maxHistoryChars
}

// compress 压缩会话历史为一段摘要，仅保留最近的一问一答。
// 只有会话版本仍未变化时才写回，避免覆盖压缩期间新加入的问答。
func (b *bot) compress(ctx context.Context, key string, triggerVersion uint64, msgs []llmMessage) {
	summary, err := complete(ctx, b.cfg, msgs)
	if err != nil {
		log.Printf("[bot] %s 压缩失败: %v", key, err)
		return
	}
	if strings.TrimSpace(summary) == "" {
		log.Printf("[bot] %s 压缩结果为空，保留原历史", key)
		return
	}
	b.applyCompression(key, triggerVersion, summary)
}

// applyCompression 仅在会话版本未变化时把历史替换为摘要，避免覆盖新问答。
func (b *bot) applyCompression(key string, triggerVersion uint64, summary string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[key]
	if s == nil || s.version != triggerVersion {
		log.Printf("[bot] %s 压缩期间有新问答，放弃本次写回 (v=%d -> %d)", key, triggerVersion, s.version)
		return
	}
	// 保留最近一问一答，其余替换为摘要，保证后续追问衔接
	keep := 0
	n := len(s.history)
	if n >= 2 && s.history[n-1].Role == "assistant" && s.history[n-2].Role == "user" {
		keep = 2
	} else if n >= 1 && s.history[n-1].Role == "user" {
		keep = 1
	}
	recent := append([]llmMessage(nil), s.history[n-keep:]...)
	s.history = []llmMessage{{Role: "system", Content: "【上一段会话摘要】" + summary}}
	s.history = append(s.history, recent...)
	s.compressed = true
	b.saveLocked()
}

// saveLocked 把当前会话快照写入存储文件；调用方已持有 b.mu。
func (b *bot) saveLocked() {
	if b.store == nil {
		return
	}
	if err := b.store.Save(b.sessions); err != nil {
		log.Printf("[bot] 保存会话历史失败: %v", err)
	}
}

func (b *bot) safeSend(ctx context.Context, msg *types.NormalizedMessage, text string) {
	if msg == nil {
		return
	}
	ctxT, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _ = b.ch.Send(ctxT, &types.SendInput{
		ChatID:         msg.ChatID,
		ReplyMessageID: msg.MessageID,
		MsgType:        "text",
		Text:           text,
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

func sessionKeyFor(msg *types.NormalizedMessage) string {
	if msg.ChatType == "group" {
		return fmt.Sprintf("group:%s:%s", msg.ChatID, msg.UserID)
	}
	return fmt.Sprintf("p2p:%s", msg.UserID)
}

func cleanMention(content string) string {
	s := strings.ReplaceAll(content, "@_user_1", "")
	s = strings.ReplaceAll(s, "@_user_2", "")
	s = strings.ReplaceAll(s, "<at id=xxx>", "")
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "/ai")
	return strings.TrimSpace(s)
}

func cardWithText(text string) string {
	return fmt.Sprintf(`{"config":{"streaming_mode":true},"header":{"title":{"tag":"plain_text","content":"AI 回答"}},"elements":[{"tag":"markdown","content":%q}]}`, text)
}
