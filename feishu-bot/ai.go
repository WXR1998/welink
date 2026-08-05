package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// llmMessage 与后端 LLMMessage 对应。
type llmMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// memFact 与后端 MemFact 对应。
type memFact struct {
	ID         int    `json:"id"`
	ContactKey string `json:"contact_key"`
	Fact       string `json:"fact"`
	Pinned     bool   `json:"pinned"`
	SourceName string `json:"source_name,omitempty"`
}

// sourceMessage 与后端 SourceMessage 对应。
type sourceMessage struct {
	Seq      int    `json:"seq"`
	Datetime string `json:"datetime"`
	Sender   string `json:"sender"`
	Content  string `json:"content"`
}

// factSource 与后端 FactSource 对应。
type factSource struct {
	Fact       memFact         `json:"fact"`
	Messages   []sourceMessage `json:"messages"`
	SourceName string          `json:"source_name"`
}

// vecMessageHit 与后端 VecMessageHit 对应。
type vecMessageHit struct {
	ContactKey string  `json:"contact_key"`
	Seq        int     `json:"seq"`
	Datetime   string  `json:"datetime"`
	Sender     string  `json:"sender"`
	Content    string  `json:"content"`
	Similarity float32 `json:"similarity"`
	SourceName string  `json:"source_name,omitempty"`
}

// rawExcerpt 与后端 RawExcerpt 对应。
type rawExcerpt struct {
	SourceName string `json:"source_name"`
	Datetime   string `json:"datetime"`
	Sender     string `json:"sender"`
	Content    string `json:"content"`
	Seq        int    `json:"seq"`
}

// resolvedEntity 与后端 ResolvedEntity 对应。
type resolvedEntity struct {
	Name        string `json:"name"`
	ContactKey  string `json:"contact_key"`
	DisplayName string `json:"display_name"`
	IsGroup     bool   `json:"is_group"`
}

// memorySearchData 是 memory-search result 中用于构建上下文的关键字段。
type pinnedContactAlias struct {
	ContactKey  string   `json:"contact_key"`
	DisplayName string   `json:"display_name"`
	Aliases     []string `json:"aliases"`
}

type memorySearchData struct {
	Facts                []memFact            `json:"facts"`
	Sources              []factSource         `json:"sources"`
	PinnedFacts          []memFact            `json:"pinned_facts"`
	PinnedContactAliases []pinnedContactAlias `json:"pinned_contact_aliases,omitempty"`
	VecMessages          []vecMessageHit      `json:"vec_messages"`
	RawHits              []rawExcerpt         `json:"raw_hits"`
	ResolvedEntities     []resolvedEntity     `json:"resolved_entities"`
	Decomposition        *struct {
		NeedsMemory bool `json:"needs_memory"`
	} `json:"decomposition"`
}

// hasResolvedEntity 返回是否解析出至少一个可检索实体（联系人/群）。
func (d *memorySearchData) hasResolvedEntity() bool {
	if d == nil {
		return false
	}
	for _, e := range d.ResolvedEntities {
		if e.ContactKey != "" {
			return true
		}
	}
	return false
}

// memorySearchRequest 对应 POST /api/ai/memory-search。
type memorySearchRequest struct {
	Query           string `json:"query"`
	ProfileID       string `json:"profile_id"`
	ConversationKey string `json:"conversation_key"`
	Model           string `json:"model,omitempty"`
	ChatID          string `json:"chat_id,omitempty"` // 飞书群 chat_id，用于后端白名单过滤
}

// analyzeRequest 对应 POST /api/ai/analyze（cross-contact 模式）。
type analyzeRequest struct {
	Username        string       `json:"username"`
	IsGroup         bool         `json:"is_group"`
	Messages        []llmMessage `json:"messages"`
	ProfileID       string       `json:"profile_id"`
	SkipMemory      bool         `json:"skip_memory"`
	Query           string       `json:"query"`
	ConversationKey string       `json:"conversation_key"`
	Model           string       `json:"model,omitempty"`
	ChatID          string       `json:"chat_id,omitempty"` // 飞书群 chat_id，用于后端白名单过滤
}

