package ear

import (
	"context"
	"slices"
	"testing"
)

func TestDefaultPipelineOrder(t *testing.T) {
	rt := NewRuntime("R")
	got := rt.PipelineNames()
	want := []string{
		"govern", "discover", "select", "compose", "schedule", "govern",
		"delegate", "recall", "reason", "formalize", "evidence", "explain",
		"audit", "memory",
	}
	if !slices.Equal(got, want) {
		t.Errorf("pipeline = %v\nwant       %v", got, want)
	}
}

// countingStage records that it ran -- a custom stage a caller composes into
// the pipeline.
type countingStage struct{ ran *int }

func (countingStage) Name() string         { return "counting" }
func (s countingStage) Run(_ *Cycle) error { *s.ran++; return nil }

func TestPipelineIsComposable(t *testing.T) {
	rt := buildRuntime()
	ran := 0
	// Insert a custom stage at the front and drop the audit stage -- the
	// pipeline is just data.
	custom := []Stage{countingStage{&ran}}
	for _, s := range rt.Pipeline {
		if s.Name() == "audit" {
			continue
		}
		custom = append(custom, s)
	}
	rt.Pipeline = custom

	if _, err := rt.Reason(context.Background(), NewIntent("Underwrite a loan",
		map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28}), nil); err != nil {
		t.Fatalf("cycle errored: %v", err)
	}
	if ran != 1 {
		t.Errorf("custom stage ran %d times, want 1", ran)
	}
	if slices.Contains(rt.PipelineNames(), "audit") {
		t.Error("audit stage should have been removed from the pipeline")
	}
}

// haltStage aborts the cycle, proving a stage error stops the pipeline.
type haltStage struct{}

func (haltStage) Name() string       { return "halt" }
func (haltStage) Run(_ *Cycle) error { return context.Canceled }

func TestStageErrorAbortsPipeline(t *testing.T) {
	rt := buildRuntime()
	// Put a halting stage first; the decision must never be produced.
	rt.Pipeline = append([]Stage{haltStage{}}, rt.Pipeline...)
	decision, err := rt.Reason(context.Background(), NewIntent("go", map[string]any{"debt_to_income": 0.1}), nil)
	if err == nil {
		t.Fatal("expected the halting stage to abort the cycle")
	}
	if decision != nil {
		t.Errorf("decision should be nil on abort, got %v", decision)
	}
}
