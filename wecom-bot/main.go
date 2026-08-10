package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const wecomWebSocketURL = "wss://openws.work.weixin.qq.com"

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("[main] %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("[main] 企业微信机器人已退出: %v", err)
	}
}

func run(ctx context.Context, cfg *Config) error {
	for {
		if err := runConnection(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[wecom] 长连接断开: %v; 3 秒后重连", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func runConnection(ctx context.Context, cfg *Config) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wecomWebSocketURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	var writeMu sync.Mutex
	send := func(frame commandFrame) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(frame)
	}
	if err := send(newSubscribe(cfg.BotID, cfg.BotSecret, newID())); err != nil {
		return err
	}
	log.Printf("[wecom] 已建立连接并发送订阅请求, bot_id=%s", cfg.BotID)

	b := newBot(cfg, send)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		msg, ok, err := decodeIncomingGroupText(raw)
		if err != nil {
			log.Printf("[wecom] 忽略无法解析的消息: %v", err)
			continue
		}
		if ok {
			b.dispatch(msg)
			continue
		}

		var frame commandFrame
		if err := json.Unmarshal(raw, &frame); err == nil && frame.ErrorCode != 0 {
			log.Printf("[wecom] 平台返回错误: code=%d message=%s", frame.ErrorCode, frame.ErrorMsg)
		}
	}
}