// analyzeUsage 携带本次 analyze 的 LLM token 用量（机器人侧镜像）。
type analyzeUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	CachedTokens int `json:"cached_tokens,omitempty"`
}

// analyzeChunk 解析后端 /api/ai/analyze 的 SSE data 帧。
type analyzeChunk struct {
	Delta string        `json:"delta,omitempty"`
	Done  bool          `json:"done,omitempty"`
	Error string        `json:"error,omitempty"`
	Usage *analyzeUsage `json:"usage,omitempty"`
}

// complete 调用 POST /api/ai/complete，用非流式补全做上下文压缩摘要。
func complete(ctx context.Context, cfg *Config, msgs []llmMessage) (string, error) {
	payload, _ := json.Marshal(map[string]any{"messages": msgs, "model": cfg.LLMModel})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.WeLinkBaseURL+"/api/ai/complete", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.WeLinkToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.WeLinkToken)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("/api/ai/complete 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Content string `json:"content"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", fmt.Errorf("%s", out.Error)
	}
	return out.Content, nil
}

// doSSE 对 url 发起 POST，回调收到的每行 data 原始字节。
func doSSE(ctx context.Context, cfg *Config, url string, payload []byte, onData func([]byte) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.WeLinkBaseURL+url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.WeLinkToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.WeLinkToken)
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s 返回 %d: %s", url, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if dataStr == "" {
			continue
		}
		if err := onData([]byte(dataStr)); err != nil {
			return err
		}
	}
}

// memorySearchTimeout 是跨联系人检索单次等待上限。后端对无实体的全局问题会
// 遍历大量 contact_key，检索可能很久；避免网关长期占用会话，超时即返回可读错误。
const memorySearchTimeout = 3 * time.Minute

// memorySearch 调用 POST /api/ai/memory-search，返回检索结果与过程。
// errMissingEntity 是网关在“未指定实体且上下文无实体”时主动中止检索的哨兵错误。
var errMissingEntity = fmt.Errorf("问题里没有指定联系人/群，且本会话上下文也没有实体对象")

// errEntityNotFound 是网关在“指定了实体名但后端未解析到 contact_key”时中止检索的哨兵错误。
var errEntityNotFound = fmt.Errorf("问题中指定的联系人/群名未在数据中找到")

