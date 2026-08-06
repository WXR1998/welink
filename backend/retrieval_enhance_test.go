package main

import (
	"strings"
	"testing"
)

func TestConceptLikeTermsExpandsEvaluationSynonyms(t *testing.T) {
	terms := conceptLikeTerms("锐评")
	joined := strings.Join(terms, "|")
	for _, want := range []string{"评价", "吐槽", "土狗", "装逼", "嘲讽"} {
		if !strings.Contains(joined, want) {
			t.Errorf("conceptLikeTerms(锐评) should include %q, got %v", want, terms)
		}
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
