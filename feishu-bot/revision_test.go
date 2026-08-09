package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitRevisionForDirReadsRepositoryHead(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("create git directory: %v", err)
	}

	original := gitRevisionCommand
	t.Cleanup(func() {
		gitRevisionCommand = original
	})
	var gotDir string
	gitRevisionCommand = func(dir string) (string, error) {
		gotDir = dir
		return "87ded7d3cfd0\n", nil
	}

	got, ok := gitRevisionForDir(repo)
	if !ok {
		t.Fatal("expected repository revision")
	}
	if got != "87ded7d3cfd0" {
		t.Fatalf("unexpected revision %q", got)
	}
	if gotDir != repo {
		t.Fatalf("git should run in repository directory, got %q", gotDir)
	}
}

func TestRepositoryDirCandidatesIncludeExecutableParents(t *testing.T) {
	executable := filepath.Join(string(filepath.Separator), "opt", "welink", "feishu-bot", "welink-feishu-bot")
	got := repositoryDirCandidates("/work/current", executable)
	for _, want := range []string{
		"/work/current",
		filepath.Join("/opt", "welink", "feishu-bot"),
		filepath.Join("/opt", "welink"),
		"/opt",
	} {
		if !containsString(got, want) {
			t.Fatalf("candidate list %v missing %q", got, want)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
