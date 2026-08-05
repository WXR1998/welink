package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// maxStreamParseFails 单次流式响应允许的 chunk 解析失败次数上限。
// 偶发一两条坏 chunk（网络分片/provider 杂讯）容忍并跳过；超过则说明流已损坏，
// 与其静默返回残缺结果（看起来"正常完成"），不如报错让用户重试（H3）。
const maxStreamParseFails = 5

// ─── 公共类型 ──────────────────────────────────────────────────────────────────

type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// StreamChunk 是 SSE 推给前端的单次增量
type StreamChunk struct {
	Delta     string       `json:"delta,omitempty"`
	Thinking  string       `json:"thinking,omitempty"` // 思考型模型的推理过程增量（Ollama reasoning 字段）
	Done      bool         `json:"done,omitempty"`
	Error     string       `json:"error,omitempty"`
	RagMeta   *RagMeta     `json:"rag_meta,omitempty"`
	Usage     *StreamUsage `json:"usage,omitempty"` // 本次调用的 token 统计
}

// StreamUsage 携带本次 LLM 调用的 token 使用统计。
type StreamUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	CachedTokens int `json:"cached_tokens,omitempty"` // 命中的 prompt cache token（provider 返回 0/未知时省略）
}

// RagMeta 携带 RAG 检索统计信息及命中消息（在 LLM 流式响应前发送）。
type RagMeta struct {
	Hits      int         `json:"hits"`               // FTS 直接命中数
	Retrieved int         `json:"retrieved"`          // 含窗口扩展后的消息数
	Total     int         `json:"total,omitempty"`    // 检索到的总数（截断前）
	Truncated bool        `json:"truncated,omitempty"` // 是否因 token 预算截断
	Messages  []RagSnipet `json:"messages,omitempty"` // 命中消息片段
}

// RagSnipet 是返回给前端展示的单条检索结果。
type RagSnipet struct {
	Datetime string `json:"datetime"`
	Sender   string `json:"sender"`
	Content  string `json:"content"`
	IsHit    bool   `json:"is_hit"` // true = 直接命中，false = 上下文扩展
}

