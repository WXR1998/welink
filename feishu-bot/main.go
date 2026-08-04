package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if len(os.Args) > 1 && os.Args[1] == "--check" {
		cfg, err := loadConfig()
		if err != nil {
			log.Fatalf("[check] %v", err)
		}
		if err := runCheck(context.Background(), cfg); err != nil {
			log.Fatalf("[check] 自检失败: %v", err)
		}
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("[main] %v", err)
	}
	log.Printf("[main] WeLink AI 网关配置已加载，base_url=%s", cfg.WeLinkBaseURL)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	b, err := newBot(ctx, cfg)
	if err != nil {
		log.Fatalf("[main] 初始化飞书 bot 失败: %v", err)
	}

	errc := make(chan error, 1)
	go func() {
		errc <- b.run(ctx)
	}()

	select {
	case <-ctx.Done():
		log.Printf("[main] 收到退出信号，正在停止…")
	case err := <-errc:
		log.Fatalf("[main] 飞书长连接异常退出: %v", err)
	}
}
