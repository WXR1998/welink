package main

import (
	"context"
	"encoding/json"
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
		assertUsesBackendDefaultProfile(t, r)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"delta\":\"答案第一部分\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"，第二部分\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"done\":true}\n\n"))
	}))
	defer server.Close()

	cfg := &Config{WeLinkBaseURL: server.URL, WeLinkToken: "tok"}
	var deltas []string
	answer, _, err := analyzeQuestion(context.Background(), cfg, "", "你好", "feishu:p2p:u", nil, "", func(delta string) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("analyzeQuestion returned error: %v", err)
	}
	if answer != "答案第一部分，第二部分" {
		t.Errorf("unexpected answer: %q", answer)
	}
	if got := strings.Join(deltas, ""); got != answer {
		t.Errorf("streamed deltas = %q, want %q", got, answer)
	}
}

func TestAnswerStreamBufferUpdatesEveryTwentyRunes(t *testing.T) {
	buf := newAnswerStreamBuffer(20)
	first := strings.Repeat("你", 19)
	if _, ready := buf.Append(first); ready {
		t.Fatal("must not update before 20 runes")
	}
	if got, ready := buf.Append("好"); !ready || got != first+"好" {
		t.Fatalf("first update = (%q, %v), want 20-rune answer and true", got, ready)
	}
	second := strings.Repeat("啊", 19)
	if _, ready := buf.Append(second); ready {
		t.Fatal("must wait for another 20 runes after an update")
	}
	if got, ready := buf.Append("！"); !ready || got != first+"好"+second+"！" {
		t.Fatalf("second update = (%q, %v), want all accumulated text and true", got, ready)
	}
}

func TestAnalyzeQuestion_PropagatesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"error\":\"生成失败\"}\n\n"))
	}))
	defer server.Close()

	cfg := &Config{WeLinkBaseURL: server.URL}
	_, _, err := analyzeQuestion(context.Background(), cfg, "", "你好", "feishu:p2p:u", nil, "", nil)
	if err == nil || !strings.Contains(err.Error(), "生成失败") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestMemorySearch_ParsesResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ai/memory-search" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		assertUsesBackendDefaultProfile(t, r)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"progress\",\"step\":\"decompose\",\"detail\":\"x\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"result\",\"data\":{\"facts\":[{\"fact\":\"张三上月聊过旅行\",\"contact_key\":\"contact:zhangsan\"}],\"decomposition\":{\"needs_memory\":true}}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n\n"))
	}))
	defer server.Close()

	cfg := &Config{WeLinkBaseURL: server.URL}
	var steps []string
	d, err := memorySearch(context.Background(), cfg, "旅行", "feishu:p2p:u", "", false,
		func(step, detail string) {
			steps = append(steps, step)
		},
		func(names []string) {},
	)
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

func assertUsesBackendDefaultProfile(t *testing.T, r *http.Request) {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	for _, key := range []string{"profile_id", "model"} {
		if _, exists := payload[key]; exists {
			t.Errorf("request must use the backend current profile, not send %q: %s", key, payload[key])
		}
	}
}

func TestMemorySearch_AbortsOnEntityNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"progress\",\"step\":\"resolve_entities\",\"detail\":\"解析实体名: 邓凯文\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"progress\",\"step\":\"resolve_entities\",\"detail\":\"实体解析结果: 未命中\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n\n"))
	}))
	defer server.Close()

	cfg := &Config{WeLinkBaseURL: server.URL}
	_, err := memorySearch(context.Background(), cfg, "我和邓凯文最近聊了什么？", "feishu:smoke", "", false,
		func(step, detail string) {},
		func(names []string) {},
	)
	if err != errEntityNotFound {
		t.Fatalf("expected errEntityNotFound, got %v", err)
	}
}

func TestMemorySearch_ProceedsOnEntityHit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"progress\",\"step\":\"resolve_entities\",\"detail\":\"解析实体名: 邓凯文\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"progress\",\"step\":\"resolve_entities\",\"detail\":\"实体解析结果: 已命中\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"result\",\"data\":{\"facts\":[],\"resolved_entities\":[{\"name\":\"邓凯文\",\"contact_key\":\"contact:wxid_dkw\",\"display_name\":\"邓凯文\"}]}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n\n"))
	}))
	defer server.Close()

	cfg := &Config{WeLinkBaseURL: server.URL}
	d, err := memorySearch(context.Background(), cfg, "我和邓凯文最近聊了什么？", "feishu:smoke", "", false,
		func(step, detail string) {},
		func(names []string) {},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d == nil || len(d.ResolvedEntities) != 1 {
		t.Fatalf("unexpected result: %+v", d)
	}
}

