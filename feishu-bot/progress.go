package main

import (
	"fmt"
	"regexp"
	"sync"
)

// progressTracker 将后端 memory-search 的进度事件转换成单调递增的
// (current, total)，避免卡片进度条回退。
//
// 步数模型 = 群聊数 N + 扩展子查询数 M + 固定尾部大步骤 fixedTail。
// - 每个群的一次完整三路检索（向量/BM25/原始消息）按 1 个群步计；
// - 每个扩展子查询按 1 个子查询步计；
// - 其余大阶段（decompose、resolve_entities、comention、rrf、rerank、
//   extract_sources、analyze）合计 fixedTail 步。
// 群数 N 与子查询数 M 从 detail 的 [k/N] / [s/M] 动态学习。
type progressTracker struct {
	mu sync.Mutex

	groupTotal int
	groupCur   int
	seenGroup  bool

	subTotal int
	subCur   int
	seenSub  bool

	headDone bool // 检索前的准备阶段是否已推进

	// 已出现并计数的固定尾部步骤
	tailSeen map[string]bool

	// 对外输出过的值，保证单调不回退
	lastCurrent int
	lastTotal   int
}

// fixedTail 是除群检索、子查询外的固定大步骤数。
const fixedTail = 7

// headSteps 是固定头部准备步骤（群检索、子查询开始前）。
var headSteps = []string{"decompose", "resolve_entities"}

var fractionRe = regexp.MustCompile(`\[\s*(\d+)\s*/\s*(\d+)\s*\]`)

func newProgressTracker() *progressTracker {
	return &progressTracker{tailSeen: map[string]bool{}}
}

// Observe 处理一条进度事件，返回当前单调进度 (current, total)。
func (t *progressTracker) Observe(step, detail string) (int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// 群检索类步骤：学习群数并按 key 递增
	if step == "vector_search" || step == "bm25_search" || step == "vecmsg_search" {
		if k, n, ok := parseFraction(detail); ok {
			t.groupTotal = n
			t.seenGroup = true
			if k > t.groupCur {
				t.groupCur = k // k 即当前完成到第几个群
			}
		}
	}
	// 扩展子查询：学习子查询数并按序递增
	if step == "expanded_search" {
		if s, m, ok := parseFraction(detail); ok {
			t.subTotal = m
			t.seenSub = true
			if s > t.subCur {
				t.subCur = s
			}
		}
	}
	// 头部准备步骤完成
	if containsStep(headSteps, step) {
		t.headDone = true
	}
	// 固定尾部步骤去重计数
	if containsStep(tailStepsEstimate, step) && !t.tailSeen[step] {
		t.tailSeen[step] = true
	}

	// 已确定的固定尾部步数（最多 fixedTail）
	tailN := len(t.tailSeen)
	if tailN > fixedTail {
		tailN = fixedTail
	}

	N := t.groupTotal
	M := t.subTotal
	total := N + M + fixedTail
	if total <= 0 {
		total = 5
	}

	// current：已做 group 步 + 已做 sub 步 + 头部已做 + 尾部已做
	current := t.groupCur + t.subCur
	if t.headDone {
		current += 1
	}
	current += tailN
	if current > total {
		current = total
	}

	// 单调性保护：只升不降
	if current < t.lastCurrent {
		current = t.lastCurrent
	}
	if total < t.lastTotal {
		total = t.lastTotal
	}
	if current > total {
		current = total
	}

	t.lastCurrent = current
	t.lastTotal = total
	return current, total
}

// tailStepsEstimate 是固定尾部大步骤。
var tailStepsEstimate = []string{
	"comention_search", "rrf_fusion", "rerank",
	"extract_sources", "expand_done", "analyze", "answer",
}

func parseFraction(detail string) (pos, total int, ok bool) {
	m := fractionRe.FindStringSubmatch(detail)
	if m == nil {
		return 0, 0, false
	}
	fmt.Sscanf(m[1], "%d", &pos)
	fmt.Sscanf(m[2], "%d", &total)
	return pos, total, total > 0
}

func containsStep(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
