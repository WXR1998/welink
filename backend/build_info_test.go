package main

import "testing"

func TestCurrentBuildInfoReturnsInjectedRevisionAndCommitTime(t *testing.T) {
	previousRevision, previousCommitTime := gitCommit, gitCommitTime
	gitCommit = "660d993"
	gitCommitTime = "2026-08-10T10:20:14+08:00"
	t.Cleanup(func() {
		gitCommit = previousRevision
		gitCommitTime = previousCommitTime
	})

	got := currentBuildInfo()
	if got.GitSHA != "660d993" {
		t.Fatalf("git SHA = %q, want %q", got.GitSHA, "660d993")
	}
	if got.CommitTime != "2026-08-10T10:20:14+08:00" {
		t.Fatalf("commit time = %q, want injected commit time", got.CommitTime)
	}
}
