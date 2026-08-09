package main

import (
	"runtime/debug"
	"testing"
)

func TestRevisionFromBuildInfoUsesVCSRevision(t *testing.T) {
	info := &debug.BuildInfo{
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "748f174496839be50c5e9abfd439afd11e934d00"},
		},
	}

	got, ok := revisionFromBuildInfo(info)
	if !ok {
		t.Fatal("expected VCS revision")
	}
	if got != "748f17449683" {
		t.Fatalf("unexpected revision %q", got)
	}
}

func TestRevisionFromBuildInfoRejectsMissingRevision(t *testing.T) {
	if got, ok := revisionFromBuildInfo(&debug.BuildInfo{}); ok || got != "" {
		t.Fatalf("expected no revision, got %q", got)
	}
}
