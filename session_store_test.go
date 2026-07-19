package ear

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// seed populates a runtime's three memory layers with something to round-trip:
// a compressed summary, a working entry with evidence and typed context, an
// experience count, and a distilled adaptation.
func seed(rt *Runtime) {
	rt.Memory.Compressed = []string{"3 earlier cycles about pricing"}
	rt.Memory.Record("approve the loan", "APPROVED", map[string]any{
		"loan_amount": float64(18500),
		"applicant":   "Dana",
		"expedited":   true,
	}, &Evidence{Basis: "cleared DTI and fraud checks", Sources: map[string]any{}, Confidence: 0.9})
	rt.Experience.ObserveEntry(rt.Memory.Working[0])
	rt.Adaptations.Add(&Adaptation{
		Name: "Bias-toward-approval", Insight: "Applicants under 0.43 DTI clear reliably.",
		Confidence: 0.8, EvidenceCount: 4,
	})
}

func assertRestored(t *testing.T, rt *Runtime) {
	t.Helper()
	if len(rt.Memory.Compressed) != 1 || rt.Memory.Compressed[0] != "3 earlier cycles about pricing" {
		t.Errorf("compressed = %#v", rt.Memory.Compressed)
	}
	if len(rt.Memory.Working) != 1 {
		t.Fatalf("working entries = %d, want 1", len(rt.Memory.Working))
	}
	entry := rt.Memory.Working[0]
	if entry.IntentText != "approve the loan" {
		t.Errorf("intent = %q", entry.IntentText)
	}
	if entry.Decision != "APPROVED" {
		t.Errorf("decision = %v", entry.Decision)
	}
	if entry.Context["loan_amount"] != float64(18500) {
		t.Errorf("loan_amount coerced to %T %v, want float64 18500", entry.Context["loan_amount"], entry.Context["loan_amount"])
	}
	if entry.Context["expedited"] != true {
		t.Errorf("expedited coerced to %T %v, want bool true", entry.Context["expedited"], entry.Context["expedited"])
	}
	if entry.Evidence == nil || entry.Evidence.Basis != "cleared DTI and fraud checks" || entry.Evidence.Confidence != 0.9 {
		t.Errorf("evidence = %#v", entry.Evidence)
	}
	if rt.Experience.Observations != 1 || rt.Experience.DecisionCounts["APPROVED"] != 1 {
		t.Errorf("experience = %d obs, counts %#v", rt.Experience.Observations, rt.Experience.DecisionCounts)
	}
	if len(rt.Adaptations.Impressions) != 1 {
		t.Fatalf("adaptations = %d, want 1", len(rt.Adaptations.Impressions))
	}
	a := rt.Adaptations.Impressions[0]
	if a.Name != "Bias-toward-approval" || a.Insight != "Applicants under 0.43 DTI clear reliably." ||
		a.Confidence != 0.8 || a.EvidenceCount != 4 {
		t.Errorf("adaptation = %#v", a)
	}
}

func TestSessionStoreMarkdownRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "session.md")
	saver := NewRuntime("Underwriter")
	seed(saver)
	if _, err := (&SessionStore{Path: path}).Save(saver); err != nil {
		t.Fatal(err)
	}

	fresh := NewRuntime("Underwriter")
	if ok := (&SessionStore{Path: path}).Restore(fresh); !ok {
		t.Fatal("restore reported nothing usable")
	}
	assertRestored(t, fresh)
}

func TestSessionStoreJSONRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	saver := NewRuntime("Underwriter")
	seed(saver)
	if _, err := (&SessionStore{Path: path}).Save(saver); err != nil {
		t.Fatal(err)
	}

	fresh := NewRuntime("Underwriter")
	if ok := (&SessionStore{Path: path}).Restore(fresh); !ok {
		t.Fatal("restore reported nothing usable")
	}
	assertRestored(t, fresh)
}

func TestSessionStoreMissingIsNotFatal(t *testing.T) {
	store := &SessionStore{Path: filepath.Join(t.TempDir(), "absent.md")}
	rt := NewRuntime("R")
	if store.Restore(rt) {
		t.Error("restoring a missing store should report false")
	}
}

func TestSessionStoreCorruptJSONIsNotFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime("R")
	if (&SessionStore{Path: path}).Restore(rt) {
		t.Error("restoring corrupt JSON should report false, leaving the runtime cold")
	}
	if rt.Memory.Len() != 0 {
		t.Error("a failed restore must not touch the runtime")
	}
}

func TestReasonSavesAfterEachCycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.md")
	rt := NewRuntime("R")
	rt.SessionStore = &SessionStore{Path: path}
	proc := &Process{Name: "P", Description: "Do."}
	proc.AddWorkflow((&Workflow{Name: "W"}).AddStep("Decide.", nil))
	rt.AddProcess(proc)

	if _, err := rt.Reason(context.Background(), NewIntent("first intent", nil), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected the store written after the cycle: %v", err)
	}

	// A fresh runtime restoring the store sees the cycle the first one recorded.
	fresh := NewRuntime("R")
	if !(&SessionStore{Path: path}).Restore(fresh) {
		t.Fatal("restore reported nothing usable")
	}
	if len(fresh.Memory.Working) == 0 || fresh.Memory.Working[0].IntentText != "first intent" {
		t.Errorf("restored working memory = %#v", fresh.Memory.Working)
	}
}

func TestLoaderWiresSessionStoreFromMemoryMd(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("process.md", "# Desk\n\n## Handle\n\nHandle requests.\n\n- W\n\n## W\n\nDecide.\n")
	write("workflow.md", "## W\n\n1. Decide the request.\n")
	write("memory.md", "# Strategy\n\n## Cross-Session Data\n\nPersist the session to `state/session.md`.\n")

	// First load + cycle writes the store.
	rt, err := LoadRuntime(dir, "Desk")
	if err != nil {
		t.Fatal(err)
	}
	if rt.SessionStore == nil {
		t.Fatal("memory.md declared a session store but none was wired")
	}
	if _, err := rt.Reason(context.Background(), NewIntent("resolve a request", nil), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state", "session.md")); err != nil {
		t.Fatalf("declared store not written: %v", err)
	}

	// A second load restores what the first session left behind.
	rt2, err := LoadRuntime(dir, "Desk")
	if err != nil {
		t.Fatal(err)
	}
	if len(rt2.Memory.Working) == 0 {
		t.Error("second load should restore the first session's memory")
	}
}

func TestLabelledBlocksReadsQuotedLabels(t *testing.T) {
	lines := []string{
		"Intent:",
		"> approve the loan",
		"> for Dana",
		"",
		"Decision:",
		"> APPROVED",
	}
	blocks := labelledBlocks(lines)
	if blocks["intent"] != "approve the loan\nfor Dana" {
		t.Errorf("intent block = %q", blocks["intent"])
	}
	if blocks["decision"] != "APPROVED" {
		t.Errorf("decision block = %q", blocks["decision"])
	}
}
