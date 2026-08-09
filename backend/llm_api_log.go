package main

// llm_api_log.go — 后端 LLM API 调用日志
//
// 前端的 apiLogger 只能拦截浏览器→后端的 fetch 调用，
// 看不到后端→LLM 提供商（OpenAI/Gemini/Claude 等）的 HTTP 请求。
// 本文件提供一个全局环形缓冲区，在 llm.go 的关键调用点记录
// 每次出站 LLM 请求的 URL、请求体、响应状态、耗时等信息。
//
// 通过 /api/ai/llm-logs 端点暴露给前端 API 日志页面。

import (
	"sync"
	"time"
)

// LLMApiLogEntry 描述一次后端→LLM 提供商的 HTTP 调用。
type LLMApiLogEntry struct {
	ID           int       `json:"id"`
	Timestamp    time.Time `json:"timestamp"`
	Method       string    `json:"method"`
	URL          string    `json:"url"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	Feature      string    `json:"feature"` // chat / query_expansion / hyde / rerank / memory_extraction / query_decomposition
	RequestBody  string    `json:"request_body"`
	Status       int       `json:"status"`
	ResponseBody string    `json:"response_body"`
	FirstTokenMs int64     `json:"first_token_ms"`
	DurationMs   int64     `json:"duration_ms"`
	Error        string    `json:"error"`
}

const (
	maxLLMApiLogs = 200
	snippetLen    = 8192
	// maxLLMLogBytes 限制整个缓冲区占用的字节数（按下限近似），
	// 防止 AI 使用量大时日志缓冲区和 /api/ai/llm-logs 响应体无限膨胀，
	// 拖慢后端序列化与前端每 3 秒的轮询。
	maxLLMLogBytes = 1 << 20
)

var (
	llmApiLogMu  sync.RWMutex
	llmApiLogs   []LLMApiLogEntry
	llmApiLogSeq int
)

// llmLogEntryBytes 估算一条日志占用的字节数，用于总容量控制。
func llmLogEntryBytes(e LLMApiLogEntry) int {
	return len(e.RequestBody) + len(e.ResponseBody) + len(e.Error) + len(e.URL) +
		len(e.Provider) + len(e.Model) + len(e.Feature) + 512
}

// trimLLMApiLogs 在持有写锁的情况下，把日志压到条目上限和字节上限以内。
func trimLLMApiLogs() {
	total := 0
	for i := range llmApiLogs {
		total += llmLogEntryBytes(llmApiLogs[i])
	}
	for len(llmApiLogs) > maxLLMApiLogs || (total > maxLLMLogBytes && len(llmApiLogs) > 1) {
		removed := llmApiLogs[0]
		llmApiLogs = llmApiLogs[1:]
		total -= llmLogEntryBytes(removed)
	}
}

// logLLMApiCall 追加一条 LLM API 调用日志，返回它的 ID。
func logLLMApiCall(entry LLMApiLogEntry) int {
	llmApiLogMu.Lock()
	llmApiLogSeq++
	entry.ID = llmApiLogSeq

	llmApiLogs = append(llmApiLogs, entry)
	trimLLMApiLogs()
	llmApiLogMu.Unlock()

	// 同步持久化到 SQLite，容器重启后仍能恢复最近的调用日志。
	persistLLMApiLog(entry)
	trimLLMApiLogsDB()
	return entry.ID
}

// getLLMApiLogs 返回最近的 LLM API 调用日志（最新在前）。
func getLLMApiLogs() []LLMApiLogEntry {
	llmApiLogMu.RLock()
	defer llmApiLogMu.RUnlock()

	out := make([]LLMApiLogEntry, len(llmApiLogs))
	// 反转：最新的放最前面
	for i, e := range llmApiLogs {
		out[len(llmApiLogs)-1-i] = e
	}
	return out
}

// limitedBuffer 是一个 io.Writer，只保留前 max 字节的数据。
// 用于在 TeeReader 中捕获响应体片段，供日志展示。
type limitedBuffer struct {
	buf    []byte
	max    int
	filled bool
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
	if lb.filled {
		return len(p), nil
	}
	remaining := lb.max - len(lb.buf)
	if remaining <= 0 {
		lb.filled = true
		return len(p), nil
	}
	if len(p) > remaining {
		lb.buf = append(lb.buf, p[:remaining]...)
		lb.filled = true
		return len(p), nil
	}
	lb.buf = append(lb.buf, p...)
	return len(p), nil
}

func (lb *limitedBuffer) String() string {
	return string(lb.buf)
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…[truncated]"
}
