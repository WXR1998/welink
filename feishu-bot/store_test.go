package main

import (
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
