package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/channel"
	"github.com/larksuite/oapi-sdk-go/v3/channel/normalize"
	"github.com/larksuite/oapi-sdk-go/v3/channel/types"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const (
	maxHistoryMsgs  = 48            // 后端 Profile 预算之外的本地兜底条数
	maxHistoryChars = 120000        // 后端 Profile 预算之外的本地兜底字符数
	sessionIdleTTL  = 2 * time.Hour // 2 小时无新提问自动新开会话
)

// bot 持有飞书通道与配置，并维护每个用户的独立会话。
type bot struct {
	cfg         *Config
	client      *lark.Client
	ch          types.Channel
	store       *sessionStore
	pending     *pendingStore
	mu          sync.Mutex
	sessions    map[string]*session
	announce    *announcer
	chatNames   map[string]string            // chat_id -> 群名（管理端展示用，惰性填充）
	memberNames map[string]map[string]string // chat_id -> (user_id -> 姓名)
	nameCacheAt time.Time                    // 名字缓存刷新时间
}

// session 表示单个用户在单个群聊/单聊中的独立上下文。
type session struct {
	history    []llmMessage // 完整问答历史（user + assistant）
	createdAt  time.Time    // 本次会话/上下文的开始时间（清空后重新计时）
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
	// 该版本 SDK 会按 chat_id 把消息塞进同一个 per-chat pipeline 做合并/串行派发：
	// 若某个人的问题处理较慢（如检索超时），会阻塞同群其他人的消息。
	// 把批处理冲刷延时设为 0，消息不再批量合并、也不做整群串行，各自独立派发。
	safetyCfg := types.DefaultChannelConfig().Safety
	safetyCfg.Batch.DelayMs = 0
	safetyCfg.Batch.LongDelayMs = 0
	b.ch = channel.NewChannel(client, wsClient, types.WithSafetyConfig(safetyCfg))

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

	// 群公告 + 心跳：启动时为 bot 所在各群写入“已重启完成”，并周期性检测后台连接。
	// ANNOUNCE_CHAT_ID 可指定要维护的群（逗号分隔）；留空则自动发现 bot 所在全部群。
	b.announce = newAnnouncer(cfg, client)
	up := b.backendStatus(ctx)
	b.announce.startup(ctx, up)
	go b.announce.runHeartbeat(ctx)

	return b, nil
}

// backendStatus 探测一次 WeLink 后端可达性（用于群公告初始连接状态）。
func (b *bot) backendStatus(ctx context.Context) bool {
	return backendStatusUp(ctx, b.cfg)
}

// run 启动飞书长连接（阻塞直到退出）。
func (b *bot) run(ctx context.Context) error {
	log.Printf("[bot] 正在启动飞书长连接，app_id=%s", b.cfg.FeishuAppID)
	// 后台枚举飞书群并上报，与主生命周期 context 解耦
	go b.syncFeishuChats()
	return b.ch.Start(ctx)
}

// syncFeishuChats 枚举 bot 所在的所有飞书群（chat_id -> 群名），
// 上报给 WeLink 后端，供前端为每个飞书群配置白名单。
// 非阻塞：失败仅打日志，不影响主流程。
func (b *bot) syncFeishuChats() {
	if b.client == nil {
		return
	}
	// 等 WS 建立、tenant token 就绪后枚举并上报（任一步失败都重试几次）
	for attempt := 1; attempt <= 6; attempt++ {
		chats := b.enumerateChats()
		if len(chats) > 0 {
			if b.reportFeishuChats(chats) {
				return
			}
			log.Printf("[bot] 上报飞书群列表失败，稍后重试 (第%d次)", attempt)
		} else {
			log.Printf("[bot] 本次未枚举到飞书群，稍后重试 (第%d次)", attempt)
		}
		time.Sleep(8 * time.Second)
	}
	log.Printf("[bot] 多次尝试后仍未成功上报飞书群列表")
}

