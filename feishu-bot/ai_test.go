package main

import (
	"context"
	"encoding/json"
	"io"
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
	answer, _, err := analyzeQuestion(context.Background(), cfg, "", "你好", "feishu:p2p:u", nil, "")
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
	_, _, err := analyzeQuestion(context.Background(), cfg, "", "你好", "feishu:p2p:u", nil, "")
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

func TestAnalyzeQuestion_SystemIncludesEvidenceRequirement(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		gotBody = buf.String()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"done\":true}\n\n"))
	}))
	defer server.Close()

	cfg := &Config{WeLinkBaseURL: server.URL}
	_, _, err := analyzeQuestion(context.Background(), cfg, "", "你好", "feishu:p2p:u", nil, "")
	if err != nil {
		t.Fatalf("analyzeQuestion returned error: %v", err)
	}
	if !strings.Contains(gotBody, "说明其依据的聊天记录原文") || !strings.Contains(gotBody, "作为佐证") {
		t.Fatalf("system prompt missing evidence requirement, body=%s", gotBody)
	}
	if !strings.Contains(gotBody, "都不要遗漏") {
		t.Fatalf("system prompt missing exhaustive open-request requirement, body=%s", gotBody)
	}
	if !strings.Contains(gotBody, "记录之间不使用 `---` 或其他分隔线") {
		t.Fatalf("system prompt must forbid Markdown record separators, body=%s", gotBody)
	}
	if !strings.Contains(gotBody, "每行必须以 Markdown 引用标记") {
		t.Fatalf("system prompt must require quoted chat records, body=%s", gotBody)
	}
	if !strings.Contains(gotBody, "不要把聊天记录写成 Markdown 标题、表格或代码块") {
		t.Fatalf("system prompt must forbid Markdown record containers, body=%s", gotBody)
	}
	if !strings.Contains(gotBody, "聊天记录原文作为证据") || !strings.Contains(gotBody, "```text") {
		t.Fatalf("system prompt must provide a fenced raw-record example, body=%s", gotBody)
	}
	if strings.Contains(gotBody, "不同记录之间用“---”分隔") {
		t.Fatalf("system prompt must not request --- separators, body=%s", gotBody)
	}
	if !strings.Contains(gotBody, "4. 使用 Markdown 排版。") {
		t.Fatalf("system prompt missing markdown anchor line, body=%s", gotBody)
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
		},
		ExpandedQueries: []string{"张三旅行计划", "张三旅行时间"},
	}

	got := formatAnswerRunMeta(meta)
	for _, want := range []string{
		"模型：问题分解 `glm-5.2`",
		"最终回答 `gpt-5.6-terra`",
		"问题分解：实体 张三；概念 旅行",
		"查询扩展：张三旅行计划；张三旅行时间",
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
	if _, _, err := analyzeQuestion(context.Background(), cfg, "", "当前问题", "feishu:p2p:u", history, "检索内容"); err != nil {
		t.Fatalf("analyzeQuestion returned error: %v", err)
	}

	if len(got.Messages) != 4 {
		t.Fatalf("expected system, history pair, and current question; got %+v", got.Messages)
	}
	if got.Messages[0].Role != "system" || strings.Contains(got.Messages[0].Content, "前一个问题") {
		t.Fatalf("history must not be embedded in the system prompt: %+v", got.Messages[0])
	}
	if got.Messages[1] != history[0] || got.Messages[2] != history[1] {
		t.Fatalf("history must retain message roles: %+v", got.Messages)
	}
	if got.Messages[3].Role != "user" || got.Messages[3].Content != "当前问题" {
		t.Fatalf("current question must be the final user message: %+v", got.Messages[3])
	}
}
