package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Config 汇总飞书网关的全部设置。
type Config struct {
	// 飞书自建应用凭证
	FeishuAppID     string
	FeishuAppSecret string

	// WeLink 后端地址（本机默认 http://127.0.0.1:8080）
	WeLinkBaseURL string

	// 本机可跳过配对鉴权；Docker 下非回环需显式带 token
	WeLinkToken string

	// 默认 AI profile_id（对应前端设置里的 LLM Profile）
	DefaultProfileID string

	// 默认 WeLink 检索 key（例如 contact:alice 或 group:闲聊）
	DefaultKey string

	// 流式回复载体：md（默认，Markdown 富文本 post 流式）或 card（卡片流式）
	StreamMode string

	// 允许使用的联系人 key，为空表示不限制（危险）
	// 例如：contact:alice,group:闲聊
	AllowedKeys []string

	// 允许的飞书用户（open_id 或 user_id），为空表示不限制（危险）
	AllowedUsers []string
}

// loadConfig 从环境变量读配置；可用 FEISHU_CONFIG 指向 JSON 覆盖。
func loadConfig() (*Config, error) {
	cfg := &Config{
		FeishuAppID:      os.Getenv("FEISHU_APP_ID"),
		FeishuAppSecret:  os.Getenv("FEISHU_APP_SECRET"),
		WeLinkBaseURL:    strings.TrimRight(getenv("WELINK_BASE_URL", "http://127.0.0.1:8080"), "/"),
		WeLinkToken:      os.Getenv("WELINK_TOKEN"),
		DefaultProfileID: os.Getenv("DEFAULT_PROFILE_ID"),
		DefaultKey:       os.Getenv("DEFAULT_AI_KEY"),
		StreamMode:       strings.ToLower(getenv("STREAM_MODE", "md")),
		AllowedKeys:      splitList(os.Getenv("ALLOWED_KEYS")),
		AllowedUsers:     splitList(os.Getenv("ALLOWED_USERS")),
	}

	if p := os.Getenv("FEISHU_CONFIG"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var overrides struct {
			FeishuAppID     string   `json:"feishu_app_id"`
			FeishuAppSecret string   `json:"feishu_app_secret"`
			WeLinkBaseURL   string   `json:"welink_base_url"`
			WeLinkToken     string   `json:"welink_token"`
			DefaultProfile  string   `json:"default_profile_id"`
			DefaultKey      string   `json:"default_ai_key"`
			StreamMode      string   `json:"stream_mode"`
			AllowedKeys     []string `json:"allowed_keys"`
			AllowedUsers    []string `json:"allowed_users"`
		}
		if err := json.Unmarshal(b, &overrides); err != nil {
			return nil, err
		}
		applyOverride(&cfg.FeishuAppID, overrides.FeishuAppID)
		applyOverride(&cfg.FeishuAppSecret, overrides.FeishuAppSecret)
		applyOverride(&cfg.WeLinkBaseURL, trimSlash(overrides.WeLinkBaseURL))
		applyOverride(&cfg.WeLinkToken, overrides.WeLinkToken)
		applyOverride(&cfg.DefaultProfileID, overrides.DefaultProfile)
		applyOverride(&cfg.DefaultKey, overrides.DefaultKey)
		applyOverride(&cfg.StreamMode, strings.ToLower(overrides.StreamMode))
		if len(overrides.AllowedKeys) > 0 {
			cfg.AllowedKeys = overrides.AllowedKeys
		}
		if len(overrides.AllowedUsers) > 0 {
			cfg.AllowedUsers = overrides.AllowedUsers
		}
	}

	if cfg.FeishuAppID == "" || cfg.FeishuAppSecret == "" {
		return nil, fmt.Errorf("FEISHU_APP_ID / FEISHU_APP_SECRET 未配置")
	}
	if cfg.DefaultKey == "" {
		return nil, fmt.Errorf("DEFAULT_AI_KEY 未配置，请设置检索范围（如 contact:alice / group:xxx）")
	}
	if cfg.StreamMode != "md" && cfg.StreamMode != "card" {
		return nil, fmt.Errorf("STREAM_MODE 仅支持 md 或 card，当前: %s", cfg.StreamMode)
	}
	return cfg, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func applyOverride(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

func trimSlash(s string) string {
	return strings.TrimRight(s, "/")
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
