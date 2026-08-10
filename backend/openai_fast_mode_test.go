package main

import "testing"

func TestOpenAIFastModeOnlyAppliesToNativeOpenAIRequests(t *testing.T) {
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
	if got := buildOpenAICompatRequest([]LLMMessage{{Role: "user", Content: "Hi"}}, customConfig, true).ServiceTier; got != "" {
		t.Fatalf("custom Chat Completions service_tier = %q, want empty", got)
	}
	if got := buildOpenAIResponsesRequest([]LLMMessage{{Role: "user", Content: "Hi"}}, customConfig, true).ServiceTier; got != "" {
		t.Fatalf("custom Responses service_tier = %q, want empty", got)
	}

	memoryConfigs := memLLMConfigs(prefs)
	if len(memoryConfigs) != 1 || !memoryConfigs[0].openAIFastMode {
		t.Fatalf("memory extraction config did not inherit OpenAI fast mode: %+v", memoryConfigs)
	}
}
