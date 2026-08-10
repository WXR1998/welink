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

type openAIResponsesRequest struct {
	Model       string                    `json:"model"`
	Input       []openAIResponsesInput    `json:"input"`
	Stream      bool                      `json:"stream"`
	Reasoning   *openAIResponsesReasoning `json:"reasoning,omitempty"`
	ServiceTier string                    `json:"service_tier,omitempty"`
}

type openAIResponsesInput struct {
	Role    string                   `json:"role"`
	Content []openAIResponsesContent `json:"content"`
}

type openAIResponsesContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAIResponsesReasoning struct {
	Effort string `json:"effort"`
}

func buildOpenAIResponsesRequest(msgs []LLMMessage, cfg llmConfig, stream bool) openAIResponsesRequest {
	input := make([]openAIResponsesInput, 0, len(msgs))
	for _, msg := range msgs {
		contentType := "input_text"
		if msg.Role == "assistant" {
			contentType = "output_text"
		}
		input = append(input, openAIResponsesInput{
			Role: msg.Role,
			Content: []openAIResponsesContent{{
				Type: contentType,
				Text: msg.Content,
			}},
		})
	}
	request := openAIResponsesRequest{Model: cfg.model, Input: input, Stream: stream}
	if cfg.reasoningEffort != "" && cfg.reasoningEffort != "off" && cfg.provider == "openai" {
		request.Reasoning = &openAIResponsesReasoning{Effort: cfg.reasoningEffort}
	}
	if cfg.openAIFastMode && supportsFastServiceTier(cfg.provider) {
		request.ServiceTier = "priority"
	}
	return request
}

func openAIResponsesURL(cfg llmConfig) string {
	return strings.TrimRight(cfg.baseURL, "/") + "/responses"
}

func streamOpenAIResponses(send func(StreamChunk), msgs []LLMMessage, cfg llmConfig) error {
	if cfg.apiKey == "" && cfg.provider != "ollama" {
		return fmt.Errorf("未配置 API Key")
	}
	if cfg.baseURL == "" {
		return fmt.Errorf("未配置 Base URL")
	}
	if cfg.model == "" {
		return fmt.Errorf("未配置模型")
	}

	requestBody := buildOpenAIResponsesRequest(msgs, cfg, true)
	body, _ := json.Marshal(requestBody)
	url := openAIResponsesURL(cfg)
	post := func(payload []byte) (*http.Response, error) {
		req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Authorization", "Bearer "+cfg.apiKey)
		return httpClientLLMStream.Do(req)
	}

	llmStart := time.Now()
	resp, err := post(body)
	if err != nil {
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), DurationMs: time.Since(llmStart).Milliseconds(), Error: err.Error()})
		return fmt.Errorf("请求失败：%w", err)
	}

	if resp.StatusCode != http.StatusOK && requestBody.ServiceTier != "" {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if shouldRetryWithoutFastServiceTier(resp.StatusCode, raw) {
			requestBody.ServiceTier = ""
			body, _ = json.Marshal(requestBody)
			resp, err = post(body)
			if err != nil {
				logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), DurationMs: time.Since(llmStart).Milliseconds(), Error: err.Error()})
				return fmt.Errorf("请求失败：%w", err)
			}
		} else {
			logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(string(raw), snippetLen), DurationMs: time.Since(llmStart).Milliseconds(), Error: fmt.Sprintf("API 错误 %d", resp.StatusCode)})
			return fmt.Errorf("API 错误 %d：%s", resp.StatusCode, truncate(string(raw), 200))
		}
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(string(raw), snippetLen), DurationMs: time.Since(llmStart).Milliseconds(), Error: fmt.Sprintf("API 错误 %d", resp.StatusCode)})
		return fmt.Errorf("API 错误 %d：%s", resp.StatusCode, truncate(string(raw), 200))
	}

	var respBuf limitedBuffer
	respBuf.max = snippetLen
	firstTokenMs := int64(0)
	content, usage, err := consumeOpenAIResponsesSSE(io.TeeReader(resp.Body, &respBuf), send, func() {
		if firstTokenMs == 0 {
			firstTokenMs = time.Since(llmStart).Milliseconds()
		}
	})
	durationMs := time.Since(llmStart).Milliseconds()
	if err != nil {
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(respBuf.String(), snippetLen), FirstTokenMs: firstTokenMs, DurationMs: durationMs, Error: err.Error()})
		return err
	}
	logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(respBuf.String(), snippetLen), FirstTokenMs: firstTokenMs, DurationMs: durationMs})
	if usage == nil {
		usage = &StreamUsage{
			PromptTokens: estimateMsgTokens(msgs),
			OutputTokens: estimateTokens(content),
		}
		usage.TotalTokens = usage.PromptTokens + usage.OutputTokens
	}
	send(StreamChunk{Usage: usage})
	return nil
}

