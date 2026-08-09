package main

import (
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

var (
	codeRevisionOnce sync.Once
	codeRevision     string
)

func currentCodeRevision() string {
	codeRevisionOnce.Do(func() {
		// 优先取当前仓库 HEAD，保证卡片中的 SHA 与部署目录实际检出的提交一致。
		if out, err := exec.Command("git", "rev-parse", "--short=12", "HEAD").Output(); err == nil {
			codeRevision = shortRevision(strings.TrimSpace(string(out)))
			return
		}
		// 已打包部署通常不包含 .git，此时才使用构建时写入的 VCS revision。
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" && setting.Value != "" {
					codeRevision = shortRevision(setting.Value)
					return
				}
			}
		}
		if codeRevision == "" {
			codeRevision = "unknown"
		}
	})
	return codeRevision
}

func shortRevision(revision string) string {
	revision = strings.TrimSpace(revision)
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}

func formatContextMetaStart(createdAt time.Time, revision string) string {
	line := "> 上下文始于 " + createdAt.In(time.FixedZone("UTC+8", 8*3600)).Format("01-02 15:04")
	if revision != "" {
		line += " · 代码 `" + shortRevision(revision) + "`"
	}
	return line
}
