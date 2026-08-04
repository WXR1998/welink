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
	"github.com/larksuite/oapi-sdk-go/v3/channel/normalize"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const (
	maxHistoryMsgs = 12  // 上下文达到该消息数时触发压缩
	maxHistoryChars = 24000 // 上下文文本达到该字符数时触发压缩
	sessionIdleTTL = 2 * time.Hour // 2 小时无新提问自动新开会话
)

// bot 持有飞书通道与配置，并维护每个用户的独立会话。
type bot struct {
	cfg        *Config
	client     *lark.Client
	ch         types.Channel
	store      *sessionStore
	pending    *pendingStore
	mu         sync.Mutex
	sessions   map[string]*session
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
		client:   client,
		store:    st,
		pending:  newPendingStore(cfg.SessionStorePath),
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
	b.recoverPending(ctx)
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

// processCard 用卡片流式回答，回复时引用用户的原始提问。
// 先发送初始卡片并登记 pending（便于崩溃后回滚），回答完成后移除登记。
func (b *bot) processCard(ctx context.Context, msg *types.NormalizedMessage, question, sessionKey string) {
	res, err := b.ch.Send(ctx, &types.SendInput{
		ChatID:         msg.ChatID,
		ReplyMessageID: msg.MessageID,
		Card:           cardWithText("⏳ 正在分析你的问题…"),
	})
	if err != nil {
		log.Printf("[bot] 创建卡片消息失败: %v", err)
		return
	}
	messageID := res.MessageID
	if messageID == "" {
		log.Printf("[bot] 发送卡片未返回 message_id，无法跟踪进行中状态")
		return
	}

	b.trackPending(messageID, msg.ChatID, sessionKey, question)

	// 初始：检索阶段进度 1/5
	_ = b.patchCard(ctx, messageID, cardJSON("🔎 正在检索", "正在跨联系人检索相关聊天记录…", progressBar(1, 5)))

	answer, errMsg := b.answer(ctx, sessionKey, question, func(stage string, current, total int) {
		title := "AI 回答"
		body := "检索完成，正在生成回答…"
		if stage == "search" {
			title = "🔎 正在检索"
			body = "正在跨联系人检索相关聊天记录…"
		} else if stage == "answer" {
			title = "✍️ 正在整理回答"
			body = "检索完成，正在生成回答…"
		}
		pb := progressBar(current, total)
		_ = b.patchCard(ctx, messageID, cardJSON(title, body, pb))
	})
	log.Printf("[bot] %s 回答完成: messageID=%s question=%q answerLen=%d errMsg=%q", sessionKey, messageID, question, len(answer), errMsg)

	if errMsg != "" {
		b.untrackPending(messageID)
		_ = b.patchCard(ctx, messageID, cardWithText("❌ "+errMsg))
		return
	}
	if answer == "" {
		b.untrackPending(messageID)
		_ = b.patchCard(ctx, messageID, cardWithText("（没有生成可展示的回答，可能没有检索到相关内容。）"))
		return
	}

	b.untrackPending(messageID)
	_ = b.patchCard(ctx, messageID, cardJSON("✅ 回答完成", answer, ""))
	b.remember(sessionKey, question, answer)
	go b.maybeCompress(context.Background(), sessionKey)

	// 提醒提问人：回答已完成（引用卡片并 @ 提问人）
	b.notifyAnswerDone(ctx, msg, messageID)
}

// notifyAnswerDone 在回答完成后发送一条引用卡片、@提问人的提醒消息。
func (b *bot) notifyAnswerDone(ctx context.Context, msg *types.NormalizedMessage, cardMessageID string) {
	if msg == nil || msg.ChatType != "group" {
		return
	}
	mention := types.Mention{UserID: msg.UserID, Name: ""}
	prefix := normalize.ComposeMentionsTextPrefix([]types.Mention{mention})
	ctxT, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _ = b.ch.Send(ctxT, &types.SendInput{
		ChatID:         msg.ChatID,
		ReplyMessageID: cardMessageID,
		MsgType:        "text",
		Text:           prefix + "你的问题已回答完毕，请查看上方卡片。",
	})
}

// patchCard 用 message_id 更新一张已发送的卡片。
func (b *bot) patchCard(ctx context.Context, messageID, card string) error {
	if messageID == "" || b.client == nil {
		return nil
	}
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().Content(card).Build()).
		Build()
	ctxT, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := b.client.Im.V1.Message.Patch(ctxT, req)
	if err != nil {
		log.Printf("[bot] 更新卡片 %s 失败: %v", messageID, err)
		return err
	}
	if !resp.Success() {
		log.Printf("[bot] 更新卡片 %s 失败: code=%d msg=%s", messageID, resp.Code, resp.Msg)
		return fmt.Errorf("patch card failed: code=%d", resp.Code)
	}
	return nil
}

