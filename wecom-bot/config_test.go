package main

import (
	"os"
	"strings"
	"testing"
)

func TestLoadConfigRequiresBotCredentials(t *testing.T) {
	t.Setenv("WECOM_BOT_ID", "")
	t.Setenv("WECOM_BOT_SECRET", "")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "WECOM_BOT_ID") {
		t.Fatalf("expected BotID configuration error, got %v", err)
	}
}

func TestLoadConfigReadsGatewaySettings(t *testing.T) {
	t.Setenv("WECOM_BOT_ID", "bot_1")
	t.Setenv("WECOM_BOT_SECRET", "secret_1")
	t.Setenv("WELINK_BASE_URL", "http://backend:8080/")
	t.Setenv("ALLOWED_USERS", "alice, bob")
	t.Setenv("BOT_NAME", "知识助手")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.BotID != "bot_1" || cfg.BotSecret != "secret_1" || cfg.WeLinkBaseURL != "http://backend:8080" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if len(cfg.AllowedUsers) != 2 || cfg.AllowedUsers[0] != "alice" || cfg.BotName != "知识助手" {
		t.Fatalf("unexpected config: %#v", cfg)
	}

	_ = os.Unsetenv("WELINK_BASE_URL")
}
