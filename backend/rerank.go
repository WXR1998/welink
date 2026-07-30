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

// rerankRequestPayload 是发给 rerank API 的请求体。
// 同时包含 documents（Jina/Cohere/SiliconFlow 格式）和 texts（TEI 格式），
// 兼容主流 rerank API。大多数 API 会忽略不认识的字段。
type rerankRequestPayload struct {
	Model     string   `json:"model,omitempty"`
	Query     string   `json:"query"`
	Documents []string `json:"documents,omitempty"`
	Texts     []string `json:"texts,omitempty"`
	TopN      int      `json:"top_n,omitempty"`
}

// rerankAPIResponse 支持 Jina/Cohere/SiliconFlow 格式。
// Jina/Cohere: {"results": [{"index": 0, "relevance_score": 0.95}]}
type rerankAPIResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

// teiRerankResult 是 TEI 格式的裸数组响应。
type teiRerankResult struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

// maxRerankBatchSize 是单次 rerank API 请求的最大文档数。
// TEI 默认 max-batch-size=32；Jina/Cohere/SiliconFlow 通常允许更大。
const maxRerankBatchSize = 32

// RerankCandidates 调用 rerank API 对 documents 做精排，返回按分数降序的结果。
// 当文档数超过 API 的批次限制时，自动分批发送并合并结果。
func RerankCandidates(query string, documents []string, cfg RerankConfig) ([]RerankResult, error) {
	if cfg.Provider == "" {
		return nil, fmt.Errorf("rerank: provider 未配置")
	}
	if len(documents) == 0 {
		return nil, nil
	}

	// Demo 模式：返回原始顺序（不做真实 rerank）
	if DemoMockActive() {
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: cfg.BaseURL + "/rerank", Provider: cfg.Provider, Model: cfg.Model, Feature: "rerank", RequestBody: `{"note":"demo mock, no real request"}`, Status: 200, ResponseBody: `{"note":"demo mock returned fake results"}`, DurationMs: 0})
		results := make([]RerankResult, len(documents))
		for i := range documents {
			results[i] = RerankResult{Index: i, Score: 1.0 - float32(i)*0.01}
		}
		return results, nil
	}

	if err := guardOutboundURL(cfg.BaseURL); err != nil {
		errMsg := fmt.Sprintf("rerank: URL 安全检查失败 (BaseURL: %s): %v", cfg.BaseURL, err)
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: cfg.BaseURL + "/rerank", Provider: cfg.Provider, Model: cfg.Model, Feature: "rerank", DurationMs: 0, Error: errMsg})
		return nil, fmt.Errorf("%s", errMsg)
	}

	// 分批处理，避免超过 API 的 batch size 限制
	var allResults []RerankResult
	for batchStart := 0; batchStart < len(documents); batchStart += maxRerankBatchSize {
		batchEnd := batchStart + maxRerankBatchSize
		if batchEnd > len(documents) {
			batchEnd = len(documents)
		}
		batchDocs := documents[batchStart:batchEnd]

		batchResults, err := rerankSingleBatch(query, batchDocs, cfg)
		if err != nil {
			return nil, err
		}

		// 将批次内索引调整为全局索引
		for i := range batchResults {
			batchResults[i].Index += batchStart
		}
		allResults = append(allResults, batchResults...)
	}

	return allResults, nil
}

// rerankSingleBatch 发送单批文档到 rerank API 并返回结果。
func rerankSingleBatch(query string, documents []string, cfg RerankConfig) ([]RerankResult, error) {
	payload := rerankRequestPayload{
		Model:     cfg.Model,
		Query:     query,
		Documents: documents,
		Texts:     documents,
		TopN:      len(documents),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("rerank: marshal: %w", err)
	}

	url := cfg.BaseURL + "/rerank"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		errMsg := fmt.Sprintf("rerank: 构造请求失败 (URL: %s): %v", url, err)
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.Provider, Model: cfg.Model, Feature: "rerank", DurationMs: 0, Error: errMsg})
		return nil, fmt.Errorf("%s", errMsg)
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
		errMsg := fmt.Sprintf("rerank: 请求失败 (URL: %s): %v", url, err)
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.Provider, Model: cfg.Model, Feature: "rerank", RequestBody: truncateStr(string(body), snippetLen), DurationMs: durMs, Error: errMsg})
		return nil, fmt.Errorf("%s", errMsg)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		errMsg := fmt.Sprintf("rerank: API 错误 %d, URL: %s, 响应: %s", resp.StatusCode, url, truncateStr(string(raw), 500))
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.Provider, Model: cfg.Model, Feature: "rerank", RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(string(raw), snippetLen), DurationMs: durMs, Error: errMsg})
		return nil, fmt.Errorf("%s", errMsg)
	}

	results, parseErr := parseRerankResponse(raw)
	if parseErr != nil {
		errMsg := fmt.Sprintf("rerank: 解析响应失败: %v, 原始响应: %s", parseErr, truncateStr(string(raw), 500))
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.Provider, Model: cfg.Model, Feature: "rerank", RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(string(raw), snippetLen), DurationMs: durMs, Error: errMsg})
		return nil, fmt.Errorf("%s", errMsg)
	}

	logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.Provider, Model: cfg.Model, Feature: "rerank", RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(string(raw), snippetLen), DurationMs: durMs})

	return results, nil
}

// parseRerankResponse 解析 rerank API 的响应，支持两种格式：
// 1. Jina/Cohere/SiliconFlow: {"results": [{"index": 0, "relevance_score": 0.95}]}
// 2. TEI: [{"index": 0, "score": 0.95}]
func parseRerankResponse(raw []byte) ([]RerankResult, error) {
	// 尝试 Jina/Cohere 格式
	var apiResp rerankAPIResponse
	if err := json.Unmarshal(raw, &apiResp); err == nil && len(apiResp.Results) > 0 {
		results := make([]RerankResult, 0, len(apiResp.Results))
		for _, r := range apiResp.Results {
			results = append(results, RerankResult{
				Index: r.Index,
				Score: float32(r.RelevanceScore),
			})
		}
		return results, nil
	}

	// 尝试 TEI 格式（裸数组）
	var teiResults []teiRerankResult
	if err := json.Unmarshal(raw, &teiResults); err == nil && len(teiResults) > 0 {
		results := make([]RerankResult, 0, len(teiResults))
		for _, r := range teiResults {
			results = append(results, RerankResult{
				Index: r.Index,
				Score: float32(r.Score),
			})
		}
		return results, nil
	}

	return nil, fmt.Errorf("无法解析 rerank 响应，原始内容: %s", truncateStr(string(raw), 200))
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
