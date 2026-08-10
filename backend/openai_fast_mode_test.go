package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIFastModeAppliesToOpenAICompatibleRequests(t *testing.T) {
	prefs := Preferences{
		OpenAIFastMode:      true,
		DefaultLLMProfileID: "openai",
		LLMProfiles: []LLMProfile{
			{ID: "openai", Provider: "openai", Model: "gpt-5.5"},
			{ID: "custom", Provider: "custom", Model: "gpt-5.5"},
		},
		MemLLMProfiles:         []MemLLMProfile{{ID: "memory", Provider: "openai", Model: "gpt-5.5"}},
		DefaultMemLLMProfileID: "memory",
	}

	openAIConfig := llmConfigForProfile("openai", prefs)
	if !openAIConfig.openAIFastMode {
		t.Fatal("OpenAI fast mode was not propagated to the LLM config")
	}
	if got := buildOpenAICompatRequest([]LLMMessage{{Role: "user", Content: "Hi"}}, openAIConfig, true).ServiceTier; got != "fast" {
		t.Fatalf("Chat Completions service_tier = %q, want fast", got)
	}
	if got := buildOpenAIResponsesRequest([]LLMMessage{{Role: "user", Content: "Hi"}}, openAIConfig, true).ServiceTier; got != "fast" {
		t.Fatalf("Responses service_tier = %q, want fast", got)
	}

	customConfig := llmConfigForProfile("custom", prefs)
	if got := buildOpenAICompatRequest([]LLMMessage{{Role: "user", Content: "Hi"}}, customConfig, true).ServiceTier; got != "fast" {
		t.Fatalf("custom Chat Completions service_tier = %q, want fast", got)
	}
	if got := buildOpenAIResponsesRequest([]LLMMessage{{Role: "user", Content: "Hi"}}, customConfig, true).ServiceTier; got != "fast" {
		t.Fatalf("custom Responses service_tier = %q, want fast", got)
	}

	memoryConfigs := memLLMConfigs(prefs)
	if len(memoryConfigs) != 1 || !memoryConfigs[0].openAIFastMode {
		t.Fatalf("memory extraction config did not inherit OpenAI fast mode: %+v", memoryConfigs)
	}
}

func TestCompleteOpenAICompatRetriesWithoutFastModeWhenUnsupported(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		var request struct {
			ServiceTier string `json:"service_tier"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if attempts == 1 {
			if request.ServiceTier != "fast" {
				t.Fatalf("first service_tier = %q, want fast", request.ServiceTier)
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Unknown parameter: service_tier"}}`)
			return
		}
		if request.ServiceTier != "" {
			t.Fatalf("fallback service_tier = %q, want empty", request.ServiceTier)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	content, err := completeOpenAICompatSync([]LLMMessage{{Role: "user", Content: "Hi"}}, llmConfig{
		provider:       "custom",
		apiKey:         "test-key",
		baseURL:        server.URL,
		model:          "test-model",
		openAIFastMode: true,
	})
	if err != nil {
		t.Fatalf("completeOpenAICompatSync returned error: %v", err)
	}
	if content != "ok" || attempts != 2 {
		t.Fatalf("content = %q, attempts = %d; want ok and 2", content, attempts)
	}
}

func TestCompleteOpenAIResponsesRetriesWithoutFastModeWhenUnsupported(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		var request struct {
			ServiceTier string `json:"service_tier"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if attempts == 1 {
			if request.ServiceTier != "fast" {
				t.Fatalf("first service_tier = %q, want fast", request.ServiceTier)
			}
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"error":{"message":"service_tier is not supported"}}`)
			return
		}
		if request.ServiceTier != "" {
			t.Fatalf("fallback service_tier = %q, want empty", request.ServiceTier)
		}
		fmt.Fprint(w, "event: response.output_text.delta\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprint(w, "event: response.completed\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	content, err := completeOpenAICompatSync([]LLMMessage{{Role: "user", Content: "Hi"}}, llmConfig{
		provider:        "custom",
		apiKey:          "test-key",
		baseURL:         server.URL,
		model:           "test-model",
		useResponsesAPI: true,
		openAIFastMode:  true,
	})
	if err != nil {
		t.Fatalf("completeOpenAICompatSync returned error: %v", err)
	}
	if content != "ok" || attempts != 2 {
		t.Fatalf("content = %q, attempts = %d; want ok and 2", content, attempts)
	}
}
