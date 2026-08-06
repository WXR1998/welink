package main

import (
	"reflect"
	"testing"
)

func TestCollectPinnedAliases(t *testing.T) {
	pinned := []MemFact{
		{ContactKey: "contact:wxid_1", Fact: "邓皓琨 外号 DJ坤"},
		{ContactKey: "contact:wxid_2", Fact: "邓凯文 外号 dkw"},
		{ContactKey: "group:g1", Fact: "群聊内容"},
		{ContactKey: "contact:wxid_1", Fact: "重复联系人"},
	}
	all := map[string][]string{
		"contact:wxid_1": {"DJ坤", "邓皓琨"},
		"contact:wxid_2": {"dkw"},
	}
	got := collectPinnedAliases(pinned, all, func(key string) string {
		m := map[string]string{"contact:wxid_1": "邓皓琨", "contact:wxid_2": "邓凯文"}
		return m[key]
	})
	want := []PinnedContactAlias{
		{ContactKey: "contact:wxid_1", DisplayName: "邓皓琨", Aliases: []string{"DJ坤", "邓皓琨"}},
		{ContactKey: "contact:wxid_2", DisplayName: "邓凯文", Aliases: []string{"dkw"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectPinnedAliases mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestCollectPinnedAliasesEmpty(t *testing.T) {
	if got := collectPinnedAliases(nil, nil, nil); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
	if got := collectPinnedAliases([]MemFact{{ContactKey: "contact:x"}}, nil, nil); got != nil {
		t.Fatalf("expected nil for empty alias map, got %v", got)
	}
}

func TestNormalizeAliasCaseInsensitive(t *testing.T) {
	cases := map[string]string{
		"DJ坤":    "dj坤",
		"dj坤":    "dj坤",
		"  DKw ": "dkw",
		"abc":    "abc",
		"ABC":    "abc",
	}
	for in, want := range cases {
		if got := normalizeAlias(in); got != want {
			t.Errorf("normalizeAlias(%q) = %q, want %q", in, got, want)
		}
	}
}
