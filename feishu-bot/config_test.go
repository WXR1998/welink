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
	if !strings.Contains(card, "streaming_mode") || !strings.Contains(card, `"你好"`) {
		t.Fatalf("card missing fields: %s", card)
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
