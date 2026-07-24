package main

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

// TokenUsage 记录单个模型的累计 token 使用量。
type TokenUsage struct {
	Model         string `json:"model"`
	PromptTokens  int64  `json:"prompt_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
	TotalTokens   int64  `json:"total_tokens"`
	CallCount     int64  `json:"call_count"`
	IsEmbedding   bool   `json:"is_embedding"`
}

type tokenStats struct {
	mu    sync.Mutex
	usage map[string]*TokenUsage // key = model + "|" + type
}

var globalTokenStats = &tokenStats{
	usage: make(map[string]*TokenUsage),
}

// recordTokenUsage 线程安全地记录一次调用的 token 消耗。
// model: 模型名（如 "deepseek-chat"、"nomic-embed-text"）
// kind: "llm" 或 "embedding"
// promptTokens, outputTokens: 估算的 token 数
func recordTokenUsage(model, kind string, promptTokens, outputTokens int) {
	if model == "" {
		model = "unknown"
	}
	key := model + "|" + kind
	globalTokenStats.mu.Lock()
	defer globalTokenStats.mu.Unlock()
	entry, ok := globalTokenStats.usage[key]
	if !ok {
		entry = &TokenUsage{Model: model, IsEmbedding: kind == "embedding"}
		globalTokenStats.usage[key] = entry
	}
	entry.PromptTokens += int64(promptTokens)
	entry.OutputTokens += int64(outputTokens)
	entry.TotalTokens = entry.PromptTokens + entry.OutputTokens
	entry.CallCount++
}

// estimateTokens 粗略估算 token 数：中文约 1 字 = 1.5 token，英文约 4 字符 = 1 token。
// 取 chars * 0.75 作为通用近似值，向上取整。
func estimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	// 简单近似：每 4 个字符约 1 token，但中文密度更高
	// 用 rune count 更准确
	runes := 0
	for range text {
		runes++
	}
	// 中文 ~1.5 token/字，英文 ~0.25 token/字符，取中间值
	return (runes*3 + 3) / 4 // ~0.75 token per rune
}

// estimateMsgTokens 估算一组消息的 token 数。
func estimateMsgTokens(msgs []LLMMessage) int {
	total := 0
	for _, m := range msgs {
		total += estimateTokens(m.Content)
		total += 4 // role overhead
	}
	return total
}

// estimateEmbeddingTokens 估算 embedding 的 token 数（输入文本长度）。
func estimateEmbeddingTokens(texts []string) int {
	total := 0
	for _, t := range texts {
		total += estimateTokens(t)
	}
	return total
}

// getTokenStatsSnapshot 返回当前 token 使用量的快照。
func getTokenStatsSnapshot() []TokenUsage {
	globalTokenStats.mu.Lock()
	defer globalTokenStats.mu.Unlock()
	out := make([]TokenUsage, 0, len(globalTokenStats.usage))
	for _, v := range globalTokenStats.usage {
		out = append(out, *v)
	}
	return out
}

// registerTokenStatsRoutes 挂载 token 使用统计端点。
func registerTokenStatsRoutes(api *gin.RouterGroup) {
	api.GET("/token-stats", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"usage": getTokenStatsSnapshot(),
		})
	})
}
