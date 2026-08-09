package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	codeRevisionOnce   sync.Once
	codeRevision       string
	gitRevisionCommand = func(dir string) (string, error) {
		out, err := exec.Command("git", "-C", dir, "rev-parse", "--short=12", "HEAD").Output()
		return string(out), err
	}
)

func currentCodeRevision() string {
	codeRevisionOnce.Do(func() {
		workingDir, _ := os.Getwd()
		executable, _ := os.Executable()
		candidates := repositoryDirCandidates(workingDir, executable)
		if repoDir := strings.TrimSpace(os.Getenv("WELINK_REPO_DIR")); repoDir != "" {
			candidates = append([]string{repoDir}, candidates...)
		}
		for _, dir := range candidates {
			if revision, ok := gitRevisionForDir(dir); ok {
				codeRevision = revision
				return
			}
		}
		// 只展示运行时读取的仓库 HEAD，不以构建时 VCS 信息替代，便于与部署目录对齐。
		codeRevision = "unknown"
	})
	return codeRevision
}

func gitRevisionForDir(dir string) (string, bool) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", false
	}
	out, err := gitRevisionCommand(dir)
	if err != nil {
		return "", false
	}
	revision := shortRevision(out)
	return revision, revision != ""
}

func repositoryDirCandidates(workingDir, executable string) []string {
	candidates := make([]string, 0, 8)
	appendCandidate := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir)
		for _, existing := range candidates {
			if existing == dir {
				return
			}
		}
		candidates = append(candidates, dir)
	}

	appendCandidate(workingDir)
	if executable == "" {
		return candidates
	}
	for dir := filepath.Dir(executable); ; {
		appendCandidate(dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return candidates
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
