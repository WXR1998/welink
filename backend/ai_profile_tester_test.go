package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLLMConnectionTestFirstOutputTimeoutDefaultsToTenSeconds(t *testing.T) {
	if llmConnectionTestFirstOutputTimeout != 10*time.Second {
		t.Fatalf("default first-output timeout = %s, want 10s", llmConnectionTestFirstOutputTimeout)
	}
}

func TestTestLLMProfileUsesConfiguredResponsesAPI(t *testing.T) {
	seenResponses := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}

		switch r.URL.Path {
		case "/chat/completions":
			t.Fatal("chat completions must not be tested when Responses API is configured")
		case "/responses":
			var request openAIResponsesRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode responses request: %v", err)
			}
			if !request.Stream || request.Model != "test-model" || len(request.Input) != 1 || request.Input[0].Content[0].Text != llmConnectionTestPrompt {
				t.Fatalf("unexpected responses request: %+v", request)
			}
			seenResponses = true
			fmt.Fprint(w, "event: response.output_text.delta\n")
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n")
			fmt.Fprint(w, "event: response.completed\n")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\"}\n\n")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	result := testLLMProfile("profile-1", "测试配置", llmConfig{
		provider:        "custom",
		apiKey:          "test-key",
		baseURL:         server.URL,
		model:           "test-model",
		useResponsesAPI: true,
	})

	if !result.OK {
		t.Fatalf("result should pass: %+v", result)
	}
	if result.Name != "测试配置" {
		t.Fatalf("profile name = %q, want 测试配置", result.Name)
	}
	if len(result.Protocols) != 1 || result.Protocols[0].Protocol != "responses" || !result.Protocols[0].OK {
		t.Fatalf("unexpected protocol results: %+v", result.Protocols)
	}
	if !seenResponses {
		t.Fatal("responses endpoint was not called")
	}
}

func TestTestLLMProfileUsesConfiguredChatCompletionsAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %s, want /chat/completions", r.URL.Path)
		}
		var request openAIRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !request.Stream || request.Model != "test-model" || len(request.Messages) != 1 || request.Messages[0].Content != llmConnectionTestPrompt {
			t.Fatalf("unexpected chat request: %+v", request)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	result := testLLMProfile("profile-1", "测试配置", llmConfig{
		provider: "custom",
		apiKey:   "test-key",
		baseURL:  server.URL,
		model:    "test-model",
	})

	if !result.OK || len(result.Protocols) != 1 || result.Protocols[0].Protocol != "chat_completions" || !result.Protocols[0].OK {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestTestLLMProfileDoesNotFallbackWhenFastModeIsUnsupported(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request openAIRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.ServiceTier != "priority" {
			t.Fatalf("service_tier = %q, want priority", request.ServiceTier)
		}
		http.Error(w, `{"error":{"message":"service_tier unsupported"}}`, http.StatusBadRequest)
	}))
	defer server.Close()

	result := testLLMProfile("profile-1", "测试配置", llmConfig{
		provider: "custom",
		apiKey:   "test-key",
		baseURL:  server.URL,
		model:    "test-model",
		fastMode: true,
	})

	if result.OK {
		t.Fatalf("unsupported Fast mode must fail the test: %+v", result)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1 without normal-mode fallback", requests)
	}
	if len(result.Protocols) != 1 || result.Protocols[0].OK {
		t.Fatalf("unexpected protocol results: %+v", result.Protocols)
	}
}

func TestTestLLMProfileFailsWhenFirstOutputExceedsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer server.Close()

	result := testLLMProfile("profile-1", "测试配置", llmConfig{
		provider:              "custom",
		apiKey:                "test-key",
		baseURL:               server.URL,
		model:                 "test-model",
		connectionTestTimeout: 10 * time.Millisecond,
	})

	if result.OK {
		t.Fatalf("late first output must fail the test: %+v", result)
	}
}
