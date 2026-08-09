package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/channel/normalize"
	"github.com/larksuite/oapi-sdk-go/v3/channel/types"
)

func TestCardStreamUpdaterKeepsOnlyLatestPendingCard(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	patched := make(chan string, 2)
	updater := newCardStreamUpdater(context.Background(), 5*time.Millisecond, func(_ context.Context, card string) {
		if card == "first" {
			close(started)
			<-release
		}
		patched <- card
	})
	defer updater.Stop()

	updater.Submit("first")
	<-started
	updater.Submit("second")
	updater.Submit("third")
	close(release)

	for _, want := range []string{"first", "third"} {
		select {
		case got := <-patched:
			if got != want {
				t.Fatalf("patched card = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %q card", want)
		}
	}
}

func TestCardStreamUpdaterPublishesAtInterval(t *testing.T) {
	patched := make(chan string, 1)
	updater := newCardStreamUpdater(context.Background(), 50*time.Millisecond, func(_ context.Context, card string) {
		patched <- card
	})
	defer updater.Stop()

	updater.Submit("latest full answer")
	select {
	case got := <-patched:
		t.Fatalf("card was published before interval elapsed: %q", got)
	case <-time.After(10 * time.Millisecond):
	}
	select {
	case got := <-patched:
		if got != "latest full answer" {
			t.Fatalf("patched card = %q, want latest full answer", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for interval update")
	}
}

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

func TestBusyLockRejectsConcurrent(t *testing.T) {
	b := &bot{cfg: &Config{}, sessions: map[string]*session{}}
	key := "p2p:user_a"
	if !b.acquireBusy(key) {
		t.Fatalf("expected first acquire to succeed")
	}
	if b.acquireBusy(key) {
		t.Fatalf("expected second acquire to be rejected while busy")
	}
	b.releaseBusy(key)
	if !b.acquireBusy(key) {
		t.Fatalf("expected acquire after release to succeed")
	}
	b.releaseBusy(key)
}

func TestIsClearContextCommandRequiresGroupMentionAndPhrase(t *testing.T) {
	if !isClearContextCommand(&types.NormalizedMessage{
		ChatType: "group", MentionedBot: true, Content: "@群史官 请清空上下文",
	}) {
		t.Fatal("expected group mention command to match")
	}
	for _, msg := range []*types.NormalizedMessage{
		{ChatType: "group", MentionedBot: false, Content: "@群史官 清空上下文"},
		{ChatType: "p2p", MentionedBot: true, Content: "清空上下文"},
		{ChatType: "group", MentionedBot: true, Content: "@群史官 帮我查询"},
	} {
		if isClearContextCommand(msg) {
			t.Fatalf("unexpected clear command match: %+v", msg)
		}
	}
}

func TestClearSessionContextKeepsInFlightBusyAndPreventsStaleWrite(t *testing.T) {
	b := &bot{cfg: &Config{}, sessions: map[string]*session{}}
	key := "group:oc_a:user_a"
	b.remember(key, "q1", "a1")
	b.mu.Lock()
	s := b.sessions[key]
	s.busy = true
	version := s.version
	b.mu.Unlock()

	b.clearSessionContext(key)

	b.mu.Lock()
	s = b.sessions[key]
	if len(s.history) != 0 || len(s.entities) != 0 || s.decomposition != nil || s.compressed {
		t.Fatalf("context was not cleared: %+v", s)
	}
	if !s.busy {
		t.Fatal("clear must not release an in-flight answer")
	}
	if s.version <= version {
		t.Fatalf("clear must advance version: %d -> %d", version, s.version)
	}
	b.mu.Unlock()

	if b.rememberIfVersion(key, version, "stale", "answer") {
		t.Fatal("stale answer must not be written after context clear")
	}
}

func TestSessionExpiresAfterIdle(t *testing.T) {
	b := &bot{cfg: &Config{}, sessions: map[string]*session{}}
	key := "p2p:user_b"
	b.remember(key, "问题1", "回答1")
	if len(b.historyOf(key)) != 2 {
		t.Fatalf("expected history length 2, got %d", len(b.historyOf(key)))
	}
	// 模拟已空闲超过 2 小时
	b.mu.Lock()
	b.sessions[key].lastActive = time.Now().Add(-(sessionIdleTTL + time.Minute))
	b.sessions[key].decomposition = &queryDecomposition{Entities: []string{"邓凯文"}}
	b.mu.Unlock()
	if len(b.historyOf(key)) != 0 {
		t.Fatalf("expected history reset after idle, got %d", len(b.historyOf(key)))
	}
	if got := b.decompositionOf(key); got != nil {
		t.Fatalf("expected decomposition reset after idle, got %+v", got)
	}
}

func TestShouldCompressAllowsLongerFeishuHistory(t *testing.T) {
	history := make([]llmMessage, 24)
	for i := range history {
		history[i] = llmMessage{Role: "user", Content: "短消息"}
	}
	if (&bot{}).shouldCompressLocked(&session{history: history}) {
		t.Fatalf("24 short messages should not trigger the relaxed fallback limit")
	}

	if !(&bot{}).shouldCompressLocked(&session{history: []llmMessage{{Role: "user", Content: strings.Repeat("长", 120000)}}}) {
		t.Fatalf("120K characters should trigger the fallback compression limit")
	}
}

func TestBuildDataContextIncludesFacts(t *testing.T) {
	d := &memorySearchData{
		Facts: []memFact{{Fact: "张三上月聊过旅行", ContactKey: "contact:zhangsan"}},
	}
	ctx := buildDataContext(d)
	if ctx == "" || !contains(ctx, "张三上月聊过旅行") {
		t.Fatalf("buildDataContext missing facts: %q", ctx)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestCleanMention(t *testing.T) {
	if got := cleanMention("@_user_1 我和张三聊了什么？"); got != "我和张三聊了什么？" {
		t.Errorf("cleanMention got %q", got)
	}
}

func TestCompressDiscardsIfVersionChanged(t *testing.T) {
	b := &bot{cfg: &Config{}, sessions: map[string]*session{}}
	key := "p2p:user_c"
	b.remember(key, "q1", "a1")
	b.remember(key, "q2", "a2")
	b.remember(key, "q3", "a3")
	b.mu.Lock()
	v0 := b.sessions[key].version
	b.mu.Unlock()

	// 触发压缩后、写回前又有新问答，version 变化
	b.remember(key, "q4", "a4")

	// 版本已变，写回应被放弃，历史保持 8 条不变
	b.applyCompression(key, v0, "summary")

	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[key]
	if len(s.history) != 8 {
		t.Errorf("expected history unchanged on race, got %d messages", len(s.history))
	}
	if s.history[0].Role != "user" || s.history[0].Content != "q1" {
		t.Errorf("expected history intact, first=%+v", s.history[0])
	}
}

func TestCompressAppliesWhenVersionUnchanged(t *testing.T) {
	b := &bot{cfg: &Config{}, sessions: map[string]*session{}}
	key := "p2p:user_d"
	b.remember(key, "q1", "a1")
	b.remember(key, "q2", "a2")
	b.mu.Lock()
	v := b.sessions[key].version
	b.mu.Unlock()

	b.applyCompression(key, v, "summary-text")

	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[key]
	if len(s.history) != 3 {
		t.Fatalf("expected 3 messages (summary + last pair), got %d", len(s.history))
	}
	if s.history[0].Role != "system" || !strings.Contains(s.history[0].Content, "summary-text") {
		t.Errorf("expected summary first, got %+v", s.history[0])
	}
}

func TestNotifyMentionPrefix(t *testing.T) {
	// 提问者 open_id，经 SDK 前缀生成应为 <at user_id="ou_xxx">
	prefix := normalize.ComposeMentionsTextPrefix([]types.Mention{{UserID: "ou_test_123", Name: ""}})
	if !strings.Contains(prefix, `<at user_id="ou_test_123">`) {
		t.Fatalf("mention prefix missing at tag: %q", prefix)
	}
	// 空 UserID 应被跳过
	empty := normalize.ComposeMentionsTextPrefix([]types.Mention{{UserID: "", Name: "x"}})
	if empty != "" {
		t.Fatalf("empty user_id should be skipped, got %q", empty)
	}
}

func TestContextMetaLine(t *testing.T) {
	created := time.Date(2026, 8, 5, 10, 0, 0, 0, time.Local)
	b := &bot{sessions: map[string]*session{
		"group:oc_abc:ou_def": {createdAt: created},
	}}
	line := b.contextMetaLine("group:oc_abc:ou_def", nil, 0)
	if !strings.HasPrefix(line, "> 上下文含 0 轮对话，始于") || strings.Contains(line, "token") {
		t.Fatalf("unexpected meta line: %q", line)
	}
	// 无 createdAt → 空行
	line2 := b.contextMetaLine("group:oc_abc:ou_def", nil, 0)
	if line2 == "" {
		t.Fatalf("createdAt set should produce meta line")
	}
}

func TestContextMetaLine_ShowsUTC8(t *testing.T) {
	// 传入的 createdAt 为 UTC 时间 10:00，卡片应显示东八区 18:00，而不是 UTC 的 10:00。
	created := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	b := &bot{sessions: map[string]*session{
		"group:oc_abc:ou_def": {createdAt: created},
	}}
	line := b.contextMetaLine("group:oc_abc:ou_def", nil, 0)
	if !strings.Contains(line, "上下文含 0 轮对话，始于 08-05 18:00") {
		t.Fatalf("expected UTC+8 time, got: %q", line)
	}
	if !strings.HasPrefix(line, "> ") {
		t.Fatalf("expected blockquote prefix, got: %q", line)
	}
}

func TestContextMetaLineIncludesTurnCountAndElapsed(t *testing.T) {
	created := time.Date(2026, time.August, 5, 10, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	b := &bot{sessions: map[string]*session{
		"p2p:user_a": {
			createdAt: created,
			history: []llmMessage{
				{Role: "user", Content: "q1"},
				{Role: "assistant", Content: "a1"},
				{Role: "user", Content: "q2"},
				{Role: "assistant", Content: "a2"},
			},
		},
	}}

	got := b.contextMetaLine("p2p:user_a", nil, 65*time.Second)
	for _, want := range []string{"上下文含 2 轮对话", "第一轮提问 | q1", "本次问答总耗时 1分05秒"} {
		if !strings.Contains(got, want) {
			t.Fatalf("meta missing %q: %q", want, got)
		}
	}
}

func TestContextMetaLineKeepsAllDetailsInOneQuoteBlock(t *testing.T) {
	b := &bot{sessions: map[string]*session{
		"p2p:user_a": {
			createdAt: time.Date(2026, time.August, 9, 8, 0, 0, 0, time.UTC),
			history: []llmMessage{
				{Role: "user", Content: "刘荟琪的评价"},
				{Role: "assistant", Content: "回答"},
			},
		},
	}}

	got := b.contextMetaLine("p2p:user_a", &answerRunMeta{
		Models: qaStepModels{
			QueryDecomposition: "glm-5.2",
			QueryExpansion:     "glm-5.2",
			FinalAnswer:        "glm-5.2",
		},
		Decomposition:   &queryDecomposition{Entities: []string{"刘荟琪"}, Concepts: []string{"评价"}},
		ExpandedQueries: []string{"刘荟琪的评价"},
	}, time.Second)

	if strings.Contains(got, "\n\n") {
		t.Fatalf("context metadata must be one contiguous quote block, got: %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "> ") {
			t.Fatalf("context metadata line must stay inside the quote block: %q", line)
		}
	}
}
