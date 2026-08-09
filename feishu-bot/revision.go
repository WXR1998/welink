package main

import (
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

var (
	codeRevisionOnce sync.Once
	codeRevision     string
	buildRevision    string
)

func currentCodeRevision() string {
	codeRevisionOnce.Do(func() {
		var info *debug.BuildInfo
		if buildInfo, ok := debug.ReadBuildInfo(); ok {
			info = buildInfo
		}
		codeRevision = preferredCodeRevision(buildRevision, info)
		if codeRevision == "" {
			codeRevision = "unknown"
		}
	})
	return codeRevision
}

func preferredCodeRevision(injected string, info *debug.BuildInfo) string {
	if revision := shortRevision(injected); revision != "" {
		return revision
	}
	if revision, ok := revisionFromBuildInfo(info); ok {
		return revision
	}
	return ""
}

func revisionFromBuildInfo(info *debug.BuildInfo) (string, bool) {
	if info == nil {
		return "", false
	}
	for _, setting := range info.Settings {
		if setting.Key != "vcs.revision" {
			continue
		}
		revision := shortRevision(setting.Value)
		return revision, revision != ""
	}
	return "", false
}

func shortRevision(revision string) string {
	revision = strings.TrimSpace(revision)
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}

func formatContextMetaStart(createdAt time.Time, revision string, turns int) string {
	line := fmt.Sprintf("> 上下文含 %d 轮对话，始于 %s", turns, createdAt.In(time.FixedZone("UTC+8", 8*3600)).Format("01-02 15:04"))
	if revision != "" {
		line += " · 代码 `" + shortRevision(revision) + "`"
	}
	return line
}

func formatAnswerElapsed(elapsed time.Duration) string {
	seconds := int(elapsed.Round(time.Second).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	if seconds < 60 {
		return fmt.Sprintf("> 本次问答总耗时 %d秒", seconds)
	}
	return fmt.Sprintf("> 本次问答总耗时 %d分%02d秒", seconds/60, seconds%60)
}