func completeOpenAIResponsesSync(msgs []LLMMessage, cfg llmConfig) (string, error) {
	if cfg.apiKey == "" && cfg.provider != "ollama" {
		return "", fmt.Errorf("未配置 API Key")
	}
	if cfg.baseURL == "" {
		return "", fmt.Errorf("未配置 Base URL")
	}
	if cfg.model == "" {
		return "", fmt.Errorf("未配置模型")
	}

	// 某些 Responses API 网关（如 Lingjun）只接受流式上游请求。
	// 此处在服务端聚合 SSE，仍向调用方保持非流式字符串返回的既有契约。
	requestBody := buildOpenAIResponsesRequest(msgs, cfg, true)
	body, _ := json.Marshal(requestBody)
	url := openAIResponsesURL(cfg)
	llmStart := time.Now()
	post := func(payload []byte) (*http.Response, error) {
		return withRetry(3, func(attempt int) (*http.Response, error) {
			req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "text/event-stream")
			req.Header.Set("Authorization", "Bearer "+cfg.apiKey)
			return httpClientLLMSync.Do(req)
		})
	}
	resp, err := post(body)
	if err != nil {
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), DurationMs: time.Since(llmStart).Milliseconds(), Error: err.Error()})
		return "", fmt.Errorf("请求失败：%w", err)
	}

	if resp.StatusCode != http.StatusOK && requestBody.ServiceTier != "" {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if shouldRetryWithoutFastServiceTier(resp.StatusCode, raw) {
			requestBody.ServiceTier = ""
			body, _ = json.Marshal(requestBody)
			resp, err = post(body)
			if err != nil {
				logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), DurationMs: time.Since(llmStart).Milliseconds(), Error: err.Error()})
				return "", fmt.Errorf("请求失败：%w", err)
			}
		} else {
			logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(string(raw), snippetLen), DurationMs: time.Since(llmStart).Milliseconds(), Error: fmt.Sprintf("API 错误 %d", resp.StatusCode)})
			return "", fmt.Errorf("API 错误 %d：%s", resp.StatusCode, truncate(string(raw), 200))
		}
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(string(raw), snippetLen), DurationMs: time.Since(llmStart).Milliseconds(), Error: fmt.Sprintf("API 错误 %d", resp.StatusCode)})
		return "", fmt.Errorf("API 错误 %d：%s", resp.StatusCode, truncate(string(raw), 200))
	}

	var respBuf limitedBuffer
	respBuf.max = snippetLen
	firstTokenMs := int64(0)
	content, _, err := consumeOpenAIResponsesSSE(io.TeeReader(resp.Body, &respBuf), nil, func() {
		if firstTokenMs == 0 {
			firstTokenMs = time.Since(llmStart).Milliseconds()
		}
	})
	durationMs := time.Since(llmStart).Milliseconds()
	if err != nil {
		logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(respBuf.String(), snippetLen), FirstTokenMs: firstTokenMs, DurationMs: durationMs, Error: err.Error()})
		return "", err
	}
	logLLMApiCall(LLMApiLogEntry{Timestamp: time.Now(), Method: "POST", URL: url, Provider: cfg.provider, Model: cfg.model, Feature: cfg.feature, RequestBody: truncateStr(string(body), snippetLen), Status: resp.StatusCode, ResponseBody: truncateStr(respBuf.String(), snippetLen), FirstTokenMs: firstTokenMs, DurationMs: durationMs})
	if content == "" {
		return "", fmt.Errorf("响应为空")
	}
	return content, nil
}

