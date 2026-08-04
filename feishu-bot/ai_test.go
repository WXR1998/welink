package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAskRAG_AccumulatesSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ai/rag" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing authorization header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"rag_meta\":{\"hits\":2,\"retrieved\":3}}\n\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"答案第一部分\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"，第二部分\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"done\":true}\n\n"))
	}))
	defer server.Close()

	cfg := &Config{
		WeLinkBaseURL: server.URL,
		WeLinkToken:   "tok",
		DefaultKey:    "contact:alice",
	}
	out, err := askRAG(context.Background(), cfg, "contact:alice", "你好", "")
	if err != nil {
		t.Fatalf("askRAG returned error: %v", err)
	}
	if out.Answer != "答案第一部分，第二部分" {
		t.Errorf("unexpected answer: %q", out.Answer)
	}
	if out.Hits != 2 || out.Retrieved != 3 {
		t.Errorf("unexpected meta: %+v", out)
	}
}

func TestAskRAG_PropagatesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"error\":\"检索失败\"}\n\n"))
	}))
	defer server.Close()

	cfg := &Config{WeLinkBaseURL: server.URL, DefaultKey: "contact:alice"}
	out, err := askRAG(context.Background(), cfg, "contact:alice", "你好", "")
	if err != nil {
		t.Fatalf("askRAG returned error: %v", err)
	}
	if out.Error != "检索失败" {
		t.Errorf("expected error, got %+v", out)
	}
}

func TestAskRAG_MissingKey(t *testing.T) {
	cfg := &Config{WeLinkBaseURL: "http://x"}
	_, err := askRAG(context.Background(), cfg, "", "你好", "")
	if err == nil || !strings.Contains(err.Error(), "DEFAULT_AI_KEY") {
		t.Fatalf("expected missing-key error, got %v", err)
	}
}
