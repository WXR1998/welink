package main

import (
	"strings"
	"testing"
)

func TestPinnedFactLineFallsBackToUsername(t *testing.T) {
	line := pinnedFactLine(MemFact{
		ContactKey: "contact:extra_1785995225704386105_0",
		Fact:       "邓凯文曾经舔过的对象",
	}, nil)
	// svc==nil 时无法解析展示名，应退化为直接列出事实，且不丢内容。
	if !strings.Contains(line, "邓凯文曾经舔过的对象") {
		t.Fatalf("fact content missing: %q", line)
	}
}

func TestPinnedFactLinePrefixesDisplayName(t *testing.T) {
	line := pinnedFactLine(MemFact{
		ContactKey:  "contact:extra_1",
		DisplayName: "丁舒",
		Fact:        "邓凯文曾经舔过的对象",
	}, nil)
	if !strings.Contains(line, "丁舒：邓凯文曾经舔过的对象") {
		t.Fatalf("expected subject prefix, got: %q", line)
	}
}

func TestPinnedFactLineForBuildPrefixesSourceName(t *testing.T) {
	line := pinnedFactLineForBuild(MemFact{
		SourceName: "与「邓凯文」的私聊",
		Fact:       "邓凯文曾经舔过的对象",
	})
	if !strings.Contains(line, "与「邓凯文」的私聊：邓凯文曾经舔过的对象") {
		t.Fatalf("expected subject from source name, got: %q", line)
	}
}
