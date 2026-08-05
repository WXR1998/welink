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
		"group:群A":         true,
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
		"group:群A":         true,
		"group:群B":         true,
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

func TestInjectFeishuGroupPrompt(t *testing.T) {
	prefs := Preferences{
		FeishuGroupPrompts: map[string]string{
			"oc_g1": "本群成员主要使用粤语交流。",
		},
	}

	// 已有 system 消息 → 补充提示作为第 7 点插在第 6 点（支持论据要求）之后
	msgs := []LLMMessage{{Role: "system", Content: "要求：\n6. 每段故事都要说明依据的聊天记录原文，直到能完整表达该事件为止。\n下面是正文"}, {Role: "user", Content: "hi"}}
	got := injectFeishuGroupPrompt(msgs, "oc_g1", prefs)
	if len(got) != 2 {
		t.Fatalf("expected length 2, got %d", len(got))
	}
	want := "要求：\n6. 每段故事都要说明依据的聊天记录原文，直到能完整表达该事件为止。\n7. 本群成员主要使用粤语交流。\n下面是正文"
	if got[0].Content != want {
		t.Fatalf("unexpected system content: %q", got[0].Content)
	}

	// 无固定开场白 → 回退追加到 system 末尾
	msgs3 := []LLMMessage{{Role: "system", Content: "base"}, {Role: "user", Content: "hi"}}
	got3 := injectFeishuGroupPrompt(msgs3, "oc_g1", prefs)
	if got3[0].Content != "base\n\n7. 本群成员主要使用粤语交流。\n" {
		t.Fatalf("unexpected fallback system content: %q", got3[0].Content)
	}

	// 无 system 消息 → 插入 system
	msgs2 := []LLMMessage{{Role: "user", Content: "hi"}}
	got2 := injectFeishuGroupPrompt(msgs2, "oc_g1", prefs)
	if len(got2) != 2 || got2[1].Role != "system" {
		t.Fatalf("expected appended system message, got %+v", got2)
	}

	// 未配置 / chat_id 为空 / 提示为空 → 原样返回
	if out := injectFeishuGroupPrompt(msgs, "", prefs); len(out) != 2 {
		t.Fatalf("empty chat_id should be no-op")
	}
	empty := Preferences{FeishuGroupPrompts: map[string]string{"oc_g1": "   "}}
	if out := injectFeishuGroupPrompt(msgs, "oc_g1", empty); len(out) != 2 {
		t.Fatalf("blank prompt should be no-op")
	}
}
