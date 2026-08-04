package main

import (
	"testing"

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

func TestKeyAllowed(t *testing.T) {
	b := &bot{cfg: &Config{AllowedKeys: []string{"contact:alice", "group:闲聊"}}}
	if !b.keyAllowed("contact:alice") {
		t.Errorf("expected contact:alice allowed")
	}
	if b.keyAllowed("contact:bob") {
		t.Errorf("expected contact:bob rejected")
	}

	b2 := &bot{cfg: &Config{AllowedKeys: nil}}
	if !b2.keyAllowed("anything") {
		t.Errorf("empty allowlist should allow all")
	}
}

func TestCleanMention(t *testing.T) {
	if got := cleanMention("@_user_1 我和张三聊了什么？"); got != "我和张三聊了什么？" {
		t.Errorf("cleanMention got %q", got)
	}
}
