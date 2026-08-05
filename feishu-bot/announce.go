package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
)

// docx 块类型枚举（见飞书 docx/v1 BlockType）
const (
	docxBlockHeading1 = 3
	docxBlockHeading2 = 4
	docxBlockText     = 2
	docxBlockBullet   = 12
	docxBlockDivider  = 22
)

// announcer 维护目标群公告：启动时写入“已重启完成”，心跳检测后台连接状态。
type announcer struct {
	cfg    *Config
	client *lark.Client
	mu     sync.Mutex

	// 上次断连公告时间，用于 1 小时限频
	lastDisconnectAnnounce time.Time
	// 当前连接状态（记录在公告里的状态），避免重复写
	connected bool
	// 是否已在启动时写过公告
	started bool
}

func newAnnouncer(cfg *Config, client *lark.Client) *announcer {
	return &announcer{cfg: cfg, client: client, connected: true}
}

// startup 开机时更新公告为“已重启完成”，并记录启动时间与初始连接状态。
func (a *announcer) startup(ctx context.Context, connected bool) {
	if a == nil || a.cfg == nil || a.cfg.AnnounceChatID == "" || a.client == nil {
		return
	}
	a.mu.Lock()
	a.started = true
	a.connected = connected
	a.mu.Unlock()
	if err := a.update(ctx, connected); err != nil {
		log.Printf("[announce] 启动更新公告失败: %v", err)
	} else {
		log.Printf("[announce] 群公告已更新（已重启完成）")
	}
}

// runHeartbeat 周期检测后台连接状态，按状态变化更新公告。
func (a *announcer) runHeartbeat(ctx context.Context) {
	if a == nil || a.cfg == nil || a.cfg.AnnounceChatID == "" || a.cfg.HeartbeatInterval <= 0 {
		return
	}
	ticker := time.NewTicker(a.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.tick(ctx)
		}
	}
}

// tick 探测一次后端状态并按需更新公告。
func (a *announcer) tick(ctx context.Context) {
	up := backendStatusUp(ctx, a.cfg)
	a.mu.Lock()
	wasConnected := a.connected
	a.connected = up
	shouldSend := false
	if up {
		// 恢复连接：即刻更新
		shouldSend = !wasConnected || !a.started
	} else {
		if wasConnected || !a.started {
			// 首次断连：立即发
			shouldSend = true
		} else {
			// 已断连状态：超 1 小时才再次刷新时间戳
			shouldSend = time.Since(a.lastDisconnectAnnounce) >= a.cfg.DisconnectCooldown
		}
	}
	a.mu.Unlock()

	if shouldSend {
		if err := a.update(ctx, up); err != nil {
			log.Printf("[announce] 更新公告失败: %v", err)
			return
		}
		if !up {
			a.mu.Lock()
			a.lastDisconnectAnnounce = time.Now()
			a.mu.Unlock()
		}
	}
}

// update 以当前连接状态重写群公告（先清空再重建）。
func (a *announcer) update(ctx context.Context, connected bool) error {
	if a.cfg.AnnounceChatID == "" || a.client == nil {
		return nil
	}
	blocks := buildAnnouncementBlocks(connected, time.Now())
	if err := a.replaceAnnouncement(ctx, blocks); err != nil {
		return err
	}
	return nil
}

