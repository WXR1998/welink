package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestListSessionsSkipsEmptyHistory(t *testing.T) {
	b := &bot{
		sessions: map[string]*session{
			"group:oc_a:user_a": {
				history:    []llmMessage{{Role: "user", Content: "问题1"}, {Role: "assistant", Content: "回答1"}},
				createdAt:  time.Now().Add(-time.Minute),
				lastActive: time.Now(),
			},
			"group:oc_a:user_b": {
				history:    nil,
				createdAt:  time.Now(),
				lastActive: time.Now(),
			},
		},
		nameCacheAt: time.Now(),
		chatNames:   map[string]string{"oc_a": "测试群"},
		memberNames: map[string]map[string]string{"oc_a": {"user_a": "张三", "user_b": "李四"}},
	}

	req := httptest.NewRequest("GET", "/sessions", nil)
	rec := httptest.NewRecorder()
	b.listSessions(rec, req)

	if rec.Code != 200 {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	var body struct {
		Sessions []manageSession `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Sessions) != 1 {
		t.Fatalf("expected 1 non-empty session, got %d: %s", len(body.Sessions), rec.Body.String())
	}
	if body.Sessions[0].Key != "group:oc_a:user_a" {
		t.Fatalf("expected only user_a session, got %q", body.Sessions[0].Key)
	}
}
