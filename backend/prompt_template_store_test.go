package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestInitPromptTemplateTableSeedsDefaultTemplates(t *testing.T) {
	withPromptTemplateTestDB(t)

	if err := initPromptTemplateTable(); err != nil {
		t.Fatalf("initialize prompt template table: %v", err)
	}
	templates, err := listPromptTemplates()
	if err != nil {
		t.Fatalf("list prompt templates: %v", err)
	}
	if len(templates) != len(defaultPromptTemplates) {
		t.Fatalf("expected %d templates, got %d", len(defaultPromptTemplates), len(templates))
	}
	template, err := getPromptTemplate("cross_qa_answer")
	if err != nil {
		t.Fatalf("get cross QA template: %v", err)
	}
	if template.Prompt == "" || template.Prompt != template.DefaultPrompt {
		t.Fatalf("expected seeded default prompt, got %+v", template)
	}
	if !strings.Contains(template.Prompt, "{{fence}}") {
		t.Fatalf("cross QA template must include group prompt placeholder: %q", template.Prompt)
	}
}

func TestUpdatePromptTemplatePersistsInDatabase(t *testing.T) {
	withPromptTemplateTestDB(t)
	if err := initPromptTemplateTable(); err != nil {
		t.Fatalf("initialize prompt template table: %v", err)
	}

	if err := updatePromptTemplate("cross_qa_answer", "数据库中的自定义模板"); err != nil {
		t.Fatalf("update template: %v", err)
	}
	template, err := getPromptTemplate("cross_qa_answer")
	if err != nil {
		t.Fatalf("get updated template: %v", err)
	}
	if template.Prompt != "数据库中的自定义模板" {
		t.Fatalf("expected persisted template, got %q", template.Prompt)
	}
}

func TestInitPromptTemplateTableMigratesLegacyPreferences(t *testing.T) {
	withPromptTemplateTestDB(t)
	if err := savePreferences(Preferences{PromptTemplates: map[string]string{
		"cross_qa_answer": "从 preferences.json 迁移的模板",
	}}); err != nil {
		t.Fatalf("save legacy preferences: %v", err)
	}

	if err := initPromptTemplateTable(); err != nil {
		t.Fatalf("initialize prompt template table: %v", err)
	}
	template, err := getPromptTemplate("cross_qa_answer")
	if err != nil {
		t.Fatalf("get migrated template: %v", err)
	}
	if template.Prompt != "从 preferences.json 迁移的模板" {
		t.Fatalf("expected legacy template in database, got %q", template.Prompt)
	}
	if got := loadPreferences().PromptTemplates; len(got) != 0 {
		t.Fatalf("legacy preferences must be cleared after migration: %+v", got)
	}
}

func withPromptTemplateTestDB(t *testing.T) {
	t.Helper()
	t.Setenv("PREFERENCES_PATH", filepath.Join(t.TempDir(), "preferences.json"))
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ai_analysis.db"))
	if err != nil {
		t.Fatalf("open test AI database: %v", err)
	}

	aiDBMu.Lock()
	previous := aiDB
	aiDB = db
	aiDBMu.Unlock()
	t.Cleanup(func() {
		aiDBMu.Lock()
		aiDB = previous
		aiDBMu.Unlock()
		_ = db.Close()
	})
}
