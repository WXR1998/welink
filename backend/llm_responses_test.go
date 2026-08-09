package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompleteOpenAICompatSyncBuffersResponsesSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %s, want /responses", r.URL.Path)
		}

		var request struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
			Input  []struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Model != "LJ/gpt-5.6-terra" || !request.Stream {
			t.Fatalf("unexpected request: %+v", request)
		}
		if len(request.Input) != 2 || request.Input[0].Role != "system" || request.Input[1].Content[0].Text != "你好" {
			t.Fatalf("messages were not converted to Responses input: %+v", request.Input)
		}

		// Lingjun may omit Content-Type even when this is an SSE body.
		fmt.Fprint(w, "event: response.output_text.delta\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"你\"}\n\n")
		fmt.Fprint(w, "event: response.output_text.delta\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"好\"}\n\n")
		fmt.Fprint(w, "event: response.completed\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":2,\"total_tokens\":14}}}\n\n")
	}))
	defer server.Close()

	cfg := llmConfig{
		provider:        "custom",
		apiKey:          "test-key",
		baseURL:         server.URL,
		model:           "LJ/gpt-5.6-terra",
		useResponsesAPI: true,
	}
	content, err := completeOpenAICompatSync([]LLMMessage{
		{Role: "system", Content: "你是助手"},
		{Role: "user", Content: "你好"},
	}, cfg)
	if err != nil {
		t.Fatalf("completeOpenAICompatSync returned error: %v", err)
	}
	if content != "你好" {
		t.Fatalf("content = %q, want %q", content, "你好")
	}
}

func TestStreamOpenAIResponsesForwardsDeltasAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %s, want /responses", r.URL.Path)
		}
		fmt.Fprint(w, "event: response.output_text.delta\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"A\"}\n\n")
		fmt.Fprint(w, "event: response.output_text.delta\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"B\"}\n\n")
		fmt.Fprint(w, "event: response.completed\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n")
	}))
	defer server.Close()

	var chunks []StreamChunk
	err := streamOpenAIResponses(func(chunk StreamChunk) {
		chunks = append(chunks, chunk)
	}, []LLMMessage{{Role: "user", Content: "Hi"}}, llmConfig{
		provider:        "custom",
		apiKey:          "test-key",
		baseURL:         server.URL,
		model:           "LJ/gpt-5.6-terra",
		useResponsesAPI: true,
	})
	if err != nil {
		t.Fatalf("streamOpenAIResponses returned error: %v", err)
	}
	if len(chunks) != 3 || chunks[0].Delta != "A" || chunks[1].Delta != "B" {
		t.Fatalf("unexpected streamed chunks: %+v", chunks)
	}
	if chunks[2].Usage == nil || chunks[2].Usage.TotalTokens != 5 {
		t.Fatalf("usage was not forwarded: %+v", chunks[2])
	}
}

func TestStreamOpenAIResponsesLogsFirstTokenAndFullDuration(t *testing.T) {
	llmApiLogMu.Lock()
	previousLogs, previousSeq := llmApiLogs, llmApiLogSeq
	llmApiLogs, llmApiLogSeq = nil, 0
	llmApiLogMu.Unlock()
	t.Cleanup(func() {
		llmApiLogMu.Lock()
		llmApiLogs, llmApiLogSeq = previousLogs, previousSeq
		llmApiLogMu.Unlock()
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(10 * time.Millisecond)
		fmt.Fprint(w, "event: response.output_text.delta\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"A\"}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, "event: response.completed\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	err := streamOpenAIResponses(func(StreamChunk) {}, []LLMMessage{{Role: "user", Content: "Hi"}}, llmConfig{
		provider:        "custom",
		apiKey:          "test-key",
		baseURL:         server.URL,
		model:           "LJ/gpt-5.6-terra",
		useResponsesAPI: true,
	})
	if err != nil {
		t.Fatalf("streamOpenAIResponses returned error: %v", err)
	}

	logs := getLLMApiLogs()
	if len(logs) != 1 {
		t.Fatalf("logged calls = %d, want 1", len(logs))
	}
	if logs[0].FirstTokenMs <= 0 {
		t.Fatalf("first token latency = %d, want > 0", logs[0].FirstTokenMs)
	}
	if logs[0].DurationMs <= logs[0].FirstTokenMs {
		t.Fatalf("total duration = %d, want > first token latency %d", logs[0].DurationMs, logs[0].FirstTokenMs)
	}
}

func TestStreamOpenAIResponsesDoesNotTreatJSONFallbackAsFirstToken(t *testing.T) {
	llmApiLogMu.Lock()
	previousLogs, previousSeq := llmApiLogs, llmApiLogSeq
	llmApiLogs, llmApiLogSeq = nil, 0
	llmApiLogMu.Unlock()
	t.Cleanup(func() {
		llmApiLogMu.Lock()
		llmApiLogs, llmApiLogSeq = previousLogs, previousSeq
		llmApiLogMu.Unlock()
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		time.Sleep(10 * time.Millisecond)
		fmt.Fprint(w, `{"output":[{"content":[{"type":"output_text","text":"A"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	err := streamOpenAIResponses(func(StreamChunk) {}, []LLMMessage{{Role: "user", Content: "Hi"}}, llmConfig{
		provider:        "custom",
		apiKey:          "test-key",
		baseURL:         server.URL,
		model:           "LJ/gpt-5.6-terra",
		useResponsesAPI: true,
	})
	if err != nil {
		t.Fatalf("streamOpenAIResponses returned error: %v", err)
	}

	logs := getLLMApiLogs()
	if len(logs) != 1 {
		t.Fatalf("logged calls = %d, want 1", len(logs))
	}
	if logs[0].FirstTokenMs != 0 {
		t.Fatalf("JSON fallback first token latency = %d, want 0", logs[0].FirstTokenMs)
	}
	if logs[0].DurationMs <= 0 {
		t.Fatalf("JSON fallback total duration = %d, want > 0", logs[0].DurationMs)
	}
}

func TestConsumeOpenAIResponsesSSEAcceptsCleanEOFWithContent(t *testing.T) {
	content, usage, err := consumeOpenAIResponsesSSE(
		strings.NewReader("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"可保留的正文\"}\n\n"),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("clean EOF after output delta should be accepted: %v", err)
	}
	if content != "可保留的正文" {
		t.Fatalf("content = %q, want streamed text", content)
	}
	if usage != nil {
		t.Fatalf("unexpected usage without completion event: %+v", usage)
	}
}

func TestBuildOpenAIResponsesRequestUsesOutputTextForAssistantHistory(t *testing.T) {
	request := buildOpenAIResponsesRequest([]LLMMessage{
		{Role: "system", Content: "系统提示"},
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好，有什么可以帮助你？"},
		{Role: "user", Content: "继续"},
	}, llmConfig{model: "LJ/gpt-5.6-terra"}, true)

	if got := request.Input[0].Content[0].Type; got != "input_text" {
		t.Fatalf("system content type = %q, want input_text", got)
	}
	if got := request.Input[1].Content[0].Type; got != "input_text" {
		t.Fatalf("user content type = %q, want input_text", got)
	}
	if got := request.Input[2].Content[0].Type; got != "output_text" {
		t.Fatalf("assistant content type = %q, want output_text", got)
	}
}