// CompleteResponse 是非流式调用的 JSON 响应
type CompleteResponse struct {
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ─── Provider 配置 ─────────────────────────────────────────────────────────────

type llmConfig struct {
	provider          string
	apiKey            string
	baseURL           string
	model             string
	noThink           bool   // Ollama 思考型模型专用，开启后请求前加 /no_think 前缀
	reasoningEffort   string // off / low / medium / high；空字符串 = off
	contextWindow     int    // 上下文窗口 token 数，0 = 默认 128000
	compressThreshold int    // 上下文压缩阈值，0 = contextWindow - 4000
	feature           string // 日志标签：chat / query_expansion / hyde / rerank / memory_extraction
}

// reasoningBudgetTokens 把档位映射到 Claude thinking.budget_tokens
func reasoningBudgetTokens(effort string) int {
	switch effort {
	case "low":
		return 2048
	case "medium":
		return 8192
	case "high":
		return 16384
	default:
		return 0
	}
}

// defaultsFor 为已知 provider 填充默认 baseURL 和 model（若用户未配置）
func defaultsFor(p *llmConfig) {
	switch p.provider {
	case "deepseek":
		if p.baseURL == "" {
			p.baseURL = "https://api.deepseek.com/v1"
		}
		if p.model == "" {
			p.model = "deepseek-v4-pro"
		}
	case "doubao":
		// 豆包 = 字节火山方舟（Volcengine Ark），OpenAI 兼容
		if p.baseURL == "" {
			p.baseURL = "https://ark.cn-beijing.volces.com/api/v3"
		}
		if p.model == "" {
			p.model = "doubao-seed-2-0-pro-260215"
		}
	case "kimi":
		if p.baseURL == "" {
			p.baseURL = "https://api.moonshot.cn/v1"
		}
		if p.model == "" {
			p.model = "kimi-k2.6"
		}
	case "gemini":
		if p.baseURL == "" {
			p.baseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
		}
		if p.model == "" {
			// gemini-2.0-flash 已于 2026-06-01 关停（NOT_FOUND）；用稳定 GA 的 3.5-flash
			p.model = "gemini-3.5-flash"
		}
	case "glm":
		if p.baseURL == "" {
			p.baseURL = "https://open.bigmodel.cn/api/paas/v4"
		}
		if p.model == "" {
			p.model = "glm-5.1"
		}
	case "grok":
		if p.baseURL == "" {
			p.baseURL = "https://api.x.ai/v1"
		}
		if p.model == "" {
			p.model = "grok-4.3"
		}
	case "minimax":
		if p.baseURL == "" {
			p.baseURL = "https://api.minimax.io/v1"
		}
		if p.model == "" {
			p.model = "MiniMax-M3"
		}
	case "minimax-cn":
		if p.baseURL == "" {
			p.baseURL = "https://api.minimaxi.com/v1"
		}
		if p.model == "" {
			p.model = "MiniMax-M3"
		}
	case "openai":
		if p.baseURL == "" {
			p.baseURL = "https://api.openai.com/v1"
		}
		if p.model == "" {
			p.model = "gpt-5.5"
		}
	case "ollama":
		if p.baseURL == "" {
			p.baseURL = "http://localhost:11434/v1"
		}
		if p.model == "" {
			// Ollama 是本地模型，需用户先 ollama pull；给个较新的常见默认
			p.model = "llama3.3"
		}
	case "claude":
		// Claude 使用原生 API，不需要 baseURL
		if p.model == "" {
			// 旗舰 Opus 4.8；4.6 代起 id 改为无日期后缀格式，本身即 pinned 快照
			p.model = "claude-opus-4-8"
		}
	case "bedrock":
		if p.baseURL == "" {
			p.baseURL = "https://bedrock-runtime.us-east-1.amazonaws.com"
		}
		if p.model == "" {
			p.model = "us.anthropic.claude-opus-4-8"
		}
	case "vertex":
		// BaseURL 由用户填写完整端点，model 给个默认
		if p.model == "" {
			// gemini-2.0-flash-001 已于 2026-06-01 关停；用稳定 GA 的 3.5-flash
			p.model = "google/gemini-3.5-flash"
		}
	case "qwen":
		// 阿里通义千问 / DashScope 兼容模式
		if p.baseURL == "" {
			p.baseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
		}
		if p.model == "" {
			p.model = "qwen3.7-max"
		}
	case "hunyuan":
		// 腾讯混元，OpenAI 兼容
		if p.baseURL == "" {
			p.baseURL = "https://api.hunyuan.cloud.tencent.com/v1"
		}
		if p.model == "" {
			p.model = "hunyuan-turbos-latest"
		}
	case "qianfan":
		// 百度千帆 / 文心一言，OpenAI 兼容端点
		if p.baseURL == "" {
			p.baseURL = "https://qianfan.baidubce.com/v2"
		}
		if p.model == "" {
			p.model = "ernie-5.1"
		}
	case "openrouter":
		if p.baseURL == "" {
			p.baseURL = "https://openrouter.ai/api/v1"
		}
		if p.model == "" {
			p.model = "openai/gpt-5.5"
		}
	case "mistral":
		if p.baseURL == "" {
			p.baseURL = "https://api.mistral.ai/v1"
		}
		if p.model == "" {
			// mistral-large-latest 是滚动别名，已指向当前 Large 旗舰，自动跟新，保持不变
			p.model = "mistral-large-latest"
		}
	case "groq":
		if p.baseURL == "" {
			p.baseURL = "https://api.groq.com/openai/v1"
		}
		if p.model == "" {
			p.model = "openai/gpt-oss-120b"
		}
	case "together":
		if p.baseURL == "" {
			p.baseURL = "https://api.together.xyz/v1"
		}
		if p.model == "" {
			p.model = "deepseek-ai/DeepSeek-R1"
		}
	case "fireworks":
		if p.baseURL == "" {
			p.baseURL = "https://api.fireworks.ai/inference/v1"
		}
		if p.model == "" {
			p.model = "accounts/fireworks/models/deepseek-v4-pro"
		}
	case "perplexity":
		if p.baseURL == "" {
			p.baseURL = "https://api.perplexity.ai"
		}
		if p.model == "" {
			p.model = "sonar-pro"
		}
	case "cohere":
		// Cohere v2 提供 OpenAI 兼容端点
		if p.baseURL == "" {
			p.baseURL = "https://api.cohere.ai/compatibility/v1"
		}
		if p.model == "" {
			p.model = "command-a-plus-05-2026"
		}
	case "siliconflow":
		// 硅基流动
		if p.baseURL == "" {
			p.baseURL = "https://api.siliconflow.cn/v1"
		}
		if p.model == "" {
			p.model = "deepseek-ai/DeepSeek-V4-Pro"
		}
	case "yi":
		// 零一万物
		if p.baseURL == "" {
			p.baseURL = "https://api.lingyiwanwu.com/v1"
		}
		if p.model == "" {
			p.model = "yi-lightning"
		}
	case "stepfun":
		// 阶跃星辰
		if p.baseURL == "" {
			p.baseURL = "https://api.stepfun.com/v1"
		}
		if p.model == "" {
			p.model = "step-3.7-flash"
		}
	case "azure":
		// Azure OpenAI：用户必须填写完整 BaseURL（含 deployment 路径）
		// 例：https://{resource}.openai.azure.com/openai/deployments/{deployment}
		if p.model == "" {
			p.model = "gpt-5.5"
		}
	}
}

// ─── Profile 辅助 ──────────────────────────────────────────────────────────────

// llmConfigForProfile 根据 profile_id 从 LLMProfiles 中查找配置；
// 找不到或 profileID 为空时回退到单配置字段（向后兼容）。
func llmConfigForProfile(profileID string, prefs Preferences) llmConfig {
	var cfg llmConfig
	if profileID != "" {
		for _, p := range prefs.LLMProfiles {
			if p.ID == profileID {
				cfg = llmConfig{provider: p.Provider, apiKey: p.APIKey, baseURL: p.BaseURL, model: p.Model, noThink: p.NoThink, reasoningEffort: p.ReasoningEffort, contextWindow: p.ContextWindow, compressThreshold: p.CompressThreshold}
				goto applyGemini
			}
		}
	}
	cfg = llmConfig{provider: prefs.LLMProvider, apiKey: prefs.LLMAPIKey, baseURL: prefs.LLMBaseURL, model: prefs.LLMModel}
applyGemini:
	if cfg.provider == "gemini" && cfg.apiKey == "" && prefs.GeminiAccessToken != "" {
		if token, err := geminiValidToken(&prefs); err == nil {
			cfg.apiKey = token
		}
	}
	defaultsFor(&cfg)
	return cfg
}

// ─── 流式调用入口 ──────────────────────────────────────────────────────────────

// StreamLLM 向选定 provider 发起流式请求，将增量 chunk 写入 w（SSE 格式）。
// 调用方应在 goroutine 中执行此函数，并在完成后关闭连接。
func StreamLLM(w http.ResponseWriter, msgs []LLMMessage, prefs Preferences) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	sendChunk := func(chunk StreamChunk) {
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	streamLLMCore(sendChunk, msgs, prefs)
}

// streamLLMCore 是流式调用的核心逻辑，接受一个已配置好的 sendChunk 函数。
// 适用于需要在 LLM 响应前先发送元数据事件的场景（如 RAG）。
func streamLLMCore(sendChunk func(StreamChunk), msgs []LLMMessage, prefs Preferences) {
	if DemoMockActive() {
		demoLLMStream(sendChunk, msgs)
		return
	}
	// Gemini OAuth：若已授权则用 OAuth token 替代 API Key
	if prefs.LLMProvider == "gemini" && prefs.GeminiAccessToken != "" {
		if token, err := geminiValidToken(&prefs); err == nil {
			prefs.LLMAPIKey = token
		}
	}
	cfg := llmConfig{
		provider: prefs.LLMProvider,
		apiKey:   prefs.LLMAPIKey,
		baseURL:  prefs.LLMBaseURL,
		model:    prefs.LLMModel,
	}
	// qwen3 等思考型模型在 Ollama 上默认开启 thinking，
	// CPU 推理时每批会生成上千个思考 token（5+ 分钟）。
	// CompleteLLM 用于记忆提炼等非交互场景，禁用 thinking 大幅加速。
	if cfg.provider == "ollama" && (strings.Contains(cfg.model, "qwen3") || strings.Contains(cfg.model, "qwen2.5")) {
		cfg.noThink = true
	}
	defaultsFor(&cfg)

	err := dispatchLLMStream(sendChunk, msgs, cfg)
	if err != nil {
		sendChunk(StreamChunk{Error: err.Error()})
	}
	sendChunk(StreamChunk{Done: true})
}

// streamLLMCoreWithProfile 与 streamLLMCore 相同，但通过 profileID 解析配置。
func streamLLMCoreWithProfile(sendChunk func(StreamChunk), msgs []LLMMessage, prefs Preferences, profileID string) {
	if DemoMockActive() {
		demoLLMStream(sendChunk, msgs)
		return
	}
	cfg := llmConfigForProfile(profileID, prefs)
	err := dispatchLLMStream(sendChunk, msgs, cfg)
	if err != nil {
		sendChunk(StreamChunk{Error: err.Error()})
	}
	sendChunk(StreamChunk{Done: true})
}

// testLLMConnProfile 测试指定 profile 的连接可用性，返回实际使用的模型名。
func testLLMConnProfile(profileID string, prefs Preferences) (string, error) {
	cfg := llmConfigForProfile(profileID, prefs)
	if cfg.baseURL == "" || cfg.model == "" {
		return "", fmt.Errorf("未配置 Base URL 或模型")
	}
	// 复用 testLLMConn 逻辑：构造临时 Preferences 只填 LLM 字段
	tmp := Preferences{
		LLMProvider: cfg.provider,
		LLMAPIKey:   cfg.apiKey,
		LLMBaseURL:  cfg.baseURL,
		LLMModel:    cfg.model,
	}
	return testLLMConn(tmp)
}

const defaultContextWindow = 128000

// compressContextIfNeeded 当对话历史过长时，自动压缩旧消息：
// 1. 保留第一条 system 消息（含聊天记录上下文）
// 2. 将中间的旧消息用 LLM 总结成一条 system 消息
// 3. 保留最近若干轮对话
// 整个过程是同步阻塞的，但只在超过阈值时才触发。
func compressContextIfNeeded(msgs []LLMMessage, cfg llmConfig, send func(StreamChunk)) []LLMMessage {
	maxTokens := cfg.contextWindow
	if maxTokens <= 0 {
		maxTokens = defaultContextWindow
	}
	// 压缩阈值：用户可配置；未配置时默认 contextWindow - 4000（留 4K 给输出）
	compressThreshold := cfg.compressThreshold
	if compressThreshold <= 0 {
		compressThreshold = maxTokens - 4000
	}
	if compressThreshold < 1000 {
		compressThreshold = 1000
	}

	totalTokens := estimateMsgTokens(msgs)
	if totalTokens <= compressThreshold {
		return msgs
	}

	log.Printf("[llm] 上下文压缩触发：%d tokens（阈值 %d）", totalTokens, compressThreshold)
	if send != nil {
		send(StreamChunk{Delta: "⏳ 对话历史较长，正在自动压缩旧消息…\n\n"})
	}

	// 分离 system 消息和对话消息
	var systemMsgs []LLMMessage
	var convMsgs []LLMMessage
	for _, m := range msgs {
		if m.Role == "system" {
			systemMsgs = append(systemMsgs, m)
		} else {
			convMsgs = append(convMsgs, m)
		}
	}

	// 计算需要保留的最近消息数量（从后往前，直到总 token 数低于阈值的一半）
	keepCount := 0
	keepTokens := 0
	halfThreshold := compressThreshold / 2
	for i := len(convMsgs) - 1; i >= 0; i-- {
		msgTokens := estimateMsgTokens([]LLMMessage{convMsgs[i]})
		if keepTokens+msgTokens > halfThreshold {
			break
		}
		keepTokens += msgTokens
		keepCount++
	}
	// 至少保留最后一条对话消息，否则压缩后只剩 system 消息，
	// GLM 等模型会报 "messages 参数非法"
	if keepCount == 0 && len(convMsgs) > 0 {
		keepCount = 1
	}

	// 需要压缩的旧消息
	toCompress := convMsgs[:len(convMsgs)-keepCount]
	if len(toCompress) == 0 {
		return msgs
	}

	// 用 LLM 总结旧对话
	var convText strings.Builder
	for _, m := range toCompress {
		convText.WriteString(m.Role)
		convText.WriteString(": ")
		convText.WriteString(m.Content)
		convText.WriteString("\n\n")
	}

	summarizeMsgs := []LLMMessage{
		{Role: "system", Content: "你是一个对话总结助手。请将以下对话历史压缩成一段简洁的摘要，保留关键信息、结论和用户意图。用中文回答。"},
		{Role: "user", Content: convText.String()},
	}

	// 复用已有的 provider 路由逻辑完成同步摘要
	var summary string
	var err error
	switch cfg.provider {
	case "claude":
		summary, err = completeClaudeSync(summarizeMsgs, cfg)
	case "bedrock":
		summary, err = completBedrockSync(summarizeMsgs, cfg)
	case "vertex":
		summary, err = completVertexSync(summarizeMsgs, cfg)
	default:
		summary, err = completeOpenAICompatSync(summarizeMsgs, cfg)
	}
	if err != nil || summary == "" {
		// 压缩失败，退回到截断策略
		log.Printf("[llm] 上下文压缩失败（%v），退回截断", err)
		result := make([]LLMMessage, 0, len(systemMsgs)+keepCount+1)
		result = append(result, systemMsgs...)
		result = append(result, LLMMessage{
			Role:    "system",
			Content: "（注：部分早期对话内容因上下文长度限制已被省略）",
		})
		result = append(result, convMsgs[len(convMsgs)-keepCount:]...)
		return result
	}

	// 组装压缩后的消息
	result := make([]LLMMessage, 0, len(systemMsgs)+1+keepCount)
	result = append(result, systemMsgs...)
	result = append(result, LLMMessage{
		Role:    "system",
		Content: "以下是之前对话的摘要：\n\n" + summary,
	})
	result = append(result, convMsgs[len(convMsgs)-keepCount:]...)

	log.Printf("[llm] 上下文压缩完成：%d tokens → %d tokens",
		totalTokens, estimateMsgTokens(result))
	if send != nil {
		send(StreamChunk{Delta: "✅ 上下文已压缩，继续回答。\n\n"})
	}

	return result
}

// maxPromptChars 限制单次 LLM 调用的 prompt 总字符数（≈ 50K tokens）。
// 超过此上限时，从最长的 system 消息中间截断，保留开头和结尾。
const maxPromptChars = 100000

// truncatePromptChars 将整个 prompt 截断到 maxPromptChars 个字符以内。
// 策略：找到最长的 system 消息，从中间截断，保留开头 1/3 和结尾 2/3。
func truncatePromptChars(msgs []LLMMessage) []LLMMessage {
	totalChars := 0
	for _, m := range msgs {
		totalChars += len([]rune(m.Content))
	}
	if totalChars <= maxPromptChars {
		return msgs
	}

	log.Printf("[llm] prompt 超长（%d chars），开始截断", totalChars)

	// 找到最长的 system 消息进行截断
	largestIdx := -1
	largestLen := 0
	for i, m := range msgs {
		if m.Role == "system" {
			l := len([]rune(m.Content))
			if l > largestLen {
				largestLen = l
				largestIdx = i
			}
		}
	}

	if largestIdx < 0 {
		return msgs
	}

	excess := totalChars - maxPromptChars
	runes := []rune(msgs[largestIdx].Content)

	if excess >= len(runes) {
		msgs[largestIdx].Content = "（因长度限制，聊天记录已截断）"
		return msgs
	}

	// 从中间截断：保留开头 1/3 和结尾 2/3
	keepTotal := len(runes) - excess
	headLen := keepTotal / 3
	tailLen := keepTotal - headLen

	msgs[largestIdx].Content = string(runes[:headLen]) +
		"\n…（因长度限制已截断部分聊天记录）…\n" +
		string(runes[len(runes)-tailLen:])

	log.Printf("[llm] prompt 截断完成：%d → %d chars", totalChars, totalChars-excess)

	return msgs
}

// dispatchLLMStream 统一流式分发
func dispatchLLMStream(send func(StreamChunk), msgs []LLMMessage, cfg llmConfig) error {
	// Demo 模式下拒绝指向内网的 baseURL，防 SSRF（M2/L4）；本地部署不限制（Ollama 等走 localhost）
	if err := guardOutboundURL(cfg.baseURL); err != nil {
		return err
	}
	// 上下文窗口管理：对话过长时自动压缩旧消息
	msgs = compressContextIfNeeded(msgs, cfg, send)
	// Token 统计：记录输入 token
	promptTokens := estimateMsgTokens(msgs)
	// 用 wrapper 追踪输出 token
	outputChars := 0
	wrappedSend := func(chunk StreamChunk) {
		if chunk.Delta != "" {
			outputChars += len(chunk.Delta)
		}
		send(chunk)
	}
	llmStart := time.Now()
	t := startTimer("llm_stream")
	var err error
	switch cfg.provider {
	case "claude":
		err = streamClaude(wrappedSend, msgs, cfg)
	case "bedrock":
		err = streamBedrock(wrappedSend, msgs, cfg)
	case "vertex":
		err = streamVertex(wrappedSend, msgs, cfg)
	default:
		err = streamOpenAICompat(wrappedSend, msgs, cfg)
	}
	t.Done(err,
		"provider", cfg.provider,
		"model", cfg.model,
		"msg_count", len(msgs),
		"prompt_chars", llmPromptChars(msgs),
		"reasoning", cfg.reasoningEffort,
	)
	// Token 统计：记录输出 token
	outputTokens := estimateTokens(strings.Repeat("x", outputChars))
	recordTokenUsage(cfg.model, "chat", promptTokens, outputTokens, time.Since(llmStart).Milliseconds())
	// OpenAI 兼容流式在内部推送真实 usage；其余 provider 由这里用估算兜底。
	switch cfg.provider {
	case "claude", "bedrock", "vertex":
		send(StreamChunk{Usage: &StreamUsage{
			PromptTokens: promptTokens,
			OutputTokens: outputTokens,
			TotalTokens:  promptTokens + outputTokens,
		}})
	}
	return err
}

// llmPromptChars 累加所有消息正文长度，用于埋点。
func llmPromptChars(msgs []LLMMessage) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
	}
	return n
}

