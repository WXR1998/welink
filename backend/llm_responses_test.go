package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
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
