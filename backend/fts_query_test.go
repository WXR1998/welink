package main

import (
	"strings"
	"testing"
)

func TestPrepareFTSQueryKeepsRestoredRealName(t *testing.T) {
	// 查询扩展把外号还原为真实姓名后，真实姓名必须保留为 FTS 词项，
	// 否则 BM25 无法命中那些直接提到真实姓名的记忆事实。
	fts, like := prepareFTSQuery("李佳轩的群友风评如何")
	if !strings.Contains(fts, "李佳轩") {
		t.Fatalf("expected restored real name in FTS terms, got fts=%q like=%v", fts, like)
	}
	if len(like) != 0 {
		t.Fatalf("expected no LIKE terms for this query, got %v", like)
	}
}
