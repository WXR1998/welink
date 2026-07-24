package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// TokenUsage 记录单个模型的累计 token 使用量。
type TokenUsage struct {
	Model         string `json:"model"`
	Kind          string `json:"kind"` // "chat" / "summary" / "embedding"
	PromptTokens  int64  `json:"prompt_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
	TotalTokens   int64  `json:"total_tokens"`
	CallCount     int64  `json:"call_count"`
	IsEmbedding   bool   `json:"is_embedding"`
}

// RecentSpeed 记录最近若干次请求的速度统计。
type RecentSpeed struct {
	Kind  string  `json:"kind"`
	Speed float64 `json:"speed"` // 平均 t/s
	Count int     `json:"count"` // 样本数
}

// tokenStats 维护全量（跨重启持久化）和当日（东八区 0:00 重置）两套统计。
type tokenStats struct {
	mu           sync.Mutex
	usage        map[string]*TokenUsage // 全量累计
	daily        map[string]*TokenUsage // 当日累计
	currentDate  string
	filePath     string
	recentSpeeds map[string][]float64 // key = kind, 滑动窗口
}

const recentSpeedWindow = 5

var dailyResetZone = time.FixedZone("CST", 8*3600)

func dailyResetDate() string {
	return time.Now().In(dailyResetZone).Format("2006-01-02")
}

var globalTokenStats = &tokenStats{
	usage:        make(map[string]*TokenUsage),
	daily:        make(map[string]*TokenUsage),
	recentSpeeds: make(map[string][]float64),
}

// tokenStatsPath 返回 token_stats.json 的路径，与 preferences.json 同目录。
func tokenStatsPath() string {
	dir := filepath.Dir(preferencesPath())
	return filepath.Join(dir, "token_stats.json")
}

// initTokenStats 从磁盘加载全量统计。在 main 启动早期调用。
func initTokenStats(filePath string) {
	globalTokenStats.mu.Lock()
	defer globalTokenStats.mu.Unlock()
	globalTokenStats.filePath = filePath
	globalTokenStats.currentDate = dailyResetDate()

	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	var persisted struct {
		Usage map[string]*TokenUsage `json:"usage"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		return
	}
	globalTokenStats.usage = persisted.Usage
	if globalTokenStats.usage == nil {
		globalTokenStats.usage = make(map[string]*TokenUsage)
	}
}

