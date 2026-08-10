package main

import "testing"

func TestAIQAStepModelNames(t *testing.T) {
	prefs := Preferences{
		LLMProfiles: []LLMProfile{
			{ID: "fast", Provider: "custom", Model: "fast-model"},
			{ID: "capable", Provider: "custom", Model: "capable-model", FastMode: true},
		},
		DefaultLLMProfileID: "fast",
		AIQALLMProfiles: AIQALLMProfiles{
			QueryDecomposition: "fast",
			QueryExpansion:     "capable",
			FinalAnswer:        "capable",
		},
	}

	got := aiQAStepModelNames(prefs, "")
	if got.QueryDecomposition != "fast-model" {
		t.Fatalf("query decomposition model = %q, want fast-model", got.QueryDecomposition)
	}
	if got.QueryExpansion != "capable-model" {
		t.Fatalf("query expansion model = %q, want capable-model", got.QueryExpansion)
	}
	if got.FinalAnswer != "capable-model" {
		t.Fatalf("final answer model = %q, want capable-model", got.FinalAnswer)
	}
	if got.QueryDecompositionFast {
		t.Fatal("query decomposition unexpectedly marked Fast")
	}
	if !got.QueryExpansionFast || !got.FinalAnswerFast {
		t.Fatalf("Fast model flags = %+v, want query expansion and final answer only", got)
	}
}
