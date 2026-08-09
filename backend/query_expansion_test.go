package main

import (
	"strings"
	"testing"
)

func TestBuildQueryExpansionPromptRequiresAliasNormalization(t *testing.T) {
	p := buildQueryExpansionPrompt(nil)
	// 空库时提示仍应要求还原外号为原名。
	for _, want := range []string{
		"必须依据下面的映射关系把它还原为对应的真实姓名",
		"不要使用外号/简称",
		"查询扩展助手",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("query expansion prompt missing %q: %s", want, p)
		}
	}
}

func TestBuildQueryExpansionPromptUsesDatabaseTemplate(t *testing.T) {
	withPromptTemplateTestDB(t)
	if err := initPromptTemplateTable(); err != nil {
		t.Fatalf("initialize prompt template table: %v", err)
	}
	if err := updatePromptTemplate("cross_qa_expansion", "别名：{{aliases_table}}\n置顶：{{pinned_memories}}\n自定义扩展提示词"); err != nil {
		t.Fatalf("update query expansion template: %v", err)
	}

	got := buildQueryExpansionPrompt(nil)
	if strings.Contains(got, "{{aliases_table}}") || strings.Contains(got, "{{pinned_memories}}") {
		t.Fatalf("query expansion variables were not rendered: %q", got)
	}
	if !strings.Contains(got, "自定义扩展提示词") {
		t.Fatalf("query expansion did not use database template: %q", got)
	}
}