// enumerateChats 用 Chat.List 单页列出 bot 所在群（chat_id -> 群名），
// 每页独立超时，按 HasMore 分页。复用公告功能的稳定实现模式。
func (b *bot) enumerateChats() map[string]string {
	chats := map[string]string{}
	pageToken := ""
	for {
		req := larkim.NewListChatReqBuilder().PageSize(100).SortType("ByCreateTimeAsc")
		if pageToken != "" {
			req.PageToken(pageToken)
		}
		ctxT, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		resp, err := b.client.Im.V1.Chat.List(ctxT, req.Build())
		cancel()
		if err != nil {
			log.Printf("[bot] 枚举飞书群列表失败: %v", err)
			return chats
		}
		if !resp.Success() {
			log.Printf("[bot] 枚举飞书群列表失败: code=%d msg=%s", resp.Code, resp.Msg)
			return chats
		}
		for _, ch := range resp.Data.Items {
			if ch.ChatId == nil || *ch.ChatId == "" {
				continue
			}
			name := ""
			if ch.Name != nil {
				name = *ch.Name
			}
			chats[*ch.ChatId] = name
		}
		if resp.Data.HasMore == nil || !*resp.Data.HasMore || resp.Data.PageToken == nil || *resp.Data.PageToken == "" {
			break
		}
		pageToken = *resp.Data.PageToken
	}
	return chats
}

