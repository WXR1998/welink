package main

import (
	"strings"
	"testing"
)

func TestMergeSearchTermsAppendsLLMSynonyms(t *testing.T) {
	got := mergeSearchTerms([]string{"锐评"}, []string{"评价", "吐槽", "嘲笑", "土狗", "装逼"})
	joined := strings.Join(got, "|")
	for _, want := range []string{"锐评", "评价", "吐槽", "嘲笑", "土狗", "装逼"} {
		if !strings.Contains(joined, want) {
			t.Errorf("mergeSearchTerms should include %q, got %v", want, got)
		}
	}
	// 空 search_terms 不影响 concepts
	if got2 := mergeSearchTerms([]string{"分手"}, nil); len(got2) != 1 || got2[0] != "分手" {
		t.Fatalf("unexpected merge without search_terms: %v", got2)
	}
}

func TestNeedsEvidenceRawMatchesEvaluationConcepts(t *testing.T) {
	cases := []struct {
		concepts []string
		want     bool
	}{
		{[]string{"锐评"}, true},
		{[]string{"评价"}, true},
		{[]string{"吐槽"}, true},
		{[]string{"分手原因"}, false},
		{[]string{"一起玩游戏"}, false},
	}
	for _, c := range cases {
		if got := needsEvidenceRaw(&QueryDecomposition{Concepts: c.concepts}); got != c.want {
			t.Errorf("needsEvidenceRaw(%v) = %v, want %v", c.concepts, got, c.want)
		}
	}
}
