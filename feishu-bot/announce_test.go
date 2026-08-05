package main

import (
	"testing"
	"time"
)

func TestBuildAnnouncementBlocksConnected(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 30, 44, 0, time.Local)
	blocks := buildAnnouncementBlocks(true, now)
	if len(blocks) < 5 {
		t.Fatalf("expected many blocks, got %d", len(blocks))
	}
	// 最后一个块应为“已连接”
	last := blocks[len(blocks)-1]
	if last.Text == nil || len(last.Text.Elements) == 0 ||
		last.Text.Elements[0].TextRun == nil || last.Text.Elements[0].TextRun.Content == nil {
		t.Fatalf("last block should be text with content")
	}
	if got := *last.Text.Elements[0].TextRun.Content; got != "✅ 已连接" {
		t.Fatalf("expected ✅ 已连接, got %q", got)
	}
}

func TestBuildAnnouncementBlocksDisconnected(t *testing.T) {
	now := time.Date(2026, 8, 5, 15, 4, 0, 0, time.Local)
	blocks := buildAnnouncementBlocks(false, now)
	last := blocks[len(blocks)-1]
	if last.Text == nil || len(last.Text.Elements) == 0 ||
		last.Text.Elements[0].TextRun == nil || last.Text.Elements[0].TextRun.Content == nil {
		t.Fatalf("last block should be text with content")
	}
	got := *last.Text.Elements[0].TextRun.Content
	if got != "❌ 连接已断开（08-05 15:04）" {
		t.Fatalf("expected disconnect text, got %q", got)
	}
}

func TestBuildAnnouncementBlocksContainsStartupTime(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 30, 44, 0, time.Local)
	blocks := buildAnnouncementBlocks(true, now)
	found := false
	for _, blk := range blocks {
		if blk.Text != nil && len(blk.Text.Elements) > 0 &&
			blk.Text.Elements[0].TextRun != nil && blk.Text.Elements[0].TextRun.Content != nil &&
			*blk.Text.Elements[0].TextRun.Content == "2026-08-05 10:30:44" {
			found = true
		}
	}
	if !found {
		t.Fatalf("startup time should be present")
	}
}
