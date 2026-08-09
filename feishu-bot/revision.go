package main

import (
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
		if info, ok := debug.ReadBuildInfo(); ok {
			if revision, found := revisionFromBuildInfo(info); found {
				codeRevision = revision
				return
			}
		}
		codeRevision = "unknown"
	})
	return codeRevision
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

func formatContextMetaStart(createdAt time.Time, revision string) string {
	line := "> 上下文始于 " + createdAt.In(time.FixedZone("UTC+8", 8*3600)).Format("01-02 15:04")
	if revision != "" {
		line += " · 代码 `" + shortRevision(revision) + "`"
	}
	return line
}
