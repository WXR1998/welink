package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	fs := flag.NewFlagSet("feishu-bot", flag.ExitOnError)
	check := fs.Bool("check", false, "运行三段自检并退出")
	smoke := fs.String("smoke", "", "运行一次跨联系人问答冒烟测试并退出（传问题文本）")
	smokeEntity := fs.Bool("smoke-entity", false, "以测试实体邓凯文运行一次冒烟测试并退出")
	_ = fs.Parse(os.Args[1:])

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("[main] %v", err)
	}

	if *check {
		if err := runCheck(context.Background(), cfg); err != nil {
			log.Fatalf("[check] 自检失败: %v", err)
		}
		return
	}

	if *smoke != "" {
		if err := runSmoke(context.Background(), cfg, *smoke); err != nil {
			log.Fatalf("[smoke] 冒烟失败: %v", err)
		}
		return
	}

	if *smokeEntity {
		if err := runSmokeEntity(context.Background(), cfg); err != nil {
			log.Fatalf("[smoke-entity] 冒烟失败: %v", err)
		}
		return
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