// reportFeishuChats 把枚举到的飞书群列表上报给 WeLink 后端。成功返回 true。
func (b *bot) reportFeishuChats(chats map[string]string) bool {
	ctxT, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]any{"chats": chats})
	req, err := http.NewRequestWithContext(ctxT, http.MethodPut, b.cfg.WeLinkBaseURL+"/api/preferences/feishu-bot-chats", bytes.NewReader(payload))
	if err != nil {
		log.Printf("[bot] 构造飞书群列表上报请求失败: %v", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	if b.cfg.WeLinkToken != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.WeLinkToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[bot] 上报飞书群列表失败: %v", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[bot] 上报飞书群列表失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
		return false
	}
	log.Printf("[bot] 已上报 %d 个飞书群到 WeLink 后端", len(chats))
	return true
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
		s = &session{history: []llmMessage{}, createdAt: time.Now(), lastActive: time.Now()}
		b.sessions[key] = s
	}
	// 超过 2 小时没新提问 → 丢弃旧上下文，新起一个会话
	if time.Since(s.lastActive) > sessionIdleTTL {
		s.history = []llmMessage{}
		s.createdAt = time.Now()
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

// chatIDFromSession 从 session key 中提取飞书群 chat_id。
// 群聊 session key 格式为 group:<chatID>:<senderID>；单聊没有群 chat_id。
func chatIDFromSession(sessionKey string) string {
	if !strings.HasPrefix(sessionKey, "group:") {
		return ""
	}
	rest := strings.TrimPrefix(sessionKey, "group:")
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) < 1 {
		return ""
	}
	return parts[0]
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

	// lastPct 记录上次渲染的百分比，保证进度条只增不减
	// （total 动态增长时 current/total 可能下降，这里在渲染层强制单调）。
	lastPct := -1
	var progressNotes []string
	seenProgressNotes := make(map[string]bool)
	streamBuffer := newAnswerStreamBuffer(20)
	answerCurrent, answerTotal := 1, 1
	answer, runMeta, errMsg := b.answer(ctx, sessionKey, chatIDFromSession(sessionKey), question, func(stage, step string, current, total int, detail string) {
		title := "AI 回答"
		body := "检索完成，正在生成回答…"
		if stage == "search" {
			title = "🔎 正在检索"
			body = "正在跨联系人检索相关聊天记录…"
			if step == "decompose_result" {
				title = "🧠 问题分解"
				body = detail
			} else if step == "query_expansion_result" {
				title = "🧩 查询扩展"
				body = detail
			}
		} else if stage == "answer" {
			title = "✍️ 正在整理回答"
			body = "检索完成，正在生成回答…"
			if total > 0 {
				answerCurrent, answerTotal = current, total
			}
		}
		pct := 0
		if total > 0 {
			pct = current * 100 / total
		}
		showResult := step == "decompose_result" || step == "query_expansion_result"
		if showResult && strings.TrimSpace(detail) != "" {
			note := "> " + strings.TrimSpace(detail)
			if !seenProgressNotes[note] {
				seenProgressNotes[note] = true
				progressNotes = append(progressNotes, note)
			}
		}
		if pct < lastPct || (pct == lastPct && !showResult) {
			return // 百分比下降，忽略本次更新，保证进度条只增不减
		}
		lastPct = pct
		pb := progressBar(current, total)
		_ = b.patchCard(ctx, messageID, cardJSON(title, progressCardBody(progressNotes, body), pb))
	}, func(delta string) {
		partial, ready := streamBuffer.Append(delta)
		if !ready {
			return
		}
		body := "检索完成，正在生成回答…\n\n" + partial
		_ = b.patchCard(ctx, messageID, cardJSON(
			"✍️ 正在整理回答",
			progressCardBody(progressNotes, body),
			progressBar(answerCurrent, answerTotal),
		))
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
	_ = b.patchCard(ctx, messageID, cardJSONFinal("✅ 回答完成", b.contextMetaLine(sessionKey, runMeta), answer))
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
// 返回回答文本、本次 LLM token 用量（可能为 nil）与错误信息。
func (b *bot) answer(ctx context.Context, sessionKey, chatID, question string, onProgress func(stage, step string, current, total int, detail string), onAnswerDelta func(string)) (string, *answerRunMeta, string) {
	// 取会话历史与前序解析出的实体
	history := b.historyOf(sessionKey)
	priorEntities := b.entitiesOf(sessionKey)
	hasPriorEntity := len(priorEntities) > 0

	convKey := "feishu:" + sessionKey
	// 进度跟踪器：把后端步骤转成单调递增的 (current, total)。
	tracker := newProgressTracker()

	// 1. memory-search 跨联系人检索（进度回调打印日志 + 驱动卡片进度；无实体则中止）
	data, err := memorySearch(ctx, b.cfg, question, convKey, chatID, hasPriorEntity,
		func(step, detail string) {
			log.Printf("[bot] %s 检索进度: %s - %s", sessionKey, step, detail)
			if onProgress != nil {
				cur, total := tracker.Observe(step, detail)
				onProgress("search", step, cur, total, detail)
			}
		},
		func(names []string) {
			// 本轮解析出了实体，记录下来供后续追问沿用
			if len(names) > 0 {
				b.setEntities(sessionKey, names)
			}
			if onProgress != nil {
				cur, total := tracker.Observe("resolve_entities", "")
				onProgress("search", "resolve_entities", cur, total, "")
			}
		},
	)
	if err != nil {
		if err == errMissingEntity {
			return "", nil, "请指定要查询的联系人/群名（例如“我和邓凯文最近聊了什么？”），或先在本会话指定一次实体对象。"
		}
		if err == errEntityNotFound {
			return "", nil, "未找到你指定的联系人/群名，请确认姓名后重试。"
		}
		return "", nil, "跨联系人检索失败，请稍后重试。\n\n" + err.Error()
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
		cur, total := tracker.Observe("answer", "")
		onProgress("answer", "answer", cur, total, "")
	}

	// 3. analyze 生成回答（带上历史 + 检索上下文）。
	// 用 memory-search 还原后的原名提问，避免最终回答仍在定位外号（如“土鲫鱼”）。
	answerQuery := question
	if data.NormalizedQuery != "" {
		answerQuery = data.NormalizedQuery
	}
	answer, usage, err := analyzeQuestion(ctx, b.cfg, chatID, answerQuery, convKey, history, dataContext, onAnswerDelta)
	if err != nil {
		return "", &answerRunMeta{Usage: usage}, "生成回答失败，请稍后重试。\n\n" + err.Error()
	}
	return answer, &answerRunMeta{
		Usage:           usage,
		Models:          data.LLMModels,
		Decomposition:   data.Decomposition,
		ExpandedQueries: data.ExpandedQueries,
	}, ""
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
	// 在群聊中发起上下文压缩时，发一条提示消息，让群成员知道旧上下文正在被自动摘要。
	if chatID := chatIDFromSession(key); chatID != "" {
		go b.sendCompressNotice(context.Background(), chatID)
	}
	cctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	b.compress(cctx, key, triggerVersion, msgs)
}

// sendCompressNotice 在指定飞书群发送一条上下文压缩提示。
func (b *bot) sendCompressNotice(ctx context.Context, chatID string) {
	ctxT, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, _ = b.ch.Send(ctxT, &types.SendInput{
		ChatID:  chatID,
		MsgType: "text",
		Text:    "当前会话上下文较长，正在自动压缩为摘要以延续后续追问。",
	})
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

func progressCardBody(notes []string, status string) string {
	if len(notes) == 0 {
		return status
	}
	if status == "" {
		return strings.Join(notes, "\n")
	}
	return strings.Join(notes, "\n") + "\n\n" + status
}

// cardJSONFinal 生成回答完成卡片，meta 为非空时在正文上方渲染一行元信息。
func cardJSONFinal(title, meta, text string) string {
	if meta != "" {
		text = meta + "\n\n" + text
	}
	return cardJSON(title, text, "")
}

// contextMetaLine 生成回答卡片上的一行小字灰色元信息：当前会话开始时间。
// 用 Markdown 引用块渲染，飞书会把该行弱化为偏小偏灰的注释；不展示 token 统计。
func (b *bot) contextMetaLine(key string, runMeta *answerRunMeta) string {
	b.mu.Lock()
	s := b.sessions[key]
	var createdAt time.Time
	if s != nil {
		createdAt = s.createdAt
	}
	b.mu.Unlock()

	var lines []string
	if !createdAt.IsZero() {
		lines = append(lines, formatContextMetaStart(createdAt, currentCodeRevision()))
	}
	if runMeta != nil {
		if detail := formatAnswerRunMeta(*runMeta); detail != "" {
			lines = append(lines, detail)
		}
	}
	return strings.Join(lines, "\n")
}

func formatAnswerRunMeta(meta answerRunMeta) string {
	var lines []string
	var modelParts []string
	if meta.Models.QueryDecomposition != "" {
		modelParts = append(modelParts, "问题分解 `"+meta.Models.QueryDecomposition+"`")
	}
	if meta.Models.QueryExpansion != "" {
		modelParts = append(modelParts, "查询扩展 `"+meta.Models.QueryExpansion+"`")
	}
	if meta.Models.FinalAnswer != "" {
		modelParts = append(modelParts, "最终回答 `"+meta.Models.FinalAnswer+"`")
	}
	if len(modelParts) > 0 {
		lines = append(lines, "> 模型："+strings.Join(modelParts, " · "))
	}
	if d := meta.Decomposition; d != nil {
		var parts []string
		if len(d.Entities) > 0 {
			parts = append(parts, "实体 "+strings.Join(d.Entities, "、"))
		}
		if len(d.Concepts) > 0 {
			parts = append(parts, "概念 "+strings.Join(d.Concepts, "、"))
		}
		if d.TimeFrom != "" || d.TimeTo != "" {
			parts = append(parts, "时间 "+d.TimeFrom+" ~ "+d.TimeTo)
		}
		if len(parts) > 0 {
			lines = append(lines, "> 问题分解："+strings.Join(parts, "；"))
		}
	}
	if len(meta.ExpandedQueries) > 0 {
		lines = append(lines, "> 查询扩展："+strings.Join(meta.ExpandedQueries, "；"))
	}
	return strings.Join(lines, "\n")
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

// progressBar 返回一个固定格数的等宽进度条行。
// 使用 inline code 触发飞书卡片的等宽字体，避免代码块显示行号。
// 实心方块与空心方块使用同一组几何字形，移动端的字形底部可保持对齐。
const maxProgressCells = 16

func progressBar(current, total int) string {
	if current < 0 {
		current = 0
	}
	if current > total {
		current = total
	}
	pct := 0
	if total > 0 {
		pct = current * 100 / total
	}
	// 固定显示 maxProgressCells 格（不随 N/M 学习而变化），
	// 按百分比计算涂色格数，保证进度比例不变。
	cells := maxProgressCells
	filled := (cells * pct) / 100
	if pct > 0 && filled == 0 {
		filled = 1
	}
	empty := cells - filled
	if empty < 0 {
		empty = 0
	}
	return fmt.Sprintf("`%3d%% %s%s`", pct, strings.Repeat("■", filled), strings.Repeat("□", empty))
}
