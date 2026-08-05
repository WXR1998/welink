package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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

	// 流式回复载体：md（默认，Markdown 富文本 post 流式）或 card（卡片流式）
	StreamMode string

	// 会话历史持久化文件路径；为空表示仅内存、重启丢失
	SessionStorePath string

	// 允许的飞书用户（open_id 或 user_id），为空表示不限制（危险）
	AllowedUsers []string

	// LLM 模型名称（覆盖后端 profile 中的模型）
	LLMModel string

	// 需要维护群公告的群 chat_id（逗号分隔）；留空则自动发现 bot 所在全部群
	AnnounceChatID string

	// 内部管理 HTTP 服务监听地址（如 :8111，供后端/前端查看删除会话）；空=关闭
	ManageAddr string

	// 心跳检测间隔与断连公告限频（默认见 loadConfig）
	HeartbeatInterval time.Duration
	DisconnectCooldown time.Duration
}

// loadConfig 从环境变量读配置；可用 FEISHU_CONFIG 指向 JSON 覆盖。
func loadConfig() (*Config, error) {
	cfg := &Config{
		FeishuAppID:      os.Getenv("FEISHU_APP_ID"),
		FeishuAppSecret:  os.Getenv("FEISHU_APP_SECRET"),
		WeLinkBaseURL:    strings.TrimRight(getenv("WELINK_BASE_URL", "http://127.0.0.1:8080"), "/"),
		WeLinkToken:      os.Getenv("WELINK_TOKEN"),
		DefaultProfileID: os.Getenv("DEFAULT_PROFILE_ID"),
		StreamMode:        strings.ToLower(getenv("STREAM_MODE", "md")),
		SessionStorePath:  os.Getenv("SESSION_STORE_PATH"),
		AllowedUsers:      splitList(os.Getenv("ALLOWED_USERS")),
		LLMModel:          getenv("LLM_MODEL", "lingjun.internal/deepseek-v4-flash"),
		AnnounceChatID:    os.Getenv("ANNOUNCE_CHAT_ID"),
		ManageAddr:        os.Getenv("MANAGE_ADDR"),
		HeartbeatInterval: time.Duration(getenvInt("HEARTBEAT_INTERVAL_SEC", 30)) * time.Second,
		DisconnectCooldown: time.Duration(getenvInt("DISCONNECT_COOLDOWN_MIN", 60)) * time.Minute,
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
			DefaultProfile   string   `json:"default_profile_id"`
			StreamMode       string   `json:"stream_mode"`
			SessionStorePath string   `json:"session_store_path"`
			AllowedUsers     []string `json:"allowed_users"`
			LLMModel         string   `json:"llm_model"`
			AnnounceChatID   string   `json:"announce_chat_id"`
			ManageAddr       string   `json:"manage_addr"`
			HeartbeatIntervalSec int `json:"heartbeat_interval_sec"`
			DisconnectCooldownMin int `json:"disconnect_cooldown_min"`
		}
		if err := json.Unmarshal(b, &overrides); err != nil {
			return nil, err
		}
		applyOverride(&cfg.FeishuAppID, overrides.FeishuAppID)
		applyOverride(&cfg.FeishuAppSecret, overrides.FeishuAppSecret)
		applyOverride(&cfg.WeLinkBaseURL, trimSlash(overrides.WeLinkBaseURL))
		applyOverride(&cfg.WeLinkToken, overrides.WeLinkToken)
		applyOverride(&cfg.DefaultProfileID, overrides.DefaultProfile)
		applyOverride(&cfg.StreamMode, strings.ToLower(overrides.StreamMode))
		applyOverride(&cfg.SessionStorePath, overrides.SessionStorePath)
		applyOverride(&cfg.LLMModel, overrides.LLMModel)
		applyOverride(&cfg.AnnounceChatID, overrides.AnnounceChatID)
		applyOverride(&cfg.ManageAddr, overrides.ManageAddr)
		if overrides.HeartbeatIntervalSec > 0 {
			cfg.HeartbeatInterval = time.Duration(overrides.HeartbeatIntervalSec) * time.Second
		}
		if overrides.DisconnectCooldownMin > 0 {
			cfg.DisconnectCooldown = time.Duration(overrides.DisconnectCooldownMin) * time.Minute
		}
		if len(overrides.AllowedUsers) > 0 {
			cfg.AllowedUsers = overrides.AllowedUsers
		}
	}

	if cfg.FeishuAppID == "" || cfg.FeishuAppSecret == "" {
		return nil, fmt.Errorf("FEISHU_APP_ID / FEISHU_APP_SECRET 未配置")
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

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
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
