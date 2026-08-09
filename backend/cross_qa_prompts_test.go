package main

import (
	"strings"
	"testing"
)

func TestEffectiveCrossQAPromptUsesCustomValue(t *testing.T) {
	prefs := Preferences{PromptTemplates: map[string]string{
		"cross_qa_answer": "自定义最终回答提示词",
	}}

	got := effectiveCrossQAPrompt(prefs, "cross_qa_answer")
	if got != "自定义最终回答提示词" {
		t.Fatalf("expected custom prompt, got %q", got)
	}
}

func TestInjectCrossQAPromptPrependsEffectiveSystemMessage(t *testing.T) {
	prefs := Preferences{PromptTemplates: map[string]string{
		"cross_qa_answer": "自定义最终回答提示词",
	}}
	messages := []LLMMessage{{Role: "user", Content: "问题"}}

	got := injectCrossQAPrompt(messages, "cross_qa_answer", prefs)
	if len(got) != 2 || got[0].Role != "system" || got[0].Content != "自定义最终回答提示词" {
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
