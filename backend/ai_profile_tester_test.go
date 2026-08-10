package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTestLLMProfileRunsBothOpenAIProtocols(t *testing.T) {
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}

		switch r.URL.Path {
		case "/chat/completions":
			var request openAIRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode chat request: %v", err)
			}
			if request.Stream || request.Model != "test-model" || len(request.Messages) != 1 || request.Messages[0].Content != llmConnectionTestPrompt {
				t.Fatalf("unexpected chat request: %+v", request)
			}
			seen["chat"] = true
			fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}]}`)
		case "/responses":
			var request openAIResponsesRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode responses request: %v", err)
			}
			if !request.Stream || request.Model != "test-model" || len(request.Input) != 1 || request.Input[0].Content[0].Text != llmConnectionTestPrompt {
				t.Fatalf("unexpected responses request: %+v", request)
			}
			seen["responses"] = true
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
		provider: "custom",
		apiKey:   "test-key",
		baseURL:  server.URL,
		model:    "test-model",
	})

	if !result.OK {
		t.Fatalf("result should pass: %+v", result)
	}
	if result.Name != "测试配置" {
		t.Fatalf("profile name = %q, want 测试配置", result.Name)
	}
	if len(result.Protocols) != 2 {
		t.Fatalf("protocol count = %d, want 2", len(result.Protocols))
	}
	for _, protocol := range result.Protocols {
		if !protocol.OK {
			t.Fatalf("protocol should pass: %+v", protocol)
		}
	}
	if !seen["chat"] || !seen["responses"] {
		t.Fatalf("both protocol paths must be called, seen = %#v", seen)
	}
}

func TestTestLLMProfilePassesWhenOneOpenAIProtocolSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat/completions":
			fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}]}`)
		case "/responses":
			http.Error(w, "not supported", http.StatusNotFound)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	result := testLLMProfile("profile-1", "测试配置", llmConfig{
		provider: "custom",
		apiKey:   "test-key",
		baseURL:  server.URL,
		model:    "test-model",
	})

	if !result.OK {
		t.Fatalf("one successful protocol should make profile usable: %+v", result)
	}
	if len(result.Protocols) != 2 || !result.Protocols[0].OK || result.Protocols[1].OK {
		t.Fatalf("unexpected protocol results: %+v", result.Protocols)
	}
}
