package main

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

const llmConnectionTestPrompt = "Reply exactly: OK"
const llmConnectionTestFirstOutputTimeout = 5 * time.Second

type AIProtocolTestResult struct {
	Protocol        string  `json:"protocol"`
	OK              bool    `json:"ok"`
	LatencyMs       int64   `json:"latency_ms"`
	TokensPerSecond float64 `json:"tokens_per_second,omitempty"`
	Error           string  `json:"error,omitempty"`
}

type AIProfileTestResult struct {
	ProfileID          string                 `json:"profile_id"`
	Name               string                 `json:"name"`
	Provider           string                 `json:"provider"`
	Model              string                 `json:"model"`
	OK                 bool                   `json:"ok"`
	LatencyMs          int64                  `json:"latency_ms"`
	TokensPerSecond    float64                `json:"tokens_per_second,omitempty"`
	SelectedProtocol   string                 `json:"selected_protocol,omitempty"`
	SelectedProtocolOK *bool                  `json:"selected_protocol_ok,omitempty"`
	Protocols          []AIProtocolTestResult `json:"protocols"`
	Error              string                 `json:"error,omitempty"`
}

type aiProfileTestJob struct {
	result           AIProfileTestResult
	selectedProtocol string
	run              func() []AIProtocolTestResult
}

func runAIProfileTests(jobs []aiProfileTestJob, onResult ...func(AIProfileTestResult)) []AIProfileTestResult {
	results := make([]AIProfileTestResult, len(jobs))
	workers := make(chan struct{}, 3)
	var wg sync.WaitGroup
	var publish func(AIProfileTestResult)
	if len(onResult) > 0 {
		publish = onResult[0]
	}

	for i, job := range jobs {
		wg.Add(1)
		go func(index int, profileJob aiProfileTestJob) {
			defer wg.Done()
			workers <- struct{}{}
			defer func() { <-workers }()

			result := profileJob.result
			result.SelectedProtocol = profileJob.selectedProtocol
			result.Protocols = profileJob.run()
			for _, protocol := range result.Protocols {
				if protocol.OK {
					result.OK = true
					if result.LatencyMs == 0 || protocol.LatencyMs < result.LatencyMs {
						result.LatencyMs = protocol.LatencyMs
						result.TokensPerSecond = protocol.TokensPerSecond
					}
				}
				if protocol.Protocol == profileJob.selectedProtocol {
					ok := protocol.OK
					result.SelectedProtocolOK = &ok
				}
			}
			if !result.OK {
				for _, protocol := range result.Protocols {
					if protocol.Error != "" {
						result.Error = protocol.Error
						break
					}
				}
				if result.Error == "" {
					result.Error = "测试失败"
				}
			}
			results[index] = result
			if publish != nil {
				publish(result)
			}
		}(i, job)
	}
	wg.Wait()
	return results
}

func runLLMProtocol(protocol string, call func() (string, error)) AIProtocolTestResult {
	start := time.Now()
	output, err := call()
	result := AIProtocolTestResult{Protocol: protocol, LatencyMs: time.Since(start).Milliseconds()}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if output == "" {
		result.Error = "响应为空"
		return result
	}
	result.OK = true
	if result.LatencyMs > 0 {
		result.TokensPerSecond = float64(estimateTokens(output)) / float64(result.LatencyMs) * 1000
	}
	return result
}