// ─── OpenAI 兼容流式实现 ───────────────────────────────────────────────────────

type openAIRequest struct {
	Model           string       `json:"model"`
	Messages        []LLMMessage `json:"messages"`
	Stream          bool         `json:"stream"`
	Think           *bool        `json:"think,omitempty"`            // Ollama 专用：false = 禁用思考模式
	Stop            []string     `json:"stop,omitempty"`             // 停止词，防止模型输出特殊 token
	ReasoningEffort string       `json:"reasoning_effort,omitempty"` // OpenAI o-series / gpt-5-reasoning：low / medium / high
}

func streamOpenAICompat(send func(StreamChunk), msgs []LLMMessage, cfg llmConfig) error {
	if cfg.apiKey == "" && cfg.provider != "ollama" {
		return fmt.Errorf("未配置 API Key")
	}
	if cfg.baseURL == "" {
		return fmt.Errorf("未配置 Base URL")
	}
	if cfg.model == "" {
		return fmt.Errorf("未配置模型")
	}
	// 累计实际输出的字符数，用于在 provider 不返回 usage 时估算输出 token；
	// 这样输出 token 会随每条回答的实际长短变化，而不是固定为 0。
	outChars := 0
	rawSend := send
	send = func(chunk StreamChunk) {
		if chunk.Delta != "" {
			outChars += len(chunk.Delta)
		}
		rawSend(chunk)
	}
	// qwen3 等思考型模型在 Ollama CPU 上极慢，流式场景也禁用 thinking
	if cfg.provider == "ollama" && (strings.Contains(cfg.model, "qwen3") || strings.Contains(cfg.model, "qwen2.5")) {
		cfg.noThink = true
	}

	reqBody := openAIRequest{Model: cfg.model, Messages: msgs, Stream: true}
	if cfg.noThink {
		f := false
		reqBody.Think = &f
	}
	if cfg.provider == "ollama" {
		reqBody.Stop = []string{"<|endoftext|>", "<|im_end|>", "<|im_start|>"}
	}
	// OpenAI o-series / gpt-5-reasoning 等支持 reasoning_effort 的模型
	if cfg.reasoningEffort != "" && cfg.reasoningEffort != "off" && cfg.provider == "openai" {
		reqBody.ReasoningEffort = cfg.reasoningEffort
	}
	body, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", cfg.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)

	llmStart := time.Now()
	resp, err := httpClientLLMStream.Do(req)
	durMs := time.Since(llmStart).Milliseconds()
	if err != nil {
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: cfg.baseURL + "/chat/completions", Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), DurationMs: durMs, Error: err.Error()})
		return fmt.Errorf("请求失败：%w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: cfg.baseURL + "/chat/completions", Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(string(raw), snippetLen), DurationMs: durMs, Error: fmt.Sprintf("API 错误 %d", resp.StatusCode)})
		return fmt.Errorf("API 错误 %d：%s", resp.StatusCode, truncate(string(raw), 200))
	}

	// TeeReader 捕获响应体片段供日志展示
	var respBuf limitedBuffer
	respBuf.max = snippetLen
	teeReader := io.TeeReader(resp.Body, &respBuf)
	scanner := bufio.NewScanner(teeReader)
	// 用于检测 <think>...</think> 标签（MiniMax / DeepSeek-R1 等思考模型）
	inThinkTag := false
	thinkBuf := ""
	parseFails := 0 // 累计 chunk 解析失败数，超阈值即中止，避免静默丢数据（H3）
	gotDone := false // 是否收到 [DONE] 标记
	// 流式响应通常把 usage 放在最后一个 chunk，这里解析并透传给上层。
	// 不同提供商字段不同：OpenAI 用 usage.prompt_tokens_details.cached_tokens，
	// DeepSeek 用 usage.prompt_cache_hit_tokens；取到哪个用哪个，取不到即 0。
	var usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		OutputTokens        int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			gotDone = true
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning"` // Ollama 思考型模型的推理增量
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens        int `json:"prompt_tokens"`
				CompletionTokens    int `json:"completion_tokens"`
				TotalTokens         int `json:"total_tokens"`
				PromptTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
				PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			parseFails++
			log.Printf("[llm] OpenAI 兼容流 chunk 解析失败(%d)：%v；payload=%q", parseFails, err, truncate(payload, 200))
			if parseFails > maxStreamParseFails {
				return fmt.Errorf("响应流多次解析失败（%d 次），结果可能不完整，请重试", parseFails)
			}
			continue
		}
		if chunk.Usage != nil {
			usage.PromptTokens = chunk.Usage.PromptTokens
			usage.OutputTokens = chunk.Usage.CompletionTokens
			usage.TotalTokens = chunk.Usage.TotalTokens
			usage.PromptTokensDetails.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			usage.PromptCacheHitTokens = chunk.Usage.PromptCacheHitTokens
		}
		if len(chunk.Choices) > 0 {
			d := chunk.Choices[0].Delta
			// Ollama 专用 reasoning 字段
			if d.Reasoning != "" {
				send(StreamChunk{Thinking: d.Reasoning})
			}
			if d.Content != "" {
				// 解析 <think>...</think> 标签（流式逐 chunk 拼接）
				text := d.Content
				for len(text) > 0 {
					if inThinkTag {
						// 在思考标签内，寻找 </think>
						endIdx := strings.Index(text, "</think>")
						if endIdx >= 0 {
							// 思考结束
							thinkBuf += text[:endIdx]
							if thinkBuf != "" {
								send(StreamChunk{Thinking: thinkBuf})
								thinkBuf = ""
							}
							inThinkTag = false
							text = strings.TrimLeft(text[endIdx+8:], "\n\r ") // 去掉 </think> 后的换行空白
						} else {
							// 还没结束，继续积累
							thinkBuf += text
							// 定期 flush 思考内容（避免长思考时前端无反馈）
							if len(thinkBuf) > 50 {
								send(StreamChunk{Thinking: thinkBuf})
								thinkBuf = ""
							}
							text = ""
						}
					} else {
						// 不在思考标签内，寻找 <think>
						startIdx := strings.Index(text, "<think>")
						if startIdx >= 0 {
							// <think> 之前的内容是正文
							if startIdx > 0 {
								send(StreamChunk{Delta: text[:startIdx]})
							}
							inThinkTag = true
							thinkBuf = ""
							text = text[startIdx+7:] // len("<think>") = 7
						} else {
							// 没有 <think>，全部是正文
							send(StreamChunk{Delta: text})
							text = ""
						}
					}
				}
			}
		}
	}
	// 如果流结束时还有未 flush 的思考内容
	if thinkBuf != "" {
		send(StreamChunk{Thinking: thinkBuf})
	}
	// 检测流是否被意外中断（没收到 [DONE] 就结束了）
	if !gotDone {
		if err := scanner.Err(); err != nil {
			logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: cfg.baseURL + "/chat/completions", Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(respBuf.String(), snippetLen), DurationMs: durMs, Error: "响应流被意外中断"})
			return fmt.Errorf("响应流被意外中断（%v），已生成的内容可能不完整", err)
		}
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: cfg.baseURL + "/chat/completions", Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(respBuf.String(), snippetLen), DurationMs: durMs, Error: "响应流被意外中断"})
		return fmt.Errorf("响应流被意外中断，已生成的内容可能不完整")
	}
	logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: cfg.baseURL + "/chat/completions", Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(respBuf.String(), snippetLen), DurationMs: durMs})
	// 推送 token 使用信息：优先用 provider 返回的真实 usage，取不到再回退估算。
	if usage.PromptTokens == 0 && usage.OutputTokens == 0 {
		usage.PromptTokens = estimateMsgTokens(msgs)
		usage.OutputTokens = estimateTokens(strings.Repeat("x", outChars))
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.OutputTokens
	}
	cached := usage.PromptTokensDetails.CachedTokens
	if cached == 0 {
		cached = usage.PromptCacheHitTokens
	}
	send(StreamChunk{Usage: &StreamUsage{
		PromptTokens: usage.PromptTokens,
		OutputTokens: usage.OutputTokens,
		TotalTokens:  usage.TotalTokens,
		CachedTokens: cached,
	}})
	return scanner.Err()
}

