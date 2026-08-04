package main

import (
	"os"
	"strings"
	"testing"
)

func TestLoadConfig_RequiresKeyAndMode(t *testing.T) {
	os.Setenv("FEISHU_APP_ID", "cli_x")
	os.Setenv("FEISHU_APP_SECRET", "secret")
	defer func() {
		os.Unsetenv("FEISHU_APP_ID")
		os.Unsetenv("FEISHU_APP_SECRET")
		os.Unsetenv("DEFAULT_AI_KEY")
		os.Unsetenv("STREAM_MODE")
	}()

	os.Unsetenv("DEFAULT_AI_KEY")
	os.Setenv("STREAM_MODE", "card")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "DEFAULT_AI_KEY") {
		t.Fatalf("expected DEFAULT_AI_KEY error, got %v", err)
	}

	os.Setenv("DEFAULT_AI_KEY", "contact:alice")
	os.Setenv("STREAM_MODE", "bogus")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "STREAM_MODE") {
		t.Fatalf("expected STREAM_MODE error, got %v", err)
	}

	os.Setenv("STREAM_MODE", "card")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.StreamMode != "card" || cfg.DefaultKey != "contact:alice" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestCardWithTextIsValidJSON(t *testing.T) {
	card := cardWithText("你好")
	if !strings.Contains(card, "streaming_mode") || !strings.Contains(card, `"你好"`) {
		t.Fatalf("card missing fields: %s", card)
	}
}
