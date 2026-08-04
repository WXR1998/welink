package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// runCheck 验证飞书凭证、WeLink 后端与长连接是否就绪，不常驻运行。
func runCheck(ctx context.Context, cfg *Config) error {
	fmt.Println("== 飞书 AI 问答网关自检 ==")

	// 1. 飞书租户凭证 + bot 信息
	fmt.Printf("[1/3] 验证飞书凭证 (app_id=%s) ...\n", cfg.FeishuAppID)
	client := lark.NewClient(cfg.FeishuAppID, cfg.FeishuAppSecret,
		lark.WithLogLevel(larkcore.LogLevelError),
	)
	botCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := client.Get(botCtx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		fmt.Printf("  ✗ 飞书凭证验证失败：%v\n", err)
		return fmt.Errorf("飞书 app_id/secret 无效或网络不可达")
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("  ✗ 飞书返回非 200：%d %s\n", resp.StatusCode, string(resp.RawBody))
		return fmt.Errorf("飞书返回非 200")
	}
	var botInfo struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenId         string `json:"open_id"`
			AppName        string `json:"app_name"`
			ActivateStatus int    `json:"activate_status"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(resp.RawBody, &botInfo); err != nil {
		fmt.Printf("  ✗ 解析飞书响应失败：%v\n", err)
		return err
	}
	if botInfo.Code != 0 {
		fmt.Printf("  ✗ 飞书返回错误码 %d：%s\n", botInfo.Code, botInfo.Msg)
		return fmt.Errorf("飞书返回错误码 %d", botInfo.Code)
	}
	statusText := "未知"
	switch botInfo.Bot.ActivateStatus {
	case 1:
		statusText = "未发布/开发中"
	case 2:
		statusText = "已启用"
	}
	fmt.Printf("  ✓ 飞书凭证有效，应用名=%s, open_id=%s, 状态=%s\n", botInfo.Bot.AppName, botInfo.Bot.OpenId, statusText)

	// 2. WeLink 后端连通性
	fmt.Printf("[2/3] 验证 WeLink 后端 (%s) ...\n", cfg.WeLinkBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.WeLinkBaseURL+"/api/status", nil)
	if err != nil {
		fmt.Printf("  ✗ 构造请求失败：%v\n", err)
		return err
	}
	if cfg.WeLinkToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.WeLinkToken)
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	wresp, err := hc.Do(req)
	if err != nil {
		fmt.Printf("  ✗ WeLink 不可达：%v\n", err)
		return err
	}
	defer wresp.Body.Close()
	wbody, _ := io.ReadAll(wresp.Body)
	if wresp.StatusCode != http.StatusOK {
		fmt.Printf("  ✗ WeLink 返回 %d：%s\n", wresp.StatusCode, strings.TrimSpace(string(wbody)))
		return fmt.Errorf("WeLink /api/status 返回 %d", wresp.StatusCode)
	}
	fmt.Printf("  ✓ WeLink 后端可达 (HTTP 200)\n")

	// 3. 飞书长连接就绪验证（短暂启动，等待 onReady）
	fmt.Println("[3/3] 验证飞书长连接（事件订阅）...")
	if err := checkWSReady(ctx, cfg); err != nil {
		fmt.Printf("  ✗ 长连接未就绪：%v\n", err)
		fmt.Println("    请确认：开发者后台 → 事件与回调 → 订阅方式选择「使用长连接接收事件/回调」，并已订阅 im.message.receive_v1。")
		return err
	}
	fmt.Println("  ✓ 飞书长连接已就绪")

	fmt.Println("自检全部通过。")
	return nil
}

// checkWSReady 启动一个临时长连接，等待 onReady 或超时后关闭。
func checkWSReady(ctx context.Context, cfg *Config) error {
	ready := make(chan struct{}, 1)
	wsClient := larkws.NewClient(cfg.FeishuAppID, cfg.FeishuAppSecret,
		larkws.WithLogLevel(larkcore.LogLevelError),
		larkws.WithOnReady(func() {
			select {
			case ready <- struct{}{}:
			default:
			}
		}),
	)

	errCh := make(chan error, 1)
	go func() {
		errCh <- wsClient.Start(ctx)
	}()

	defer wsClient.Close()

	select {
	case <-ready:
		return nil
	case err := <-errCh:
		return err
	case <-time.After(12 * time.Second):
		return fmt.Errorf("12 秒内未收到长连接就绪事件")
	}
}