// ─── Claude 原生 API 流式实现 ─────────────────────────────────────────────────

type claudeRequest struct {
	Model     string           `json:"model"`
	MaxTokens int              `json:"max_tokens"`
	System    string           `json:"system,omitempty"`
	Messages  []LLMMessage     `json:"messages"`
	Stream    bool             `json:"stream"`
	Thinking  *claudeThinking  `json:"thinking,omitempty"` // Extended Thinking（Sonnet 4+ / Opus 4+）
}

type claudeThinking struct {
	Type         string `json:"type"`           // "enabled"
	BudgetTokens int    `json:"budget_tokens"`  // 1024-64000
}

func streamClaude(send func(StreamChunk), msgs []LLMMessage, cfg llmConfig) error {
	if cfg.apiKey == "" {
		return fmt.Errorf("未配置 API Key")
	}
	if cfg.model == "" {
		return fmt.Errorf("未配置模型")
	}

	// 分离 system 消息
	var system string
	var userMsgs []LLMMessage
	for _, m := range msgs {
		if m.Role == "system" {
			system = m.Content
		} else {
			userMsgs = append(userMsgs, m)
		}
	}

	reqObj := claudeRequest{
		Model:     cfg.model,
		MaxTokens: 8192,
		System:    system,
		Messages:  userMsgs,
		Stream:    true,
	}
	if budget := reasoningBudgetTokens(cfg.reasoningEffort); budget > 0 {
		reqObj.Thinking = &claudeThinking{Type: "enabled", BudgetTokens: budget}
		// Extended Thinking 要求 max_tokens > budget_tokens
		reqObj.MaxTokens = budget + 8192
	}
	body, _ := json.Marshal(reqObj)

	baseURL := "https://api.anthropic.com"
	if cfg.baseURL != "" {
		baseURL = cfg.baseURL
	}
	req, err := http.NewRequest("POST", baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cfg.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := httpClientLLMStream.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API 错误 %d：%s", resp.StatusCode, truncate(string(raw), 200))
	}

	scanner := bufio.NewScanner(resp.Body)
	parseFails := 0 // 累计解析失败数，超阈值即中止（H3）
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Type     string `json:"type"`      // "text_delta" / "thinking_delta"
				Text     string `json:"text"`      // text_delta
				Thinking string `json:"thinking"`  // thinking_delta
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			parseFails++
			log.Printf("[llm] Claude 流事件解析失败(%d)：%v；payload=%q", parseFails, err, truncate(payload, 200))
			if parseFails > maxStreamParseFails {
				return fmt.Errorf("响应流多次解析失败（%d 次），结果可能不完整，请重试", parseFails)
			}
			continue
		}
		if event.Type != "content_block_delta" {
			continue
		}
		switch event.Delta.Type {
		case "thinking_delta":
			if event.Delta.Thinking != "" {
				send(StreamChunk{Thinking: event.Delta.Thinking})
			}
		case "text_delta", "":
			if event.Delta.Text != "" {
				send(StreamChunk{Delta: event.Delta.Text})
			}
		}
	}
	return scanner.Err()
}

