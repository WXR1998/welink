package main

import (
	"testing"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/channel/types"
)

func TestSessionKeyFor_GroupIsolation(t *testing.T) {
	a := &types.NormalizedMessage{ChatType: "group", ChatID: "oc_a", UserID: "user_a"}
	b := &types.NormalizedMessage{ChatType: "group", ChatID: "oc_a", UserID: "user_b"}
	if sessionKeyFor(a) == sessionKeyFor(b) {
		t.Fatalf("group sessions should be isolated per user: %q == %q", sessionKeyFor(a), sessionKeyFor(b))
	}
	p := &types.NormalizedMessage{ChatType: "p2p", ChatID: "oc_a", UserID: "user_a"}
	if sessionKeyFor(a) == sessionKeyFor(p) {
		t.Fatalf("p2p should differ from group key")
	}
}

func TestBusyLockRejectsConcurrent(t *testing.T) {
	b := &bot{cfg: &Config{}, sessions: map[string]*session{}}
	key := "p2p:user_a"
	if !b.acquireBusy(key) {
		t.Fatalf("expected first acquire to succeed")
	}
	if b.acquireBusy(key) {
		t.Fatalf("expected second acquire to be rejected while busy")
	}
	b.releaseBusy(key)
	if !b.acquireBusy(key) {
		t.Fatalf("expected acquire after release to succeed")
	}
	b.releaseBusy(key)
}

func TestSessionExpiresAfterIdle(t *testing.T) {
	b := &bot{cfg: &Config{}, sessions: map[string]*session{}}
	key := "p2p:user_b"
	b.remember(key, "问题1", "回答1")
	if len(b.historyOf(key)) != 2 {
		t.Fatalf("expected history length 2, got %d", len(b.historyOf(key)))
	}
	// 模拟已空闲超过 2 小时
	b.mu.Lock()
	b.sessions[key].lastActive = time.Now().Add(-(sessionIdleTTL + time.Minute))
	b.mu.Unlock()
	if len(b.historyOf(key)) != 0 {
		t.Fatalf("expected history reset after idle, got %d", len(b.historyOf(key)))
	}
}

func TestBuildDataContextIncludesFacts(t *testing.T) {
	d := &memorySearchData{
		Facts: []memFact{{Fact: "张三上月聊过旅行", ContactKey: "contact:zhangsan"}},
	}
	ctx := buildDataContext(d)
	if ctx == "" || !contains(ctx, "张三上月聊过旅行") {
		t.Fatalf("buildDataContext missing facts: %q", ctx)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestCleanMention(t *testing.T) {
	if got := cleanMention("@_user_1 我和张三聊了什么？"); got != "我和张三聊了什么？" {
		t.Errorf("cleanMention got %q", got)
	}
}