func testLLMProfile(profileID, name string, cfg llmConfig) AIProfileTestResult {
	defaultsFor(&cfg)
	cfg.strictFastMode = true
	if cfg.connectionTestTimeout == 0 {
		cfg.connectionTestTimeout = llmConnectionTestFirstOutputTimeout
	}
	result := AIProfileTestResult{
		ProfileID: profileID,
		Name:      name,
		Provider:  cfg.provider,
		Model:     cfg.model,
	}
	job := aiProfileTestJob{result: result, selectedProtocol: selectedLLMProtocol(cfg)}

	switch cfg.provider {
	case "claude":
		job.run = func() []AIProtocolTestResult {
			return []AIProtocolTestResult{
				runLLMProtocol("claude", func() (string, error) {
					return completeClaudeSync([]LLMMessage{{Role: "user", Content: llmConnectionTestPrompt}}, cfg)
				}),
			}
		}
	case "bedrock":
		job.run = func() []AIProtocolTestResult {
			return []AIProtocolTestResult{
				runLLMProtocol("bedrock", func() (string, error) {
					return completBedrockSync([]LLMMessage{{Role: "user", Content: llmConnectionTestPrompt}}, cfg)
				}),
			}
		}
	case "vertex":
		job.run = func() []AIProtocolTestResult {
			return []AIProtocolTestResult{
				runLLMProtocol("vertex", func() (string, error) {
					return completVertexSync([]LLMMessage{{Role: "user", Content: llmConnectionTestPrompt}}, cfg)
				}),
			}
		}
	default:
		job.run = func() []AIProtocolTestResult {
			protocol := selectedLLMProtocol(cfg)
			call := func() (string, error) {
				var output strings.Builder
				send := func(chunk StreamChunk) {
					output.WriteString(chunk.Delta)
					output.WriteString(chunk.Thinking)
				}
				if cfg.useResponsesAPI {
					err := streamOpenAIResponses(send, []LLMMessage{{Role: "user", Content: llmConnectionTestPrompt}}, cfg)
					return output.String(), err
				}
				err := streamOpenAICompat(send, []LLMMessage{{Role: "user", Content: llmConnectionTestPrompt}}, cfg)
				return output.String(), err
			}
			return []AIProtocolTestResult{runLLMProtocol(protocol, call)}
		}
	}

	return runAIProfileTests([]aiProfileTestJob{job})[0]
}

func testLLMProfiles(prefs Preferences) []AIProfileTestResult {
	return runAIProfileTests(llmProfileTestJobs(prefs))
}

func llmProfileTestJobs(prefs Preferences) []aiProfileTestJob {
	jobs := make([]aiProfileTestJob, 0, len(prefs.LLMProfiles))
	for index, profile := range prefs.LLMProfiles {
		cfg := llmConfigForProfile(profile.ID, prefs)
		name := profile.Name
		if name == "" {
			name = fmt.Sprintf("配置 %d", index+1)
		}
		jobs = append(jobs, aiProfileTestJob{
			result: AIProfileTestResult{ProfileID: profile.ID, Name: name, Provider: cfg.provider, Model: cfg.model},
			run: func() []AIProtocolTestResult {
				return testLLMProfile(profile.ID, name, cfg).Protocols
			},
			selectedProtocol: selectedLLMProtocol(cfg),
		})
	}
	return jobs
}

func selectedLLMProtocol(cfg llmConfig) string {
	switch cfg.provider {
	case "claude", "bedrock", "vertex":
		return cfg.provider
	case "":
		return ""
	case "default":
		return "chat_completions"
	default:
		if cfg.useResponsesAPI {
			return "responses"
		}
		return "chat_completions"
	}
}

func testEmbeddingProfiles(prefs Preferences) []AIProfileTestResult {
	return runAIProfileTests(embeddingProfileTestJobs(prefs))
}

func embeddingProfileTestJobs(prefs Preferences) []aiProfileTestJob {
	jobs := make([]aiProfileTestJob, 0, len(prefs.EmbeddingProfiles))
	for index, profile := range prefs.EmbeddingProfiles {
		cfg := EmbeddingConfig{Provider: profile.Provider, APIKey: profile.APIKey, BaseURL: profile.BaseURL, Model: profile.Model, Dims: profile.Dims}
		applyEmbeddingDefaults(&cfg)
		name := profile.Name
		if name == "" {
			name = fmt.Sprintf("Embedding %d", index+1)
		}
		jobs = append(jobs, aiProfileTestJob{
			result:           AIProfileTestResult{ProfileID: profile.ID, Name: name, Provider: cfg.Provider, Model: cfg.Model},
			selectedProtocol: "embedding",
			run: func() []AIProtocolTestResult {
				start := time.Now()
				vectors, err := GetEmbeddingsBatch([]string{"WeLink connection test"}, cfg)
				protocol := AIProtocolTestResult{Protocol: "embedding", LatencyMs: time.Since(start).Milliseconds()}
				if err != nil {
					protocol.Error = err.Error()
					return []AIProtocolTestResult{protocol}
				}
				if len(vectors) != 1 || len(vectors[0]) == 0 {
					protocol.Error = "未返回有效向量"
					return []AIProtocolTestResult{protocol}
				}
				for _, value := range vectors[0] {
					if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
						protocol.Error = "向量包含无效数值"
						return []AIProtocolTestResult{protocol}
					}
				}
				protocol.OK = true
				return []AIProtocolTestResult{protocol}
			},
		})
	}
	return jobs
}

