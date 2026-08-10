package main

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	BotID            string
	BotSecret        string
	BotName          string
	WeLinkBaseURL    string
	WeLinkToken      string
	AllowedUsers     []string
	SessionStorePath string
}

func loadConfig() (*Config, error) {
	cfg := &Config{
		BotID:            strings.TrimSpace(os.Getenv("WECOM_BOT_ID")),
		BotSecret:        strings.TrimSpace(os.Getenv("WECOM_BOT_SECRET")),
		BotName:          strings.TrimSpace(os.Getenv("BOT_NAME")),
		WeLinkBaseURL:    strings.TrimRight(getenv("WELINK_BASE_URL", "http://127.0.0.1:8080"), "/"),
		WeLinkToken:      strings.TrimSpace(os.Getenv("WELINK_TOKEN")),
		AllowedUsers:     splitList(os.Getenv("ALLOWED_USERS")),
		SessionStorePath: strings.TrimSpace(os.Getenv("SESSION_STORE_PATH")),
	}
	if cfg.BotID == "" || cfg.BotSecret == "" {
		return nil, fmt.Errorf("WECOM_BOT_ID / WECOM_BOT_SECRET 未配置")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	return out
}
