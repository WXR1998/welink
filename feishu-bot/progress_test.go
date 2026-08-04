package main

import "testing"

func TestProgressTrackerMonotonic(t *testing.T) {
	tr := newProgressTracker()
	prevCur, prevTotal := 0, 1

	// 模拟后端真实顺序
	events := []struct{ step, detail string }{
		{"decompose", "正在用 LLM 分解问题..."},
		{"resolve_entities", "解析实体名: 邓凯文"},
		{"vector_search", "[1/3] 向量检索 (concepts: 故事)"},
		{"bm25_search", "[1/3] BM25 关键词检索 (query: 故事)"},
		{"vecmsg_search", "[1/3] 原始消息向量检索 (query: 故事)"},
		{"vector_search", "[2/3] 向量检索 (concepts: 故事)"},
		{"bm25_search", "[2/3] BM25 关键词检索 (query: 故事)"},
		{"vecmsg_search", "[2/3] 原始消息向量检索 (query: 故事)"},
		{"vector_search", "[3/3] 向量检索 (concepts: 故事)"},
		{"expanded_search", "[1/2] 扩展子查询检索: 95式步枪"},
		{"expanded_search", "[2/2] 扩展子查询检索: 95后群体"},
		{"comention_search", "实体×概念共现检索（全局）"},
		{"rrf_fusion", "RRF 融合 100 条向量 + 50 条BM25 候选..."},
		{"extract_sources", "从 50 条记忆事实中提取源聊天记录..."},
	}

	var lastCur int
	for _, e := range events {
		cur, total := tr.Observe(e.step, e.detail)
		if cur < prevCur {
			t.Fatalf("progress regressed: %d -> %d on %q", prevCur, cur, e.step)
		}
		if total < prevTotal {
			t.Fatalf("total regressed: %d -> %d on %q", prevTotal, total, e.step)
		}
		if cur > total {
			t.Fatalf("current %d > total %d on %q", cur, total, e.step)
		}
		prevCur, prevTotal = cur, total
		lastCur = cur
		_ = lastCur
	}
}

func TestProgressTrackerFinalWithinTotal(t *testing.T) {
	tr := newProgressTracker()
	// 3 群 + 2 子查询
	tr.Observe("vector_search", "[3/3] 向量检索")
	tr.Observe("expanded_search", "[2/2] 扩展子查询")
	tr.Observe("answer", "")
	cur, total := tr.Observe("answer", "")
	if cur > total {
		t.Fatalf("current %d exceeds total %d", cur, total)
	}
	if total < 3+2+1 {
		t.Fatalf("total %d too small", total)
	}
}
