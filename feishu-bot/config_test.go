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
	if !strings.Contains(card, "`60%`") || !strings.Contains(card, "███") {
		t.Fatalf("progress bar missing: %s", card)
	}
	if !strings.Contains(card, `"lark_md"`) && !strings.Contains(card, `"markdown"`) {
		t.Fatalf("card missing markdown element: %s", card)
	}
}

func TestProgressBar(t *testing.T) {
	if got := progressBar(0, 5); strings.Contains(got, "█") {
		t.Fatalf("0 progress should have no filled: %s", got)
	}
	if got := progressBar(5, 5); strings.Contains(got, "░") {
		t.Fatalf("100%% progress should have no empty: %s", got)
	}
	if got := progressBar(7, 5); got != progressBar(5, 5) {
		t.Fatalf("over-full should clamp to 100%%: %s", got)
	}
}

func TestProgressBarCellCap(t *testing.T) {
	// 无论 total 大小，格子数始终固定为 maxProgressCells，按百分比涂色。
	got := progressBar(10, 100) // 10%
	if strings.Count(got, "█")+strings.Count(got, "░") != maxProgressCells {
		t.Fatalf("cell count should always be %d: %s", maxProgressCells, got)
	}
	expect := maxProgressCells / 10
	if c := strings.Count(got, "█"); c != expect {
		t.Fatalf("10%% should fill %d of %d cells, got %d: %s", expect, maxProgressCells, c, got)
	}
	// 小 total 时格子数不变，不因 N/M 学习而变化
	small := progressBar(2, 5) // 40%
	if strings.Count(small, "█")+strings.Count(small, "░") != maxProgressCells {
		t.Fatalf("small totals still use %d cells: %s", maxProgressCells, small)
	}
	expect = maxProgressCells * 40 / 100
	if c := strings.Count(small, "█"); c != expect {
		t.Fatalf("40%% should fill %d of %d, got %d: %s", expect, maxProgressCells, c, small)
	}
	// 25%（50/200）按百分比填格
	expect = maxProgressCells * 25 / 100
	if c := strings.Count(progressBar(50, 200), "█"); c != expect {
		t.Fatalf("25%% should fill %d of %d, got %d", expect, maxProgressCells, c)
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

func TestLoadConfig_AnnounceChatIDAndHeartbeat(t *testing.T) {
	os.Setenv("FEISHU_APP_ID", "cli_x")
	os.Setenv("FEISHU_APP_SECRET", "secret")
	defer func() {
		os.Unsetenv("FEISHU_APP_ID")
		os.Unsetenv("FEISHU_APP_SECRET")
		os.Unsetenv("ANNOUNCE_CHAT_ID")
		os.Unsetenv("HEARTBEAT_INTERVAL_SEC")
		os.Unsetenv("DISCONNECT_COOLDOWN_MIN")
	}()

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AnnounceChatID != "" {
		t.Fatalf("expected empty announce chat id, got %q", cfg.AnnounceChatID)
	}
	if cfg.HeartbeatInterval.String() != "30s" {
		t.Fatalf("expected default 30s heartbeat, got %v", cfg.HeartbeatInterval)
	}
	if cfg.DisconnectCooldown.String() != "1h0m0s" {
		t.Fatalf("expected default 1h cooldown, got %v", cfg.DisconnectCooldown)
	}

	os.Setenv("ANNOUNCE_CHAT_ID", "oc_abc123")
	os.Setenv("HEARTBEAT_INTERVAL_SEC", "60")
	os.Setenv("DISCONNECT_COOLDOWN_MIN", "10")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AnnounceChatID != "oc_abc123" {
		t.Fatalf("expected announce chat id, got %q", cfg.AnnounceChatID)
	}
	if cfg.HeartbeatInterval.String() != "1m0s" {
		t.Fatalf("expected 1m heartbeat, got %v", cfg.HeartbeatInterval)
	}
	if cfg.DisconnectCooldown.String() != "10m0s" {
		t.Fatalf("expected 10m cooldown, got %v", cfg.DisconnectCooldown)
	}
}