// replaceAnnouncement 先删除根块下所有子块，再创建新的公告块。
func (a *announcer) replaceAnnouncement(ctx context.Context, blocks []*larkdocx.Block) error {
	chatID := a.cfg.AnnounceChatID
	rootID := chatID // 页面根块 block_id 即为 chat_id

	// 1. 列出根块的子块数量
	listResp, err := a.client.Docx.V1.ChatAnnouncementBlock.List(ctx,
		larkdocx.NewListChatAnnouncementBlockReqBuilder().
			ChatId(chatID).RevisionId(-1).PageSize(100).Build())
	if err != nil {
		return fmt.Errorf("list blocks: %w", err)
	}
	if !listResp.Success() {
		return fmt.Errorf("list blocks: code=%d msg=%s", listResp.Code, listResp.Msg)
	}
	children := 0
	for _, blk := range listResp.Data.Items {
		if blk.Children != nil {
			children = len(blk.Children)
		}
	}

	// 2. 删除根块下全部子块
	if children > 0 {
		delResp, err := a.client.Docx.V1.ChatAnnouncementBlockChildren.BatchDelete(ctx,
			larkdocx.NewBatchDeleteChatAnnouncementBlockChildrenReqBuilder().
				ChatId(chatID).BlockId(rootID).RevisionId(-1).
				Body(larkdocx.NewBatchDeleteChatAnnouncementBlockChildrenReqBodyBuilder().
					StartIndex(0).EndIndex(children).Build()).
				Build())
		if err != nil {
			return fmt.Errorf("delete blocks: %w", err)
		}
		if !delResp.Success() {
			return fmt.Errorf("delete blocks: code=%d msg=%s", delResp.Code, delResp.Msg)
		}
	}

	// 3. 创建新公告块
	createResp, err := a.client.Docx.V1.ChatAnnouncementBlockChildren.Create(ctx,
		larkdocx.NewCreateChatAnnouncementBlockChildrenReqBuilder().
			ChatId(chatID).BlockId(rootID).RevisionId(-1).
			Body(larkdocx.NewCreateChatAnnouncementBlockChildrenReqBodyBuilder().
				Children(blocks).Index(0).Build()).
			Build())
	if err != nil {
		return fmt.Errorf("create blocks: %w", err)
	}
	if !createResp.Success() {
		return fmt.Errorf("create blocks: code=%d msg=%s", createResp.Code, createResp.Msg)
	}
	return nil
}

// buildAnnouncementBlocks 生成 3 段群公告的 docx 块：
// 1. 使用方法（@群史官 提问 + 冷却说明）
// 2. 上次后台网关启动时间
// 3. AI 后台当前连接状态
func buildAnnouncementBlocks(connected bool, now time.Time) []*larkdocx.Block {
	blocks := []*larkdocx.Block{
		headingBlock(1, "🤖 群史官 · AI 问答"),
		dividerBlock(),
		headingBlock(2, "📖 使用方法"),
		bulletBlock("在群里 @群史官 并发送你的问题，即可向 AI 提问。"),
		bulletBlock("提问需指定联系人/群名，例如：“我和邓凯文最近聊了什么？”"),
		bulletBlock("同一个人的问题回答中会有冷却：上一个问题处理完之前，再次提问会被拒绝，请稍候重试。"),
		dividerBlock(),
		headingBlock(2, "🕘 上次后台网关启动时间"),
		textBlock(now.Format("2006-01-02 15:04:05")),
		dividerBlock(),
		headingBlock(2, "🔌 AI 后台连接状态"),
	}

	if connected {
		blocks = append(blocks, textBlock("✅ 已连接"))
	} else {
		blocks = append(blocks, textBlock("❌ 连接已断开（"+now.Format("01-02 15:04")+"）"))
	}
	return blocks
}

// ---- 块构建辅助 ----

func textBlock(s string) *larkdocx.Block {
	return larkdocx.NewBlockBuilder().
		BlockType(docxBlockText).
		Text(larkdocx.NewTextBuilder().Elements(textElements(s)).Build()).
		Build()
}

func headingBlock(level int, s string) *larkdocx.Block {
	text := larkdocx.NewTextBuilder().Elements(textElements(s)).Build()
	if level >= 2 {
		return larkdocx.NewBlockBuilder().
			BlockType(docxBlockHeading2).
			Heading2(text).
			Build()
	}
	return larkdocx.NewBlockBuilder().
		BlockType(docxBlockHeading1).
		Heading1(text).
		Build()
}

func bulletBlock(s string) *larkdocx.Block {
	return larkdocx.NewBlockBuilder().
		BlockType(docxBlockBullet).
		Bullet(larkdocx.NewTextBuilder().Elements(textElements(s)).Build()).
		Build()
}

func dividerBlock() *larkdocx.Block {
	return larkdocx.NewBlockBuilder().
		BlockType(docxBlockDivider).
		Divider(larkdocx.NewDividerBuilder().Build()).
		Build()
}

func textElements(s string) []*larkdocx.TextElement {
	return []*larkdocx.TextElement{
		larkdocx.NewTextElementBuilder().TextRun(
			larkdocx.NewTextRunBuilder().Content(s).Build(),
		).Build(),
	}
}
