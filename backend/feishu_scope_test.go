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

func TestApplyFeishuScope_GroupOnlyAllowsAllContacts(t *testing.T) {
	prefs := Preferences{
		FeishuGroupScope: map[string][]string{
			"oc_group1": {"group:群A"},
		},
	}
	keys := []string{"group:群A", "group:群B", "contact:zhangsan", "contact:lisi"}
	got := applyFeishuScope(keys, "oc_group1", prefs)
	want := map[string]bool{
		"group:群A":          true,
		"contact:zhangsan": true,
		"contact:lisi":     true,
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 keys (group:群A + all contacts), got %v", got)
	}
	for _, k := range got {
		if !want[k] {
			t.Fatalf("unexpected key %q", k)
		}
	}
}

func TestApplyFeishuScope_ContactOnlyAllowsAllGroups(t *testing.T) {
	prefs := Preferences{
		FeishuGroupScope: map[string][]string{
			"oc_group1": {"contact:zhangsan"},
		},
	}
	keys := []string{"group:群A", "group:群B", "contact:zhangsan", "contact:lisi"}
	got := applyFeishuScope(keys, "oc_group1", prefs)
	want := map[string]bool{
		"group:群A":          true,
		"group:群B":          true,
		"contact:zhangsan": true,
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 keys (all groups + contact:zhangsan), got %v", got)
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

func TestFilterMemFactsByScope_GroupOnlyKeepsContactFacts(t *testing.T) {
	prefs := Preferences{
		FeishuGroupScope: map[string][]string{
			"oc_group1": {"group:群A"},
		},
	}
	facts := []MemFact{
		{ContactKey: "group:群A", Fact: "f1"},
		{ContactKey: "group:群B", Fact: "f3"},
		{ContactKey: "contact:lisi", Fact: "f2"},
	}
	got := filterMemFactsByScope(facts, "oc_group1", prefs)
	// group:群A 在白名单 → 保留
	// group:群B 不在白名单且群白名单已配置 → 过滤
	// contact:lisi 不在白名单但私聊白名单未配置 → 保留
	if len(got) != 2 {
		t.Fatalf("expected 2 facts, got %+v", got)
	}
	seen := map[string]bool{}
	for _, f := range got {
		seen[f.ContactKey] = true
	}
	if !seen["group:群A"] || !seen["contact:lisi"] {
		t.Fatalf("expected group:群A + contact:lisi, got %+v", got)
	}
}
