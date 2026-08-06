package main

import "testing"

func TestReplaceAllFoldCaseInsensitive(t *testing.T) {
	out := replaceAllFold("群友如何锐评DJ坤", "dj坤", "邓皓琨")
	if out != "群友如何锐评邓皓琨" {
		t.Fatalf("expected case-insensitive replace, got %q", out)
	}
}

func TestNormalizeEntityNamesNilSvcNoOp(t *testing.T) {
	in := "群友如何锐评土鲫鱼"
	if got := normalizeEntityNames(in, nil); got != in {
		t.Fatalf("expected unchanged with nil svc, got %q", got)
	}
}
