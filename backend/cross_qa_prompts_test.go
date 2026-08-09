package main

import (
	"strings"
	"testing"
)

func TestEffectiveCrossQAPromptUsesDatabaseValue(t *testing.T) {
	withPromptTemplateTestDB(t)
	if err := initPromptTemplateTable(); err != nil {
		t.Fatalf("initialize prompt template table: %v", err)
	}
	if err := updatePromptTemplate("cross_qa_answer", "数据库中的最终回答提示词"); err != nil {
		t.Fatalf("update prompt template: %v", err)
	}

	got := effectiveCrossQAPrompt("cross_qa_answer")
	if got != "数据库中的最终回答提示词" {
		t.Fatalf("expected database prompt, got %q", got)
	}
}

func TestInjectCrossQAPromptPrependsEffectiveSystemMessage(t *testing.T) {
	withPromptTemplateTestDB(t)
	if err := initPromptTemplateTable(); err != nil {
		t.Fatalf("initialize prompt template table: %v", err)
	}
	if err := updatePromptTemplate("cross_qa_answer", "数据库中的最终回答提示词"); err != nil {
		t.Fatalf("update prompt template: %v", err)
	}
	messages := []LLMMessage{{Role: "user", Content: "问题"}}

	got := injectCrossQAPrompt(messages, "cross_qa_answer", "", "")
	if len(got) != 2 || got[0].Role != "system" || got[0].Content != "数据库中的最终回答提示词" {
		t.Fatalf("unexpected prompt injection: %+v", got)
	}
	if got[1] != messages[0] {
		t.Fatalf("input messages must follow injected system prompt: %+v", got)
	}
}

func TestRenderCrossQARawEvidencePromptInjectsRecords(t *testing.T) {
	records := "2026-08-09 14:05 ｜ 张三 ｜ 我周末到上海"
	got := renderCrossQARawEvidencePrompt(defaultCrossQARawEvidencePrompt, records)

	if strings.Contains(got, "{{records}}") {
		t.Fatalf("records placeholder was not rendered: %q", got)
	}
	if !strings.Contains(got, records) {
		t.Fatalf("rendered prompt missing records: %q", got)
	}
	if !strings.Contains(got, "```text") {
		t.Fatalf("rendered prompt must retain text code fence: %q", got)
	}
}

func TestDefaultCrossQAAnswerPromptUsesOnlyCodeBlockRuleForChatRecords(t *testing.T) {
	if strings.Contains(defaultCrossQAAnswerPrompt, "Markdown 引用标记") {
		t.Fatalf("chat-record prompt must not include a competing quote-line rule: %q", defaultCrossQAAnswerPrompt)
	}
	if !strings.Contains(defaultCrossQAAnswerPrompt, "```text") {
		t.Fatalf("chat-record prompt must retain the code-block rule: %q", defaultCrossQAAnswerPrompt)
	}
}

func TestRenderCrossQAAnswerPromptInjectsPinnedMemoriesAndAliases(t *testing.T) {
	template := "别名：{{aliases_table}}\n置顶：{{pinned_memories}}\n群：{{fence}}"
	got := renderCrossQAAnswerPrompt(template, "别名表内容", "置顶记忆内容")

	if strings.Contains(got, "{{aliases_table}}") || strings.Contains(got, "{{pinned_memories}}") {
		t.Fatalf("template variables were not rendered: %q", got)
	}
	if !strings.Contains(got, "别名：别名表内容") || !strings.Contains(got, "置顶：置顶记忆内容") {
		t.Fatalf("missing rendered context: %q", got)
	}
}
