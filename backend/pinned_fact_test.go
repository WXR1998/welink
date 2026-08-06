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