// memorySearch 调用 POST /api/ai/memory-search，返回检索结果与过程。
// hasPriorEntity 表示会话历史里是否已有明确实体（追问时可放行无实体问题）。
// onResolveEntities 在收到 resolve_entities 进度时回调，可在无实体时主动中止。
func memorySearch(ctx context.Context, cfg *Config, query, convKey, chatID string, hasPriorEntity bool, cb func(step, detail string), onResolveEntities func(names []string)) (*memorySearchData, error) {
	payload, _ := json.Marshal(memorySearchRequest{
		Query:           query,
		ProfileID:       cfg.DefaultProfileID,
		ConversationKey: convKey,
		Model:           cfg.LLMModel,
		ChatID:          chatID,
	})

	var result *memorySearchData
	lastDetail := ""
	ctx2, cancel := context.WithTimeout(ctx, memorySearchTimeout)
	defer cancel()

	err := doSSE(ctx2, cfg, "/api/ai/memory-search", payload, func(data []byte) error {
		var evt struct {
			Type   string          `json:"type"`
			Step   string          `json:"step"`
			Detail string          `json:"detail"`
			Data   json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(data, &evt); err != nil {
			return nil
		}
		if evt.Type == "progress" {
			if evt.Detail != "" {
				lastDetail = evt.Detail
			}
			if cb != nil {
				cb(evt.Step, evt.Detail)
			}
			if evt.Step == "resolve_entities" {
				if strings.Contains(evt.Detail, "实体解析结果:") {
					// 第二条进度：后端已尝试解析实体，报告命中与否
					if !hasPriorEntity && strings.Contains(evt.Detail, "未命中") {
						return errEntityNotFound
					}
					return nil
				}
				// 第一条进度：解析实体名（无实体且无前序实体则中止）
				if !hasPriorEntity {
					names := parseEntityNamesFromDetail(evt.Detail)
					if onResolveEntities != nil {
						onResolveEntities(names)
					}
					if len(names) == 0 {
						return errMissingEntity
					}
				}
			}
			return nil
		}
		if evt.Type == "result" {
			var d memorySearchData
			if err := json.Unmarshal(evt.Data, &d); err != nil {
				return fmt.Errorf("解析 memory-search 结果失败: %w", err)
			}
			result = &d
		}
		return nil
	})
	if err != nil {
		if err == context.DeadlineExceeded {
			return nil, fmt.Errorf("跨联系人检索超过 %s 仍未完成，建议改成更具体的人名/事件问题再试", memorySearchTimeout)
		}
		return nil, err
	}
	if result == nil {
		if lastDetail != "" {
			return nil, fmt.Errorf("跨联系人检索未返回最终结果（最后进度：%s），建议改成更具体的问题再试", lastDetail)
		}
		return nil, fmt.Errorf("跨联系人检索未返回最终结果，建议改成更具体的问题再试")
	}
	return result, nil
}

// parseEntityNamesFromDetail 从 “解析实体名: A、B” 的进度详情里提取实体名。
func parseEntityNamesFromDetail(detail string) []string {
	idx := strings.Index(detail, "解析实体名:")
	if idx < 0 {
		return nil
	}
	rest := strings.TrimSpace(detail[idx+len("解析实体名:"):])
	if rest == "" {
		return nil
	}
	var out []string
	for _, p := range strings.FieldsFunc(rest, func(r rune) bool {
		return r == '、' || r == ',' || r == '，' || r == ' '
	}) {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// buildDataContext 把 memory-search 结果拼成给 analyze 的上下文文本。
func buildDataContext(d *memorySearchData) string {
	if d == nil {
		return ""
	}
	var sb strings.Builder

	if len(d.PinnedFacts) > 0 {

		sb.WriteString("\n【手工置顶的背景知识】\n")
		for _, f := range d.PinnedFacts {
			sb.WriteString("- " + f.Fact + "\n")
		}
		if len(d.PinnedContactAliases) > 0 {
			sb.WriteString("\n联系人外号（用于确认上下文里的人物指向）：\n")
			for _, pca := range d.PinnedContactAliases {
				label := pca.DisplayName
				if label == "" {
					label = pca.ContactKey
				}
				sb.WriteString(fmt.Sprintf("- %s（%s）\n", label, strings.Join(pca.Aliases, "、")))
			}
		}
	}

	// 无需记忆：直接回答（不注入搜索片段）
	if d.Decomposition != nil && !d.Decomposition.NeedsMemory {
		return sb.String()
	}

	if len(d.Facts) > 0 {
		sb.WriteString("\n【从聊天记录中提炼的事实】\n")
		for _, f := range d.Facts {
			src := f.SourceName
			if src == "" {
				src = f.ContactKey
				if src == "" {
					src = "未知"
				}
			}
			sb.WriteString(fmt.Sprintf("- （来源：%s）%s\n", src, f.Fact))
		}
	}

	if len(d.RawHits) > 0 {
		sb.WriteString("\n【原文精确命中（聊天记录原文）】\n")
		for _, rh := range d.RawHits {
			sb.WriteString(fmt.Sprintf("[%s %s %s]：%s\n", rh.SourceName, rh.Datetime, rh.Sender, rh.Content))
		}
	}

	if len(d.VecMessages) > 0 {
		sb.WriteString("\n【原始消息向量检索结果】\n")
		for _, vm := range d.VecMessages {
			src := vm.SourceName
			if src == "" {
				src = vm.ContactKey
				if src == "" {
					src = "未知"
				}
			}
			sb.WriteString(fmt.Sprintf("[%s %s %s]：%s\n", src, vm.Datetime, vm.Sender, vm.Content))
		}
	}

	if len(d.Sources) > 0 {
		sb.WriteString("\n【记忆事实对应的源聊天记录】\n")
		for _, s := range d.Sources {
			for _, m := range s.Messages {
				sb.WriteString(fmt.Sprintf("[%s %s %s]：%s\n", s.SourceName, m.Datetime, m.Sender, m.Content))
			}
		}
	}

	if sb.Len() == 0 {
		return "\n【未找到相关记忆】"
	}
	return sb.String()
}

// analyzeQuestion 调用 POST /api/ai/analyze（跨联系人），生成最终回答。
// analyzeQuestion 调用 POST /api/ai/analyze（跨联系人），生成最终回答。
// 返回回答文本与此次调用的 token 用量（可能为 nil）。
func analyzeQuestion(ctx context.Context, cfg *Config, chatID, query, convKey string, history []llmMessage, dataContext string) (string, *analyzeUsage, error) {
	var sys strings.Builder
	sys.WriteString("你是 WeLink 的跨联系人 AI 助手，用户刚问了一个关于微信聊天记录的问题。\n")
	sys.WriteString("以下是从数据库中检索到的相关数据，请基于这些数据回答用户的问题。\n")
	sys.WriteString("要求：\n")
	sys.WriteString("1. 用中文回答，简洁清晰\n")
	sys.WriteString("2. 直接回答问题，不要废话\n")
	sys.WriteString("3. 如果数据不足以回答，诚实说明\n")
	sys.WriteString("4. 使用 Markdown 排版。\n")
	sys.WriteString("5. 如果涉及多个联系人，用列表列出并简要说明\n")
	sys.WriteString("6. 每段故事、结论或场景都要说明其依据的聊天记录原文（含前后上下文）作为佐证；引用原文时至少保留该事件前后各 5 条上下文聊天信息；若前后各 5 条仍不足以完整表达一个事件或观点，则继续延伸，直到能完整表达该事件为止。\n")
	sys.WriteString("7. 当用户提出“探索一下”“找一下”“列举一下”“有什么有趣的事情”等开放性请求时，请尽量给出详尽的内容：凡是主体和客体对应正确、即使只是略有相关的信息或案例，都尽量纳入回答；这类问题不要追求过度简洁，应把有价值的信息尽可能多地列出来，都不要遗漏。\n")
	sys.WriteString("8. 当用户要求把聊天记录整理或格式化输出时，请采用确定且统一的格式：每条记录单独一行，按“时间 ｜ 发送者 ｜ 内容”顺序排列，时间统一用 YYYY-MM-DD HH:MM；不同记录之间用“---”分隔。只要是格式化输出，务必严格套用这一格式，不要自行变化。\n")
	if len(history) > 0 {
		sys.WriteString("\n以下是本会话之前的问题与回答，供连续追问参考：\n")
		for _, m := range history {
			if m.Role == "user" {
				sys.WriteString("用户问：" + m.Content + "\n")
			} else if m.Role == "assistant" {
				sys.WriteString("AI 答：" + m.Content + "\n")
			}
		}
	}
	if dataContext != "" {
		sys.WriteString("\n以下是本次检索到的相关数据：\n" + dataContext + "\n")
	}

	msgs := []llmMessage{
		{Role: "system", Content: sys.String()},
		{Role: "user", Content: query},
	}

	payload, _ := json.Marshal(analyzeRequest{
		Username:        "__cross_contact__",
		IsGroup:         false,
		Messages:        msgs,
		ProfileID:       cfg.DefaultProfileID,
		SkipMemory:      true, // memory-search 已注入，analyze 不再重复加载记忆
		Query:           query,
		ConversationKey: convKey,
		Model:           cfg.LLMModel,
		ChatID:          chatID,
	})

	var answer strings.Builder
	var usage *analyzeUsage
	err := doSSE(ctx, cfg, "/api/ai/analyze", payload, func(data []byte) error {
		var ch analyzeChunk
		if err := json.Unmarshal(data, &ch); err != nil {
			return nil
		}
		if ch.Error != "" {
			return fmt.Errorf("%s", ch.Error)
		}
		if ch.Delta != "" {
			answer.WriteString(ch.Delta)
		}
		if ch.Usage != nil {
			usage = ch.Usage
		}
		return nil
	})
	if err != nil {
		return answer.String(), usage, err
	}
	return answer.String(), usage, nil
}
