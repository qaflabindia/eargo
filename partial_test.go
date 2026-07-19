package ear

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDaysInProse(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"after 3 days", 3, true},
		{"after 2 weeks", 14, true},
		{"weekly", 7, true},
		{"monthly", 30, true},
		{"soonish", 0, false},
	}
	for _, tc := range cases {
		got, ok := daysInProse(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("daysInProse(%q) = %v,%v want %v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCountInProse(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"retry a failed leg twice", 2, true},
		{"retry 3 times", 3, true},
		{"no retries", 0, true},
		{"several times", 0, false},
	}
	for _, tc := range cases {
		got, ok := countInProse(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("countInProse(%q) = %v,%v want %v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestLoaderParsesEscalationDays(t *testing.T) {
	doc := ParseDocument("# Policies\n\n## Gate\n\nFallback: x <= 1\nApproval: required\nEscalate: after 3 days\n\nMust approve.\n")
	policies, _, err := loadPolicies(doc)
	if err != nil {
		t.Fatalf("loadPolicies error: %v", err)
	}
	p := policies["gate"]
	if p == nil || p.EscalationDays == nil || *p.EscalationDays != 3 {
		t.Fatalf("EscalationDays = %v", p.EscalationDays)
	}
}

func TestLoaderRejectsUnreadableEscalation(t *testing.T) {
	doc := ParseDocument("# Policies\n\n## Gate\n\nEscalate: soonish\n\nMust approve.\n")
	if _, _, err := loadPolicies(doc); err == nil {
		t.Fatal("expected an error on an unreadable Escalate field")
	}
}

func TestLoaderParsesRetries(t *testing.T) {
	doc := ParseDocument("# Workflows\n\n## W\n\nRetries: retry a failed leg twice\n\n1. Do the thing.\n")
	workflows, _, err := loadWorkflows(doc, map[string]*Persona{}, map[string]*Policy{})
	if err != nil {
		t.Fatalf("loadWorkflows error: %v", err)
	}
	w := workflows["w"]
	if w == nil || w.RetryBudget == nil || *w.RetryBudget != 2 {
		t.Fatalf("RetryBudget = %v", w.RetryBudget)
	}
}

func TestLoaderRejectsUnreadableRetries(t *testing.T) {
	doc := ParseDocument("# Workflows\n\n## W\n\nRetries: several times\n\n1. Do the thing.\n")
	if _, _, err := loadWorkflows(doc, map[string]*Persona{}, map[string]*Policy{}); err == nil {
		t.Fatal("expected an error on an unreadable Retries field")
	}
}

func TestContractStructuralJudge(t *testing.T) {
	c := &Contract{Name: "Deliverable"}
	c.AddField("risk grade", "a letter A-E")
	c.AddField("decision", "approve or decline")

	conforms, rationale := c.Judge(map[string]any{"risk grade": "B", "decision": "approve"})
	if !conforms {
		t.Errorf("expected structural conformance, got: %s", rationale)
	}
	conforms, rationale = c.Judge(map[string]any{"risk grade": "B"})
	if conforms {
		t.Error("expected nonconformance for a missing field")
	}
	if !strings.Contains(rationale, "decision") {
		t.Errorf("rationale should name the missing field: %s", rationale)
	}
}

func TestStrategyFromMarkdown(t *testing.T) {
	md := "# Memory & Strategy\n\n" +
		"## Context History\n\nKeep the 30 most recent cycles verbatim.\n\n" +
		"## Reasoning Audit Trail\n\nLog every step to `.ear/reasoning.md`. Keep 90 days.\n\n" +
		"## Tools\n\n- amortization_calculator: computes the monthly payment\n" +
		"- document_checker: verifies the file, via `checker`\n\n" +
		"## Subagent Spawning\n\nAllow spawning up to 4 subagents.\n\n" +
		"## Ontological Settings\n\n- risk grade: a letter from A to E\n"
	s := StrategyFromMarkdown(md)
	if s.HistoryCapacity != 30 {
		t.Errorf("HistoryCapacity = %d, want 30", s.HistoryCapacity)
	}
	if s.RetentionDays != 90 {
		t.Errorf("RetentionDays = %v, want 90", s.RetentionDays)
	}
	if len(s.Tools) != 2 {
		t.Fatalf("Tools = %d, want 2", len(s.Tools))
	}
	if s.Tools[1].Command != "checker" {
		t.Errorf("Tools[1].Command = %q, want checker", s.Tools[1].Command)
	}
	if s.MaxSubagents != 4 {
		t.Errorf("MaxSubagents = %d, want 4", s.MaxSubagents)
	}
	if s.Ontology.Terms["risk grade"] == "" {
		t.Errorf("ontology missing 'risk grade': %+v", s.Ontology)
	}
}

func TestRetentionRotation(t *testing.T) {
	log := &ReasoningLog{}
	now := time.Now()
	log.Cycles = append(log.Cycles,
		Cycle{IntentText: "old", Started: now.Add(-100 * 24 * time.Hour)},
		Cycle{IntentText: "recent", Started: now.Add(-1 * time.Hour)},
	)
	removed := log.Rotate(90, now)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if len(log.Cycles) != 1 || log.Cycles[0].IntentText != "recent" {
		t.Errorf("cycles after rotate = %+v", log.Cycles)
	}
}

func TestCycleRecordsUsageAndContractSkip(t *testing.T) {
	guru := &Persona{Name: "Guru"}
	w := &Workflow{Name: "W"}
	w.AddStep("Decide.", guru)
	w.Contract = (&Contract{Name: "W Deliverable"}).AddField("decision", "approve or decline")
	proc := &Process{Name: "P", Description: "Do a thing."}
	proc.AddWorkflow(w)
	rt := NewRuntime("R")
	rt.AddProcess(proc)

	if _, err := rt.Reason(context.Background(), NewIntent("go", nil), nil); err != nil {
		t.Fatalf("cycle errored: %v", err)
	}

	var sawUsage, sawContract bool
	for rec := range rt.ReasoningLog.Records() {
		switch rec.Stage {
		case "usage":
			sawUsage = true
			if !strings.Contains(rec.Output, "deterministic fallbacks") {
				t.Errorf("usage output = %q", rec.Output)
			}
		case "contract":
			sawContract = true
			if !strings.Contains(rec.Output, "skipped") {
				t.Errorf("contract output = %q", rec.Output)
			}
		}
	}
	if !sawUsage {
		t.Error("expected a usage record on the trail")
	}
	if !sawContract {
		t.Error("expected a contract skip record on the trail")
	}
}

func TestBlockedCycleStillRecordsUsage(t *testing.T) {
	rt := buildRuntime()
	// Over the DTI ceiling -> hard block, but usage must still be recorded.
	_, _ = rt.Reason(context.Background(), NewIntent("Underwrite a loan",
		map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.60}), nil)
	var sawUsage bool
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "usage" {
			sawUsage = true
		}
	}
	if !sawUsage {
		t.Error("a blocked cycle should still record usage -- a refusal costs whatever it cost")
	}
}
