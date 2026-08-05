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

// runSmoke 直接调用跨联系人问答做一次真实链路冒烟，不依赖飞书消息。
func runSmoke(ctx context.Context, cfg *Config, question string) error {
	fmt.Printf("== 跨联系人问答冒烟测试 ==\n")
	fmt.Printf("问题: %s\n", question)

	convKey := "feishu:smoke:test"
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	fmt.Println("[1/2] memory-search 检索...")
	data, err := memorySearch(ctx, cfg, question, convKey, "", false,
		func(step, detail string) {
			fmt.Printf("  - %s: %s\n", step, detail)
		},
		func(names []string) {
			if len(names) > 0 {
				fmt.Printf("  ↳ 解析到实体: %v\n", names)
			}
		},
	)
	if err != nil {
		if err == errMissingEntity {
			fmt.Println("  ✗ 未指定实体（联系人/群名），且上下文无实体，已拒绝进入检索")
			return err
		}
		if err == errEntityNotFound {
			fmt.Println("  ✗ 指定的实体未命中（联系人/群名未在数据中找到），已拒绝进入检索")
			return err
		}
		fmt.Printf("  ✗ memory-search 失败: %v\n", err)
		return err
	}
	if !data.hasResolvedEntity() {
		fmt.Println("  ✗ 未解析到有效实体，已拒绝进入检索")
		return fmt.Errorf("未指定实体")
	}
	ctxData := buildDataContext(data)
	if ctxData == "" {
		fmt.Println("  ✓ memory-search 完成（无检索上下文）")
	} else {
		fmt.Printf("  ✓ memory-search 完成，检索到上下文 %d 字符\n", len(ctxData))
	}

	fmt.Println("[2/2] analyze 生成回答...")
	answer, err := analyzeQuestion(ctx, cfg, "", question, convKey, nil, ctxData)
	if err != nil {
		fmt.Printf("  ✗ analyze 失败: %v\n", err)
		return err
	}
	if answer == "" {
		fmt.Println("  ✗ analyze 返回空回答")
		return fmt.Errorf("analyze 返回空回答")
	}
	fmt.Printf("  ✓ analyze 完成，回答 %d 字符：\n\n%s\n", len(answer), answer)
	return nil
}

// runSmokeEntity 以邓凯文为测试实体跑一次冒烟，验证实体约束与跨联系人检索链路。
func runSmokeEntity(ctx context.Context, cfg *Config) error {
	return runSmoke(ctx, cfg, "我和邓凯文最近聊了什么？")
}