func consumeOpenAIResponsesSSE(reader io.Reader, send func(StreamChunk), onFirstOutputText func()) (string, *StreamUsage, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var content strings.Builder
	var raw strings.Builder
	var eventType string
	var dataLines []string
	var usage *StreamUsage
	sawSSE := false
	completed := false
	sawOutputText := false
	var streamErr error

	flush := func() {
		if len(dataLines) == 0 || streamErr != nil {
			return
		}
		payload := strings.Join(dataLines, "\n")
		if payload == "[DONE]" {
			completed = true
			return
		}
		var event struct {
			Type    string `json:"type"`
			Delta   string `json:"delta"`
			Message string `json:"message"`
			Error   *struct {
				Message string `json:"message"`
			} `json:"error"`
			Response struct {
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
					TotalTokens  int `json:"total_tokens"`
					InputDetails struct {
						CachedTokens int `json:"cached_tokens"`
					} `json:"input_tokens_details"`
				} `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			streamErr = fmt.Errorf("解析 Responses 响应失败：%w", err)
			return
		}
		kind := eventType
		if kind == "" {
			kind = event.Type
		}
		switch kind {
		case "response.output_text.delta":
			if event.Delta != "" && !sawOutputText {
				sawOutputText = true
				if onFirstOutputText != nil {
					onFirstOutputText()
				}
			}
			content.WriteString(event.Delta)
			if send != nil && event.Delta != "" {
				send(StreamChunk{Delta: event.Delta})
			}
		case "response.reasoning_summary_text.delta":
			if send != nil && event.Delta != "" {
				send(StreamChunk{Thinking: event.Delta})
			}
		case "response.completed":
			completed = true
			usage = &StreamUsage{
				PromptTokens: event.Response.Usage.InputTokens,
				OutputTokens: event.Response.Usage.OutputTokens,
				TotalTokens:  event.Response.Usage.TotalTokens,
				CachedTokens: event.Response.Usage.InputDetails.CachedTokens,
			}
		case "error", "response.failed", "response.incomplete":
			if event.Error != nil && event.Error.Message != "" {
				streamErr = fmt.Errorf("Responses API 错误：%s", event.Error.Message)
			} else if event.Message != "" {
				streamErr = fmt.Errorf("Responses API 错误：%s", event.Message)
			} else {
				streamErr = fmt.Errorf("Responses API 请求失败：%s", kind)
			}
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		raw.WriteString(line)
		raw.WriteByte('\n')
		switch {
		case line == "":
			flush()
			eventType = ""
			dataLines = nil
		case strings.HasPrefix(line, "event:"):
			sawSSE = true
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			sawSSE = true
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return "", nil, fmt.Errorf("读取 Responses 响应失败：%w", err)
	}
	if streamErr != nil {
		return "", nil, streamErr
	}
	if !sawSSE {
		return parseOpenAIResponsesJSON([]byte(raw.String()), send)
	}
	if !completed {
		if content.Len() > 0 {
			log.Printf("[llm] Responses 流缺少完成事件，按干净 EOF 保留已接收正文")
			return content.String(), usage, nil
		}
		return "", nil, fmt.Errorf("Responses 响应流意外结束")
	}
	return content.String(), usage, nil
}

func parseOpenAIResponsesJSON(raw []byte, send func(StreamChunk)) (string, *StreamUsage, error) {
	var response struct {
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
			InputDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", nil, fmt.Errorf("解析 Responses 响应失败：%w", err)
	}
	var content strings.Builder
	for _, output := range response.Output {
		for _, part := range output.Content {
			if part.Type == "output_text" && part.Text != "" {
				content.WriteString(part.Text)
			}
		}
	}
	if send != nil && content.Len() > 0 {
		send(StreamChunk{Delta: content.String()})
	}
	return content.String(), &StreamUsage{
		PromptTokens: response.Usage.InputTokens,
		OutputTokens: response.Usage.OutputTokens,
		TotalTokens:  response.Usage.TotalTokens,
		CachedTokens: response.Usage.InputDetails.CachedTokens,
	}, nil
}
