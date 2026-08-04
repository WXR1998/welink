package main

import (
	"os"
	"strings"
	"testing"
)

func TestLoadConfig_RequiresIDsAndMode(t *testing.T) {
	os.Setenv("FEISHU_APP_ID", "cli_x")
	os.Setenv("FEISHU_APP_SECRET", "secret")
	defer func() {
		os.Unsetenv("FEISHU_APP_ID")
		os.Unsetenv("FEISHU_APP_SECRET")
		os.Unsetenv("STREAM_MODE")
	}()

	os.Unsetenv("FEISHU_APP_ID")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "FEISHU_APP_ID") {
		t.Fatalf("expected FEISHU_APP_ID error, got %v", err)
	}

	os.Setenv("FEISHU_APP_ID", "cli_x")
	os.Setenv("STREAM_MODE", "bogus")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "STREAM_MODE") {
		t.Fatalf("expected STREAM_MODE error, got %v", err)
	}

	os.Setenv("STREAM_MODE", "card")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.StreamMode != "card" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestCardWithTextIsValidJSON(t *testing.T) {
	card := cardWithText("你好")
	for _, want := range []string{`"schema"`, `"2.0"`, "streaming_mode", `"你好"`, `"body"`, `"elements"`} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %s: %s", want, card)
		}
	}
}

func TestCardJSONWithProgress(t *testing.T) {
	card := cardJSON("🔎 正在检索", "正文", progressBar(3, 5))
	if !strings.Contains(card, `"🔎 正在检索"`) || !strings.Contains(card, `"正文"`) {
		t.Fatalf("card missing title/body: %s", card)
	}
	if !strings.Contains(card, "60%") || !strings.Contains(card, "🟩🟩🟩") {
		t.Fatalf("progress bar missing: %s", card)
	}
	if !strings.Contains(card, `"lark_md"`) && !strings.Contains(card, `"markdown"`) {
		t.Fatalf("card missing markdown element: %s", card)
	}
}

func TestProgressBar(t *testing.T) {
	if got := progressBar(0, 5); strings.Contains(got, "🟩") {
		t.Fatalf("0 progress should have no filled: %s", got)
	}
	if got := progressBar(5, 5); strings.Contains(got, "⬜") {
		t.Fatalf("100%% progress should have no empty: %s", got)
	}
	if got := progressBar(7, 5); got != progressBar(5, 5) {
		t.Fatalf("over-full should clamp to 100%%: %s", got)
	}
}


func TestLoadConfig_LLMModelDefault(t *testing.T) {
	os.Setenv("FEISHU_APP_ID", "cli_x")
	os.Setenv("FEISHU_APP_SECRET", "secret")
	defer func() {
		os.Unsetenv("FEISHU_APP_ID")
		os.Unsetenv("FEISHU_APP_SECRET")
		os.Unsetenv("LLM_MODEL")
	}()

	os.Unsetenv("LLM_MODEL")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LLMModel != "lingjun.internal/deepseek-v4-flash" {
		t.Fatalf("expected default LLM model, got %q", cfg.LLMModel)
	}

	os.Setenv("LLM_MODEL", "custom-model")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LLMModel != "custom-model" {
		t.Fatalf("expected custom model, got %q", cfg.LLMModel)
	}
}