// trackPending 持久化一条进行中的卡片记录。
func (b *bot) trackPending(messageID, chatID, sessionKey, question string) {
	if b.pending == nil {
		return
	}
	items, err := b.pending.Load()
	if err != nil {
		log.Printf("[bot] 读取进行中记录失败: %v", err)
		items = map[string]pendingItem{}
	}
	items[messageID] = pendingItem{
		MessageID:  messageID,
		ChatID:     chatID,
		SessionKey: sessionKey,
		Question:   question,
		CreatedAt:  time.Now(),
	}
	if err := b.pending.Save(items); err != nil {
		log.Printf("[bot] 保存进行中记录失败: %v", err)
	}
}

// untrackPending 从进行中记录中移除一条已完成/出错的卡片。
func (b *bot) untrackPending(messageID string) {
	if b.pending == nil || messageID == "" {
		return
	}
	items, err := b.pending.Load()
	if err != nil {
		log.Printf("[bot] 读取进行中记录失败: %v", err)
		return
	}
	if _, ok := items[messageID]; !ok {
		return
	}
	delete(items, messageID)
	if err := b.pending.Save(items); err != nil {
		log.Printf("[bot] 保存进行中记录失败: %v", err)
	}
}

// recoverPending 在启动时处理上次未完成的回答：把群里对应的卡片改成
// "连接已断开，重新尝试"，并从会话历史中丢弃这些对话。
func (b *bot) recoverPending(ctx context.Context) {
	if b.pending == nil {
		return
	}
	items, err := b.pending.Load()
	if err != nil {
		log.Printf("[bot] 读取进行中记录失败: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}
	log.Printf("[bot] 检测到 %d 条上次未完成的回答，正在回滚...", len(items))
	for _, it := range items {
		b.dropSession(it.SessionKey)
		_ = b.patchCard(ctx, it.MessageID, cardWithText("⚠️ 连接已断开，重新尝试"))
	}
	if err := b.pending.Save(map[string]pendingItem{}); err != nil {
		log.Printf("[bot] 清空进行中记录失败: %v", err)
	}
}

// dropSession 丢弃指定会话的历史与进行中状态（busy 锁、已记录的实体）。
func (b *bot) dropSession(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.sessions[key]; s != nil {
		s.history = []llmMessage{}
		s.compressed = false
		s.busy = false
		s.entities = nil
		b.saveLocked()
	}
}

// answer 执行一次跨联系人问答：先 memory-search，再 analyze。
func (b *bot) answer(ctx context.Context, sessionKey, question string, onProgress func(stage string, current, total int)) (string, string) {
	// 取会话历史与前序解析出的实体
	history := b.historyOf(sessionKey)
	priorEntities := b.entitiesOf(sessionKey)
	hasPriorEntity := len(priorEntities) > 0

	convKey := "feishu:" + sessionKey
	// 1. memory-search 跨联系人检索（进度回调打印日志 + 驱动卡片进度；无实体则中止）
	data, err := memorySearch(ctx, b.cfg, question, convKey, hasPriorEntity,
		func(step, detail string) {
			log.Printf("[bot] %s 检索进度: %s - %s", sessionKey, step, detail)
			if onProgress != nil {
				// 检索阶段按步骤推进 1->4 格
				var cur int
				switch step {
				case "resolve_entities":
					cur = 2
				case "vector_search", "vecmsg_search", "bm25_search":
					cur = 3
				case "enhanced", "search_facts", "extract_sources":
					cur = 4
				default:
					cur = 1
				}
				onProgress("search", cur, 5)
			}
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

	// 2b. 检索完成，进入生成回答阶段
	if onProgress != nil {
		onProgress("answer", 5, 5)
	}

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
	return cardJSON("AI 回答", text, "")
}

// cardJSON 生成飞书卡片 JSON 2.0。body.elements 里的 markdown 组件会正确渲染
// # 标题、> 引用等标准语法（需显式声明 schema:2.0）。
// progress 留空则只渲染 markdown；非空则在其上下加一行 emoji 进度条。
func cardJSON(title, text, progress string) string {
	var elem string
	if progress != "" {
		elem = fmt.Sprintf(`{"tag":"markdown","content":%q},{"tag":"markdown","content":%q}`,
			progress, text)
	} else {
		elem = fmt.Sprintf(`{"tag":"markdown","content":%q}`, text)
	}
	return fmt.Sprintf(`{"schema":"2.0","config":{"streaming_mode":true},"header":{"title":{"tag":"plain_text","content":%q}},"body":{"elements":[%s]}}`, title, elem)
}

// progressBar 返回一个 n/5 格的 emoji 进度条行。
func progressBar(current, total int) string {
	if current < 0 {
		current = 0
	}
	if current > total {
		current = total
	}
	filled := strings.Repeat("🟩", current)
	empty := strings.Repeat("⬜", total-current)
	pct := 0
	if total > 0 {
		pct = current * 100 / total
	}
	return fmt.Sprintf("**%d%%** %s%s", pct, filled, empty)
}
