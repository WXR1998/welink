package main

import "testing"

func TestAIQAStepProfileID(t *testing.T) {
	prefs := Preferences{
		LLMProfiles: []LLMProfile{
			{ID: "fast"},
			{ID: "capable"},
		},
		AIQALLMProfiles: AIQALLMProfiles{
			QueryDecomposition: "fast",
			QueryExpansion:     "missing",
			SourceSelection:    "",
			FinalAnswer:        "capable",
		},
	}

	tests := []struct {
		name      string
		step      string
		requestID string
		want      string
	}{
		{name: "uses configured valid profile", step: "query_decomposition", requestID: "capable", want: "fast"},
		{name: "empty override falls back to request profile", step: "source_selection", requestID: "fast", want: "fast"},
		{name: "deleted override falls back to request profile", step: "query_expansion", requestID: "capable", want: "capable"},
		{name: "final answer uses configured profile", step: "final_answer", requestID: "fast", want: "capable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := aiQAStepProfileID(prefs, tt.step, tt.requestID); got != tt.want {
				t.Fatalf("aiQAStepProfileID(%q, %q) = %q, want %q", tt.step, tt.requestID, got, tt.want)
			}
		})
	}
}