func TestAnalyzeQuestionUsesCrossQAAnswerTemplate(t *testing.T) {
	var got analyzeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"done\":true}\n\n"))
	}))
	defer server.Close()

	cfg := &Config{WeLinkBaseURL: server.URL}
	_, _, err := analyzeQuestion(context.Background(), cfg, "", "你好", "feishu:p2p:u", nil, "", nil)
	if err != nil {
		t.Fatalf("analyzeQuestion returned error: %v", err)
	}
	if got.PromptTemplate != "cross_qa_answer" {
		t.Fatalf("expected cross_qa_answer prompt template, got %q", got.PromptTemplate)
	}
	for _, message := range got.Messages {
		if message.Role == "system" {
			t.Fatalf("final answer prompt must be injected by backend, got system message: %+v", got.Messages)
		}
	}
}

func TestFormatAnswerRunMeta(t *testing.T) {
	meta := answerRunMeta{
		Models: qaStepModels{
			QueryDecomposition: "glm-5.2",
			QueryExpansion:     "gpt-5.6-terra",
			FinalAnswer:        "gpt-5.6-terra",
		},
		Decomposition: &queryDecomposition{
			Entities: []string{"张三"},
			Concepts: []string{"旅行"},
			TimeFrom: "2026-07-01",
			TimeTo:   "2026-08-09",
		},
		ExpandedQueries: []string{"张三旅行计划", "张三旅行时间"},
	}

	got := formatAnswerRunMeta(meta)
	for _, want := range []string{
		"> **模型**",
		"| 步骤 | 模型 |",
		"| 问题分解 | `glm-5.2` |",
		"| 查询扩展 | `gpt-5.6-terra` |",
		"| 最终回答 | `gpt-5.6-terra` |",
		"> **问题分解**",
		"| 维度 | 结果 |",
		"| 实体 | 张三 |",
		"| 概念 | 旅行 |",
		"| 时间 | 2026-07-01 ~ 2026-08-09 |",
		"> **查询扩展**",
		"| 序号 | 查询 |",
		"| 1 | 张三旅行计划 |",
		"| 2 | 张三旅行时间 |",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("meta missing %q: %s", want, got)
		}
	}
}

func TestFormatAnswerRunMetaEscapesTableCells(t *testing.T) {
	got := formatAnswerRunMeta(answerRunMeta{
		Models: qaStepModels{FinalAnswer: "gpt-5.6"},
		ExpandedQueries: []string{
			"",
			"张三 | 旅行\n时间\r地点",
		},
	})

	for _, want := range []string{
		"| 问题分解 | - |",
		"| 查询扩展 | - |",
		"| 最终回答 | `gpt-5.6` |",
		"| 1 | 张三 \\| 旅行<br>时间<br>地点 |",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("meta missing %q: %s", want, got)
		}
	}
}

func TestAnalyzeQuestionSendsHistoryAsConversationMessages(t *testing.T) {
	var got analyzeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"done\":true}\n\n"))
	}))
	defer server.Close()

	history := []llmMessage{
		{Role: "user", Content: "前一个问题"},
		{Role: "assistant", Content: "前一个回答"},
	}
	cfg := &Config{WeLinkBaseURL: server.URL}
	if _, _, err := analyzeQuestion(context.Background(), cfg, "", "当前问题", "feishu:p2p:u", history, "检索内容", nil); err != nil {
		t.Fatalf("analyzeQuestion returned error: %v", err)
	}

	if got.PromptTemplate != "cross_qa_answer" {
		t.Fatalf("expected cross_qa_answer prompt template, got %q", got.PromptTemplate)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("expected history pair and current question; got %+v", got.Messages)
	}
	if got.Messages[0] != history[0] || got.Messages[1] != history[1] {
		t.Fatalf("history must retain message roles: %+v", got.Messages)
	}
	if got.Messages[2].Role != "user" || got.Messages[2].Content != "问题：当前问题\n\n检索内容" {
		t.Fatalf("current question must be the final user message: %+v", got.Messages[2])
	}
}