// ─── 非流式调用 ───────────────────────────────────────────────────────────────

// CompleteLLM 发起非流式请求，返回完整响应文本（用于分段摘要）
func CompleteLLM(msgs []LLMMessage, prefs Preferences) (string, error) {
	return CompleteLLMFeature(msgs, prefs, "", "")
}

// CompleteLLMFeature 与 CompleteLLM 相同，但接受一个 feature 标签用于 API 日志。
// feature 值如 "query_expansion"、"hyde"、"memory_extraction" 等，空字符串 = 普通调用。
func CompleteLLMFeature(msgs []LLMMessage, prefs Preferences, feature string, profileID ...string) (string, error) {
	// Demo 模式：AI 被锁死时直接返回 canned 响应，不走真实 provider
	if DemoMockActive() {
		return demoLLMComplete(msgs), nil
	}
	pid := ""
	if len(profileID) > 0 {
		pid = profileID[0]
	}
	// 使用 llmConfigForProfile 解析用户选择的 provider，
	// 而非默认的 prefs.LLMProvider 字段
	cfg := llmConfigForProfile(pid, prefs)
	cfg.feature = feature
	// Gemini OAuth：若已授权则用 OAuth token 替代 API Key
	if cfg.provider == "gemini" && cfg.apiKey == "" && prefs.GeminiAccessToken != "" {
		if token, err := geminiValidToken(&prefs); err == nil {
			cfg.apiKey = token
		}
	}
	// qwen3 等思考型模型在 Ollama 上默认开启 thinking，
	// CPU 推理时每批会生成上千个思考 token（5+ 分钟）。
	// CompleteLLM 用于记忆提炼等非交互场景，禁用 thinking 大幅加速。
	if cfg.provider == "ollama" && (strings.Contains(cfg.model, "qwen3") || strings.Contains(cfg.model, "qwen2.5")) {
		cfg.noThink = true
	}
	defaultsFor(&cfg)
	// Demo 模式下拒绝内网 baseURL，防 SSRF（M2/L4）
	if err := guardOutboundURL(cfg.baseURL); err != nil {
		return "", err
	}
	promptTokens := estimateMsgTokens(msgs)
	llmStart := time.Now()
	t := startTimer("llm_complete")
	var (
		out string
		err error
	)
	switch cfg.provider {
	case "claude":
		out, err = completeClaudeSync(msgs, cfg)
	case "bedrock":
		out, err = completBedrockSync(msgs, cfg)
	case "vertex":
		out, err = completVertexSync(msgs, cfg)
	default:
		out, err = completeOpenAICompatSync(msgs, cfg)
	}
	t.Done(err,
		"provider", cfg.provider,
		"model", cfg.model,
		"msg_count", len(msgs),
		"prompt_chars", llmPromptChars(msgs),
		"resp_chars", len(out),
	)
	outputTokens := estimateTokens(out)
	recordTokenUsage(cfg.model, "summary", promptTokens, outputTokens, time.Since(llmStart).Milliseconds())
	return out, err
}

