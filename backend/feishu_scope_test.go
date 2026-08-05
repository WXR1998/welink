package main

import "testing"

func TestApplyFeishuScope_WhiteList(t *testing.T) {
	prefs := Preferences{
		FeishuGroupScope: map[string][]string{
			"oc_group1": {"group:群A", "contact:zhangsan"},
		},
	}
	keys := []string{"group:群A", "contact:zhangsan", "group:群B", "contact:lisi"}
	got := applyFeishuScope(keys, "oc_group1", prefs)
	want := map[string]bool{"group:群A": true, "contact:zhangsan": true}
	if len(got) != 2 {
		t.Fatalf("expected 2 keys, got %v", got)
	}
	for _, k := range got {
		if !want[k] {
			t.Fatalf("unexpected key %q", k)
		}
	}
}

func TestApplyFeishuScope_NoChatOrUnconfiguredMeansAllowAll(t *testing.T) {
	prefs := Preferences{
		FeishuGroupScope: map[string][]string{
			"oc_group1": {"group:群A"},
		},
	}
	keys := []string{"group:群A", "contact:lisi"}
	// 未传 chat_id → 全放行
	if got := applyFeishuScope(keys, "", prefs); len(got) != 2 {
		t.Fatalf("expected allow-all without chat_id, got %v", got)
	}
	// 没配置该群的 chat_id → 全放行
	if got := applyFeishuScope(keys, "oc_unknown", prefs); len(got) != 2 {
		t.Fatalf("expected allow-all for unconfigured chat_id, got %v", got)
	}
}

func TestFilterMemFactsByScope(t *testing.T) {
	prefs := Preferences{
		FeishuGroupScope: map[string][]string{
			"oc_group1": {"group:群A"},
		},
	}
	facts := []MemFact{
		{ContactKey: "group:群A", Fact: "f1"},
		{ContactKey: "contact:lisi", Fact: "f2"},
	}
	got := filterMemFactsByScope(facts, "oc_group1", prefs)
	if len(got) != 1 || got[0].ContactKey != "group:群A" {
		t.Fatalf("expected only group:群A, got %+v", got)
	}
}
