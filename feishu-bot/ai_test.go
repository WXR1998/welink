package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnalyzeQuestion_AccumulatesSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ai/analyze" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing authorization header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"delta\":\"答案第一部分\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"，第二部分\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"done\":true}\n\n"))
	}))
	defer server.Close()

	cfg := &Config{WeLinkBaseURL: server.URL, WeLinkToken: "tok"}
	answer, err := analyzeQuestion(context.Background(), cfg, "你好", "feishu:p2p:u", nil, "")
	if err != nil {
		t.Fatalf("analyzeQuestion returned error: %v", err)
	}
	if answer != "答案第一部分，第二部分" {
		t.Errorf("unexpected answer: %q", answer)
	}
}

func TestAnalyzeQuestion_PropagatesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"error\":\"生成失败\"}\n\n"))
	}))
	defer server.Close()

	cfg := &Config{WeLinkBaseURL: server.URL}
	_, err := analyzeQuestion(context.Background(), cfg, "你好", "feishu:p2p:u", nil, "")
	if err == nil || !strings.Contains(err.Error(), "生成失败") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestMemorySearch_ParsesResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ai/memory-search" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"progress\",\"step\":\"decompose\",\"detail\":\"x\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"result\",\"data\":{\"facts\":[{\"fact\":\"张三上月聊过旅行\",\"contact_key\":\"contact:zhangsan\"}],\"decomposition\":{\"needs_memory\":true}}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n\n"))
	}))
	defer server.Close()

	cfg := &Config{WeLinkBaseURL: server.URL}
	var steps []string
	d, err := memorySearch(context.Background(), cfg, "旅行", "feishu:p2p:u", func(step, detail string) {
		steps = append(steps, step)
	})
	if err != nil {
		t.Fatalf("memorySearch returned error: %v", err)
	}
	if d == nil || len(d.Facts) != 1 || d.Facts[0].Fact != "张三上月聊过旅行" {
		t.Fatalf("unexpected result: %+v", d)
	}
	if len(steps) == 0 || steps[0] != "decompose" {
		t.Fatalf("expected progress callback, got %v", steps)
	}
}
