package main

import (
	"strings"
	"testing"
)

func TestContextTokenBudgetUsesSeventyPercentByDefault(t *testing.T) {
	if got := contextTokenBudget(llmConfig{contextWindow: 400000}); got != 280000 {
		t.Fatalf("400K context should reserve a 280K input budget, got %d", got)
	}
	if got := contextTokenBudget(llmConfig{}); got != 89600 {
		t.Fatalf("default 128K context should reserve an 89.6K input budget, got %d", got)
	}
}

func TestContextTokenBudgetHonorsExplicitThreshold(t *testing.T) {
	if got := contextTokenBudget(llmConfig{contextWindow: 400000, compressThreshold: 320000}); got != 320000 {
		t.Fatalf("explicit compression threshold should be preserved, got %d", got)
	}
}

func TestRecentConversationBudgetReservesSpaceForSystemContext(t *testing.T) {
	if got := recentConversationTokenBudget(280000, 210000); got != 62000 {
		t.Fatalf("system context should leave 62K tokens for recent messages, got %d", got)
	}
}

func TestEstimateContextTokensUsesConservativeChineseBudget(t *testing.T) {
	if got := estimateContextTokens(strings.Repeat("证", 10)); got != 11 {
		t.Fatalf("10 Chinese runes should reserve 11 context tokens, got %d", got)
	}
}

func TestTruncatePromptToTokenBudgetKeepsPromptWithinBudget(t *testing.T) {
	msgs := []LLMMessage{
		{Role: "system", Content: strings.Repeat("系", 400)},
		{Role: "user", Content: strings.Repeat("问", 40)},
	}

	got := truncatePromptToTokenBudget(msgs, 100)
	if tokens := estimateMsgTokens(got); tokens > 100 {
		t.Fatalf("prompt exceeds token budget: %d", tokens)
	}
	if !strings.Contains(got[0].Content, "因长度限制已截断部分聊天记录") {
		t.Fatalf("expected system prompt truncation marker, got %q", got[0].Content)
	}
	if got[1].Content != msgs[1].Content {
		t.Fatalf("user message must be retained, got %q", got[1].Content)
	}
}

func TestTruncatePromptToTokenBudgetTruncatesOversizedUserEvidence(t *testing.T) {
	msgs := []LLMMessage{
		{Role: "system", Content: "你是助手"},
		{Role: "user", Content: "问题：最新问题\n\n检索证据：\n" + strings.Repeat("证", 400)},
	}

	got := truncatePromptToTokenBudget(msgs, 100)
	if tokens := estimateContextMsgTokens(got); tokens > 100 {
		t.Fatalf("prompt exceeds conservative context budget: %d", tokens)
	}
	if !strings.Contains(got[1].Content, "问题：最新问题") {
		t.Fatalf("latest user question must be retained: %q", got[1].Content)
	}
	if !strings.Contains(got[1].Content, "因长度限制已截断部分聊天记录") {
		t.Fatalf("oversized user evidence must be truncated: %q", got[1].Content)
	}
}