func completeOpenAICompatSync(msgs []LLMMessage, cfg llmConfig) (string, error) {
	if cfg.apiKey == "" && cfg.provider != "ollama" {
		return "", fmt.Errorf("未配置 API Key")
	}
	if cfg.baseURL == "" {
		return "", fmt.Errorf("未配置 Base URL")
	}
	if cfg.model == "" {
		return "", fmt.Errorf("未配置模型")
	}

	reqBody := openAIRequest{Model: cfg.model, Messages: msgs, Stream: false}
	if cfg.noThink {
		f := false
		reqBody.Think = &f
	}
	if cfg.provider == "ollama" {
		reqBody.Stop = []string{"<|endoftext|>", "<|im_end|>", "<|im_start|>"}
	}
	if cfg.reasoningEffort != "" && cfg.reasoningEffort != "off" && cfg.provider == "openai" {
		reqBody.ReasoningEffort = cfg.reasoningEffort
	}
	body, _ := json.Marshal(reqBody)

	llmStart := time.Now()
	resp, err := withRetry(3, func(attempt int) (*http.Response, error) {
		req, err := http.NewRequest("POST", cfg.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+cfg.apiKey)
		return httpClientLLMSync.Do(req)
	})
	durMs := time.Since(llmStart).Milliseconds()
	if err != nil {
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: cfg.baseURL + "/chat/completions", Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), DurationMs: durMs, Error: err.Error()})
		return "", fmt.Errorf("请求失败：%w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: cfg.baseURL + "/chat/completions", Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(string(raw), snippetLen), DurationMs: durMs, Error: fmt.Sprintf("API 错误 %d", resp.StatusCode)})
		return "", fmt.Errorf("API 错误 %d：%s", resp.StatusCode, truncate(string(raw), 200))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	// TeeReader 捕获响应体片段供日志展示
	var respBuf limitedBuffer
	respBuf.max = snippetLen
	teeReader := io.TeeReader(resp.Body, &respBuf)
	if err := json.NewDecoder(teeReader).Decode(&result); err != nil {
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: cfg.baseURL + "/chat/completions", Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(respBuf.String(), snippetLen), DurationMs: durMs, Error: fmt.Sprintf("解析响应失败：%v", err)})
		return "", fmt.Errorf("解析响应失败：%w", err)
	}
	logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: cfg.baseURL + "/chat/completions", Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(respBuf.String(), snippetLen), DurationMs: durMs})
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("响应为空")
	}
	return result.Choices[0].Message.Content, nil
}

