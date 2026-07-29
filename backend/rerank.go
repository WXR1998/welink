package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RerankConfig 是重排 API 的运行时配置。
type RerankConfig struct {
	Provider string
	APIKey   string
	BaseURL  string
	Model    string
}

// defaultRerankConfig 从 Preferences 构造 RerankConfig 并填充各 provider 默认值。
// 默认不启用 rerank（Provider 为空）。
func defaultRerankConfig(prefs Preferences) RerankConfig {
	cfg := RerankConfig{
		Provider: prefs.RerankProvider,
		APIKey:   prefs.RerankAPIKey,
		BaseURL:  prefs.RerankBaseURL,
		Model:    prefs.RerankModel,
	}
	applyRerankDefaults(&cfg)
	return cfg
}

// applyRerankDefaults 为已知 provider 填充默认 baseURL 和 model。
func applyRerankDefaults(cfg *RerankConfig) {
	switch cfg.Provider {
	case "jina":
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.jina.ai/v1"
		}
		if cfg.Model == "" {
			cfg.Model = "jina-reranker-v2-base-multilingual"
		}
	case "cohere":
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.cohere.ai/v1"
		}
		if cfg.Model == "" {
			cfg.Model = "rerank-multilingual-v3.0"
		}
	case "siliconflow":
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.siliconflow.cn/v1"
		}
		if cfg.Model == "" {
			cfg.Model = "BAAI/bge-reranker-v2-m3"
		}
	}
}

// rerankConfigs 从 Preferences 构造 []RerankConfig（多提供商 fallback）。
// 优先使用 RerankProfiles；为空时回退到单字段配置。
func rerankConfigs(prefs Preferences) []RerankConfig {
	if len(prefs.RerankProfiles) > 0 {
		configs := make([]RerankConfig, 0, len(prefs.RerankProfiles))
		for _, p := range prefs.RerankProfiles {
			cfg := RerankConfig{
				Provider: p.Provider,
				APIKey:   p.APIKey,
				BaseURL:  p.BaseURL,
				Model:    p.Model,
			}
			applyRerankDefaults(&cfg)
			configs = append(configs, cfg)
		}
		return configs
	}
	single := defaultRerankConfig(prefs)
	if single.Provider == "" {
		return nil
	}
	return []RerankConfig{single}
}

// RerankResult 是单条重排结果。
type RerankResult struct {
	Index int     `json:"index"`
	Score float32 `json:"relevance_score"`
}

type rerankAPIResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

// rerankRequestPayload 是发给 rerank API 的请求体。
// 兼容 Jina / Cohere / SiliconFlow / TEI 等主流 rerank API。
type rerankRequestPayload struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n,omitempty"`
}

// RerankCandidates 调用 rerank API 对 documents 做精排，返回按分数降序的结果。
func RerankCandidates(query string, documents []string, cfg RerankConfig) ([]RerankResult, error) {
	if cfg.Provider == "" {
		return nil, fmt.Errorf("rerank: provider 未配置")
	}
	if len(documents) == 0 {
		return nil, nil
	}

	// Demo 模式：返回原始顺序（不做真实 rerank）
	if DemoMockActive() {
		results := make([]RerankResult, len(documents))
		for i := range documents {
			results[i] = RerankResult{Index: i, Score: 1.0 - float32(i)*0.01}
		}
		return results, nil
	}

	if err := guardOutboundURL(cfg.BaseURL); err != nil {
		return nil, err
	}

	payload := rerankRequestPayload{
		Model:     cfg.Model,
		Query:     query,
		Documents: documents,
		TopN:      len(documents),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("rerank: marshal: %w", err)
	}

	url := cfg.BaseURL + "/rerank"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rerank: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	durMs := time.Since(start).Milliseconds()
	if err != nil {
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.Provider, Model: cfg.Model, RequestBody: truncateStr(string(body), snippetLen), DurationMs: durMs, Error: err.Error()})
		return nil, fmt.Errorf("rerank: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.Provider, Model: cfg.Model, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(string(raw), snippetLen), DurationMs: durMs, Error: fmt.Sprintf("API 错误 %d", resp.StatusCode)})
		return nil, fmt.Errorf("rerank: API 错误 %d", resp.StatusCode)
	}

	var apiResp rerankAPIResponse
	if err := json.Unmarshal(raw, &apiResp); err != nil {
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.Provider, Model: cfg.Model, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(string(raw), snippetLen), DurationMs: durMs, Error: fmt.Sprintf("解析响应失败：%v", err)})
		return nil, fmt.Errorf("rerank: 解析响应失败: %w", err)
	}

	results := make([]RerankResult, 0, len(apiResp.Results))
	for _, r := range apiResp.Results {
		results = append(results, RerankResult{
			Index: r.Index,
			Score: float32(r.RelevanceScore),
		})
	}

	logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.Provider, Model: cfg.Model, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(string(raw), snippetLen), DurationMs: durMs})

	return results, nil
}

// RerankCandidatesWithFallback 尝试多个 rerank 配置，直到成功或全部失败。
func RerankCandidatesWithFallback(query string, documents []string, configs []RerankConfig) ([]RerankResult, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("rerank: 未配置任何重排提供商")
	}
	var lastErr error
	for _, cfg := range configs {
		if cfg.Provider == "" {
			continue
		}
		results, err := RerankCandidates(query, documents, cfg)
		if err == nil {
			return results, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("rerank: 所有配置均无效")
	}
	return nil, lastErr
}
