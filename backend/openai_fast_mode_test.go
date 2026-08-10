package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLLMProfileFastModeAppliesToOpenAICompatibleRequests(t *testing.T) {
	prefs := Preferences{
		DefaultLLMProfileID: "openai",
		LLMProfiles: []LLMProfile{
			{ID: "openai", Provider: "openai", Model: "gpt-5.5", FastMode: true},
			{ID: "custom-fast", Provider: "custom", Model: "gpt-5.5", FastMode: true},
			{ID: "custom-normal", Provider: "custom", Model: "gpt-5.5"},
		},
		MemLLMProfiles:         []MemLLMProfile{{ID: "memory", Provider: "openai", Model: "gpt-5.5", FastMode: true}},
		DefaultMemLLMProfileID: "memory",
	}

	openAIConfig := llmConfigForProfile("openai", prefs)
	if !openAIConfig.fastMode {
		t.Fatal("profile fast mode was not propagated to the LLM config")
	}
	if got := buildOpenAICompatRequest([]LLMMessage{{Role: "user", Content: "Hi"}}, openAIConfig, true).ServiceTier; got != "priority" {
		t.Fatalf("Chat Completions service_tier = %q, want priority", got)
	}
	if got := buildOpenAIResponsesRequest([]LLMMessage{{Role: "user", Content: "Hi"}}, openAIConfig, true).ServiceTier; got != "priority" {
		t.Fatalf("Responses service_tier = %q, want priority", got)
	}

	customConfig := llmConfigForProfile("custom-fast", prefs)
	if got := buildOpenAICompatRequest([]LLMMessage{{Role: "user", Content: "Hi"}}, customConfig, true).ServiceTier; got != "priority" {
		t.Fatalf("custom Chat Completions service_tier = %q, want priority", got)
	}
	if got := buildOpenAIResponsesRequest([]LLMMessage{{Role: "user", Content: "Hi"}}, customConfig, true).ServiceTier; got != "priority" {
		t.Fatalf("custom Responses service_tier = %q, want priority", got)
	}

	memoryConfigs := memLLMConfigs(prefs)
	if len(memoryConfigs) != 1 || !memoryConfigs[0].fastMode {
		t.Fatalf("memory extraction config did not inherit OpenAI fast mode: %+v", memoryConfigs)
	}

	normalConfig := llmConfigForProfile("custom-normal", prefs)
	if got := buildOpenAICompatRequest([]LLMMessage{{Role: "user", Content: "Hi"}}, normalConfig, true).ServiceTier; got != "" {
		t.Fatalf("normal custom profile service_tier = %q, want empty", got)
	}
}

func TestDecodePreferencesMigratesLegacyGlobalFastModeToProfiles(t *testing.T) {
	prefs, err := decodePreferences([]byte(`{
		"schema_version": 5,
		"openai_fast_mode": true,
		"llm_profiles": [
			{"id":"openai","provider":"openai"},
			{"id":"custom","provider":"custom"},
			{"id":"deepseek","provider":"deepseek"}
		],
		"mem_llm_profiles": [{"id":"memory","provider":"custom"}]
	}`))
	if err != nil {
		t.Fatalf("decode preferences: %v", err)
	}
	if !prefs.LLMProfiles[0].FastMode || !prefs.LLMProfiles[1].FastMode {
		t.Fatalf("supported LLM profiles did not inherit legacy Fast mode: %+v", prefs.LLMProfiles)
	}
	if prefs.LLMProfiles[2].FastMode {
		t.Fatalf("unsupported LLM profile inherited legacy Fast mode: %+v", prefs.LLMProfiles[2])
	}
	if len(prefs.MemLLMProfiles) != 1 || !prefs.MemLLMProfiles[0].FastMode {
		t.Fatalf("memory profile did not inherit legacy Fast mode: %+v", prefs.MemLLMProfiles)
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
			if request.ServiceTier != "priority" {
				t.Fatalf("first service_tier = %q, want priority", request.ServiceTier)
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
		provider: "custom",
		apiKey:   "test-key",
		baseURL:  server.URL,
		model:    "test-model",
		fastMode: true,
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
			if request.ServiceTier != "priority" {
				t.Fatalf("first service_tier = %q, want priority", request.ServiceTier)
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
		fastMode:        true,
	})
	if err != nil {
		t.Fatalf("completeOpenAICompatSync returned error: %v", err)
	}
	if content != "ok" || attempts != 2 {
		t.Fatalf("content = %q, attempts = %d; want ok and 2", content, attempts)
	}
}