func completeClaudeSync(msgs []LLMMessage, cfg llmConfig) (string, error) {
	if cfg.apiKey == "" {
		return "", fmt.Errorf("未配置 API Key")
	}
	if cfg.model == "" {
		return "", fmt.Errorf("未配置模型")
	}

	var system string
	var userMsgs []LLMMessage
	for _, m := range msgs {
		if m.Role == "system" {
			system = m.Content
		} else {
			userMsgs = append(userMsgs, m)
		}
	}

	reqObj := claudeRequest{
		Model: cfg.model, MaxTokens: 2048,
		System: system, Messages: userMsgs, Stream: false,
	}
	if budget := reasoningBudgetTokens(cfg.reasoningEffort); budget > 0 {
		reqObj.Thinking = &claudeThinking{Type: "enabled", BudgetTokens: budget}
		reqObj.MaxTokens = budget + 2048
	}
	body, _ := json.Marshal(reqObj)
	baseURL := "https://api.anthropic.com"
	if cfg.baseURL != "" {
		baseURL = cfg.baseURL
	}
	req, err := http.NewRequest("POST", baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cfg.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	llmStart := time.Now()
	resp, err := httpClientLLMSync.Do(req)
	durMs := time.Since(llmStart).Milliseconds()
	if err != nil {
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: baseURL + "/v1/messages", Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), DurationMs: durMs, Error: err.Error()})
		return "", fmt.Errorf("请求失败：%w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: baseURL + "/v1/messages", Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(string(raw), snippetLen), DurationMs: durMs, Error: fmt.Sprintf("API 错误 %d", resp.StatusCode)})
		return "", fmt.Errorf("API 错误 %d：%s", resp.StatusCode, truncate(string(raw), 200))
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	// TeeReader 捕获响应体片段供日志展示
	var respBuf limitedBuffer
	respBuf.max = snippetLen
	teeReader := io.TeeReader(resp.Body, &respBuf)
	if err := json.NewDecoder(teeReader).Decode(&result); err != nil {
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: baseURL + "/v1/messages", Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(respBuf.String(), snippetLen), DurationMs: durMs, Error: fmt.Sprintf("解析响应失败：%v", err)})
		return "", fmt.Errorf("解析响应失败：%w", err)
	}
	logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: baseURL + "/v1/messages", Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(respBuf.String(), snippetLen), DurationMs: durMs})
	for _, block := range result.Content {
		if block.Type == "text" && block.Text != "" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("响应为空")
}

// ─── 连接测试 ──────────────────────────────────────────────────────────────────

