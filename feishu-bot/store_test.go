package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	st := newSessionStore(path)
	in := map[string]*session{
		"p2p:user_a": {
			history: []llmMessage{
				{Role: "user", Content: "q1"},
				{Role: "assistant", Content: "a1"},
			},
			lastActive: time.Now(),
			version:    3,
			entities:   []string{"邓凯文"},
		},
	}
	if err := st.Save(in); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	st2 := newSessionStore(path)
	out, err := st2.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	s, ok := out["p2p:user_a"]
	if !ok {
		t.Fatalf("missing session after reload")
	}
	if len(s.history) != 2 || s.history[0].Content != "q1" || s.history[1].Content != "a1" {
		t.Fatalf("history not preserved: %+v", s.history)
	}
	if s.version != 3 {
		t.Fatalf("version not preserved: %d", s.version)
	}
	if len(s.entities) != 1 || s.entities[0] != "邓凯文" {
		t.Fatalf("entities not preserved: %v", s.entities)
	}
	if s.lastActive.IsZero() {
		t.Fatalf("lastActive not preserved")
	}
}

func TestSessionStoreEmptyPathNoop(t *testing.T) {
	st := newSessionStore("")
	in := map[string]*session{"p2p:user_a": {history: []llmMessage{{Role: "user", Content: "q"}}}}
	if err := st.Save(in); err != nil {
		t.Fatalf("Save with empty path should be no-op, got %v", err)
	}
	out, err := st.Load()
	if err != nil {
		t.Fatalf("Load with empty path should be no-op, got %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty, got %d", len(out))
	}
}


func TestPendingStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	p := newPendingStore(path)
	items := map[string]pendingItem{
		"om_x100": {
			MessageID:  "om_x100",
			ChatID:     "oc_abc",
			SessionKey: "group:oc_abc:user1",
			Question:   "讲讲95的故事",
			CreatedAt:  time.Now(),
		},
	}
	if err := p.Save(items); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// pending.json 应与会话文件在同一目录
	if _, err := os.Stat(filepath.Join(dir, "pending.json")); err != nil {
		t.Fatalf("pending.json not found: %v", err)
	}

	p2 := newPendingStore(path)
	got, err := p2.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	it, ok := got["om_x100"]
	if !ok {
		t.Fatalf("missing pending item after reload")
	}
	if it.SessionKey != "group:oc_abc:user1" || it.Question != "讲讲95的故事" {
		t.Fatalf("pending item not preserved: %+v", it)
	}
}

func TestPendingStoreEmptyPathNoop(t *testing.T) {
	p := newPendingStore("")
	if err := p.Save(map[string]pendingItem{}); err != nil {
		t.Fatalf("Save with empty path should be no-op, got %v", err)
	}
	got, err := p.Load()
	if err != nil {
		t.Fatalf("Load with empty path should be no-op, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
}

func TestDropSessionClearsState(t *testing.T) {
	b := &bot{cfg: &Config{}, sessions: map[string]*session{}}
	b.remember("p2p:user_x", "q1", "a1")
	b.mu.Lock()
	b.sessions["p2p:user_x"].busy = true
	b.sessions["p2p:user_x"].entities = []string{"邓凯文"}
	b.mu.Unlock()

	b.dropSession("p2p:user_x")

	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions["p2p:user_x"]
	if len(s.history) != 0 {
		t.Fatalf("expected history cleared, got %d", len(s.history))
	}
	if s.busy {
		t.Fatalf("expected busy cleared")
	}
	if len(s.entities) != 0 {
		t.Fatalf("expected entities cleared")
	}
}