func testRerankProfiles(prefs Preferences) []AIProfileTestResult {
	return runAIProfileTests(rerankProfileTestJobs(prefs))
}

func rerankProfileTestJobs(prefs Preferences) []aiProfileTestJob {
	jobs := make([]aiProfileTestJob, 0, len(prefs.RerankProfiles))
	for index, profile := range prefs.RerankProfiles {
		cfg := RerankConfig{Provider: profile.Provider, APIKey: profile.APIKey, BaseURL: profile.BaseURL, Model: profile.Model}
		applyRerankDefaults(&cfg)
		name := profile.Name
		if name == "" {
			name = fmt.Sprintf("Rerank %d", index+1)
		}
		jobs = append(jobs, aiProfileTestJob{
			result:           AIProfileTestResult{ProfileID: profile.ID, Name: name, Provider: cfg.Provider, Model: cfg.Model},
			selectedProtocol: "rerank",
			run: func() []AIProtocolTestResult {
				docs := []string{"今天天气很好", "张三说他明天来", "李四去北京出差了"}
				start := time.Now()
				ranked, err := RerankCandidates("张三来不来", docs, cfg)
				protocol := AIProtocolTestResult{Protocol: "rerank", LatencyMs: time.Since(start).Milliseconds()}
				if err != nil {
					protocol.Error = err.Error()
					return []AIProtocolTestResult{protocol}
				}
				if len(ranked) == 0 {
					protocol.Error = "未返回重排结果"
					return []AIProtocolTestResult{protocol}
				}
				for _, item := range ranked {
					if item.Index < 0 || item.Index >= len(docs) || math.IsNaN(float64(item.Score)) || math.IsInf(float64(item.Score), 0) {
						protocol.Error = "重排结果无效"
						return []AIProtocolTestResult{protocol}
					}
				}
				protocol.OK = true
				return []AIProtocolTestResult{protocol}
			},
		})
	}
	return jobs
}

func testMemLLMProfiles(prefs Preferences) []AIProfileTestResult {
	return runAIProfileTests(memLLMProfileTestJobs(prefs))
}

func memLLMProfileTestJobs(prefs Preferences) []aiProfileTestJob {
	if len(prefs.MemLLMProfiles) == 0 {
		cfg := llmConfigForProfile("", prefs)
		return []aiProfileTestJob{{
			result:           AIProfileTestResult{ProfileID: "", Name: "默认 AI 配置", Provider: cfg.provider, Model: cfg.model},
			selectedProtocol: selectedLLMProtocol(cfg),
			run: func() []AIProtocolTestResult {
				return testLLMProfile("", "默认 AI 配置", cfg).Protocols
			},
		}}
	}

	jobs := make([]aiProfileTestJob, 0, len(prefs.MemLLMProfiles))
	for index, profile := range prefs.MemLLMProfiles {
		cfg := llmConfig{
			provider:        profile.Provider,
			apiKey:          profile.APIKey,
			baseURL:         profile.BaseURL,
			model:           profile.Model,
			useResponsesAPI: profile.UseResponsesAPI,
			fastMode:        profile.FastMode,
		}
		defaultsFor(&cfg)
		name := profile.Name
		if name == "" {
			name = fmt.Sprintf("记忆模型 %d", index+1)
		}
		jobs = append(jobs, aiProfileTestJob{
			result:           AIProfileTestResult{ProfileID: profile.ID, Name: name, Provider: cfg.provider, Model: cfg.model},
			selectedProtocol: selectedLLMProtocol(cfg),
			run: func() []AIProtocolTestResult {
				return testLLMProfile(profile.ID, name, cfg).Protocols
			},
		})
	}
	return jobs
}