// testLLMConn 发起流式请求，收到第一个非空 delta 即中止并返回成功。
// 相比 CompleteLLM，不等待完整响应，对思考型模型（Qwen3+）特别友好。
func testLLMConn(prefs Preferences) (string, error) {
	if prefs.LLMProvider == "gemini" && prefs.GeminiAccessToken != "" {
		if token, err := geminiValidToken(&prefs); err == nil {
			prefs.LLMAPIKey = token
		}
	}
	cfg := llmConfig{
		provider: prefs.LLMProvider,
		apiKey:   prefs.LLMAPIKey,
		baseURL:  prefs.LLMBaseURL,
		model:    prefs.LLMModel,
	}
	// qwen3 等思考型模型在 Ollama 上默认开启 thinking，
	// CPU 推理时每批会生成上千个思考 token（5+ 分钟）。
	// CompleteLLM 用于记忆提炼等非交互场景，禁用 thinking 大幅加速。
	if cfg.provider == "ollama" && (strings.Contains(cfg.model, "qwen3") || strings.Contains(cfg.model, "qwen2.5")) {
		cfg.noThink = true
	}
	defaultsFor(&cfg)

	if cfg.baseURL == "" || cfg.model == "" {
		return "", fmt.Errorf("未配置 Base URL 或模型")
	}
	if cfg.apiKey == "" && cfg.provider != "ollama" {
		return "", fmt.Errorf("未配置 API Key")
	}

	// Bedrock / Vertex 走专用测试路径
	if cfg.provider == "bedrock" {
		result, err := completBedrockSync([]LLMMessage{{Role: "user", Content: "Hi"}}, cfg)
		if err != nil {
			return cfg.model, err
		}
		if result != "" {
			return cfg.model, nil
		}
		return cfg.model, fmt.Errorf("响应为空")
	}
	if cfg.provider == "vertex" {
		return testVertexConn(cfg)
	}

	body, _ := json.Marshal(openAIRequest{Model: cfg.model, Messages: []LLMMessage{{Role: "user", Content: "Hi"}}, Stream: true})
	req, err := http.NewRequest("POST", cfg.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return cfg.model, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)

	resp, err := httpClientFast.Do(req)
	if err != nil {
		return cfg.model, fmt.Errorf("请求失败：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return cfg.model, fmt.Errorf("API 错误 %d：%s", resp.StatusCode, truncate(string(raw), 200))
	}

	// 读到第一个有内容的 delta 就认为连接成功，立即返回
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}
		var event struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		if len(event.Choices) > 0 {
			// 收到第一个 chunk（不管内容是否为空），即表示模型在响应，连接正常
			return cfg.model, nil
		}
	}
	return cfg.model, nil
}

// ─── 辅助 ──────────────────────────────────────────────────────────────────────

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// LLMTestStats 携带 LLM 连接测试的统计数据。
type LLMTestStats struct {
	Model           string  `json:"model"`
	LatencyMs       int64   `json:"latency_ms"`        // 首 token 时延
	OutputTokens    int     `json:"output_tokens"`     // 生成 token 数
	TokensPerSecond float64 `json:"tokens_per_second"`  // 生成速度
}

// testLLMConnStats 与 testLLMConn 相同，但返回时延和 token 速度统计。
func testLLMConnStats(prefs Preferences) (*LLMTestStats, error) {
	if prefs.LLMProvider == "gemini" && prefs.GeminiAccessToken != "" {
		if token, err := geminiValidToken(&prefs); err == nil {
			prefs.LLMAPIKey = token
		}
	}
	cfg := llmConfig{
		provider: prefs.LLMProvider,
		apiKey:   prefs.LLMAPIKey,
		baseURL:  prefs.LLMBaseURL,
		model:    prefs.LLMModel,
	}
	if cfg.provider == "ollama" && (strings.Contains(cfg.model, "qwen3") || strings.Contains(cfg.model, "qwen2.5")) {
		cfg.noThink = true
	}
	defaultsFor(&cfg)

	if cfg.baseURL == "" || cfg.model == "" {
		return nil, fmt.Errorf("未配置 Base URL 或模型")
	}
	if cfg.apiKey == "" && cfg.provider != "ollama" {
		return nil, fmt.Errorf("未配置 API Key")
	}

	start := time.Now()

	// Bedrock / Vertex 走专用测试路径
	if cfg.provider == "bedrock" {
		result, err := completBedrockSync([]LLMMessage{{Role: "user", Content: "Hi"}}, cfg)
		if err != nil {
			return nil, err
		}
		latencyMs := time.Since(start).Milliseconds()
		tokens := estimateTokens(result)
		var tps float64
		if latencyMs > 0 {
			tps = float64(tokens) / float64(latencyMs) * 1000
		}
		return &LLMTestStats{Model: cfg.model, LatencyMs: latencyMs, OutputTokens: tokens, TokensPerSecond: tps}, nil
	}
	if cfg.provider == "vertex" {
		_, err := testVertexConn(cfg)
		if err != nil {
			return nil, err
		}
		latencyMs := time.Since(start).Milliseconds()
		return &LLMTestStats{Model: cfg.model, LatencyMs: latencyMs, OutputTokens: 0, TokensPerSecond: 0}, nil
	}

	body, _ := json.Marshal(openAIRequest{Model: cfg.model, Messages: []LLMMessage{{Role: "user", Content: "Hi"}}, Stream: true})
	req, err := http.NewRequest("POST", cfg.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)

	resp, err := httpClientFast.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API 错误 %d：%s", resp.StatusCode, truncate(string(raw), 200))
	}

	// 读取完整流，统计 token 数和生成速度
	scanner := bufio.NewScanner(resp.Body)
	firstTokenMs := int64(0)
	totalTokens := 0
	var allContent strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}
		var event struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		if len(event.Choices) > 0 && event.Choices[0].Delta.Content != "" {
			if firstTokenMs == 0 {
				firstTokenMs = time.Since(start).Milliseconds()
			}
			allContent.WriteString(event.Choices[0].Delta.Content)
			totalTokens = estimateTokens(allContent.String())
		}
	}

	totalMs := time.Since(start).Milliseconds()
	if firstTokenMs == 0 {
		firstTokenMs = totalMs
	}

	// tokens_per_second = output_tokens / generation_time
	genMs := totalMs - firstTokenMs
	var tps float64
	if genMs > 0 && totalTokens > 0 {
		tps = float64(totalTokens) / float64(genMs) * 1000
	}

	return &LLMTestStats{
		Model:           cfg.model,
		LatencyMs:       firstTokenMs,
		OutputTokens:    totalTokens,
		TokensPerSecond: tps,
	}, nil
}

// testLLMConnProfileStats 与 testLLMConnProfile 相同，但返回时延和 token 速度统计。
func testLLMConnProfileStats(profileID string, prefs Preferences) (*LLMTestStats, error) {
	cfg := llmConfigForProfile(profileID, prefs)
	if cfg.baseURL == "" || cfg.model == "" {
		return nil, fmt.Errorf("未配置 Base URL 或模型")
	}
	tmp := Preferences{
		LLMProvider: cfg.provider,
		LLMAPIKey:   cfg.apiKey,
		LLMBaseURL:  cfg.baseURL,
		LLMModel:    cfg.model,
	}
	return testLLMConnStats(tmp)
}