// saveTokenStatsLocked 将全量统计原子写入磁盘。调用方必须持锁。
func saveTokenStatsLocked() {
	if globalTokenStats.filePath == "" {
		return
	}
	data, err := json.MarshalIndent(struct {
		Usage map[string]*TokenUsage `json:"usage"`
	}{
		Usage: globalTokenStats.usage,
	}, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(globalTokenStats.filePath)
	os.MkdirAll(dir, 0700)
	tmp, err := os.CreateTemp(dir, ".token_stats-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	os.Chmod(tmpName, 0600)
	os.Rename(tmpName, globalTokenStats.filePath)
}

// recordTokenUsage 线程安全地记录一次调用的 token 消耗。
// model: 模型名（如 "deepseek-chat"、"nomic-embed-text"）
// kind: "chat"（AI 对话流式）/ "summary"（记忆总结非流式）/ "embedding"
// promptTokens, outputTokens: 估算的 token 数
// durationMs: 本次请求耗时（毫秒），>0 时用于计算 t/s
func recordTokenUsage(model, kind string, promptTokens, outputTokens int, durationMs int64) {
	if model == "" {
		model = "unknown"
	}
	key := model + "|" + kind

	globalTokenStats.mu.Lock()
	defer globalTokenStats.mu.Unlock()

	// 检查日期轮转（东八区 0:00）
	today := dailyResetDate()
	if today != globalTokenStats.currentDate {
		globalTokenStats.currentDate = today
		globalTokenStats.daily = make(map[string]*TokenUsage)
	}

	// 更新全量统计
	entry, ok := globalTokenStats.usage[key]
	if !ok {
		entry = &TokenUsage{Model: model, Kind: kind, IsEmbedding: kind == "embedding"}
		globalTokenStats.usage[key] = entry
	}
	entry.PromptTokens += int64(promptTokens)
	entry.OutputTokens += int64(outputTokens)
	entry.TotalTokens = entry.PromptTokens + entry.OutputTokens
	entry.CallCount++

	// 更新当日统计
	daily, ok := globalTokenStats.daily[key]
	if !ok {
		daily = &TokenUsage{Model: model, Kind: kind, IsEmbedding: kind == "embedding"}
		globalTokenStats.daily[key] = daily
	}
	daily.PromptTokens += int64(promptTokens)
	daily.OutputTokens += int64(outputTokens)
	daily.TotalTokens = daily.PromptTokens + daily.OutputTokens
	daily.CallCount++

	// 记录请求速度（t/s）
	if durationMs > 0 {
		totalTok := float64(promptTokens + outputTokens)
		seconds := float64(durationMs) / 1000.0
		if seconds > 0 {
			speed := totalTok / seconds
			speeds := globalTokenStats.recentSpeeds[kind]
			speeds = append(speeds, speed)
			if len(speeds) > recentSpeedWindow {
				speeds = speeds[len(speeds)-recentSpeedWindow:]
			}
			globalTokenStats.recentSpeeds[kind] = speeds
		}
	}

	// 持久化到磁盘
	saveTokenStatsLocked()
}

// estimateTokens 粗略估算 token 数：中文约 1 字 = 1.5 token，英文约 4 字符 = 1 token。
// 取 chars * 0.75 作为通用近似值，向上取整。
func estimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	runes := 0
	for range text {
		runes++
	}
	return (runes*3 + 3) / 4
}

// estimateMsgTokens 估算一组消息的 token 数。
func estimateMsgTokens(msgs []LLMMessage) int {
	total := 0
	for _, m := range msgs {
		total += estimateTokens(m.Content)
		total += 4
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

// getTokenStatsSnapshot 返回全量和当日两份快照。
func getTokenStatsSnapshot() ([]TokenUsage, []TokenUsage) {
	globalTokenStats.mu.Lock()
	defer globalTokenStats.mu.Unlock()

	allTime := make([]TokenUsage, 0, len(globalTokenStats.usage))
	for _, v := range globalTokenStats.usage {
		allTime = append(allTime, *v)
	}

	today := dailyResetDate()
	if today != globalTokenStats.currentDate {
		globalTokenStats.currentDate = today
		globalTokenStats.daily = make(map[string]*TokenUsage)
	}

	daily := make([]TokenUsage, 0, len(globalTokenStats.daily))
	for _, v := range globalTokenStats.daily {
		daily = append(daily, *v)
	}

	return allTime, daily
}

// getRecentSpeeds 返回最近若干次请求的平均速度（t/s）。
func getRecentSpeeds() []RecentSpeed {
	globalTokenStats.mu.Lock()
	defer globalTokenStats.mu.Unlock()

	out := make([]RecentSpeed, 0, len(globalTokenStats.recentSpeeds))
	for kind, speeds := range globalTokenStats.recentSpeeds {
		if len(speeds) == 0 {
			continue
		}
		sum := 0.0
		for _, s := range speeds {
			sum += s
		}
		out = append(out, RecentSpeed{
			Kind:  kind,
			Speed: sum / float64(len(speeds)),
			Count: len(speeds),
		})
	}
	return out
}

// registerTokenStatsRoutes 挂载 token 使用统计端点。
func registerTokenStatsRoutes(api *gin.RouterGroup) {
	api.GET("/token-stats", func(c *gin.Context) {
		allTime, daily := getTokenStatsSnapshot()
		c.JSON(http.StatusOK, gin.H{
			"usage":         allTime,
			"daily":         daily,
			"recent_speeds": getRecentSpeeds(),
		})
	})
}
