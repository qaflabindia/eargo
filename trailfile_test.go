package ear

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// trailRuntime builds a minimal runtime whose ReasoningLog persists to path.
func trailRuntime(t *testing.T, path string) *Runtime {
	t.Helper()
	trail, err := OpenTrailFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { trail.Close() })
	rt := NewRuntime("R")
	rt.ReasoningLog.Trail = trail
	rt.ReasoningLog.SeedCycleNumbering(trail.MaxCycle())
	proc := &Process{Name: "P", Description: "Decide."}
	proc.AddWorkflow((&Workflow{Name: "W"}).AddStep("Decide.", nil))
	rt.AddProcess(proc)
	return rt
}

func TestTrailFileJSONLVerifies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trail.jsonl")
	rt := trailRuntime(t, path)
	if _, err := rt.Reason(context.Background(), NewIntent("decide something", nil), nil); err != nil {
		t.Fatal(err)
	}
	ok, detail := VerifyTrail(path)
	if !ok {
		t.Fatalf("fresh trail should verify: %s", detail)
	}
	// The persisted trail carries the intent itself.
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "decide something") {
		t.Error("the trail should record what was asked")
	}
}

func TestTrailFileDetectsTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trail.jsonl")
	rt := trailRuntime(t, path)
	if _, err := rt.Reason(context.Background(), NewIntent("decide", nil), nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	tampered := strings.Replace(string(data), "decide", "decree", 1)
	if tampered == string(data) {
		t.Fatal("tamper had no effect")
	}
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, detail := VerifyTrail(path)
	if ok {
		t.Fatal("a tampered trail must not verify")
	}
	if !strings.Contains(detail, "record 1") {
		t.Errorf("the first record was altered; detail = %q", detail)
	}
}

func TestTrailFileResumesAcrossSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trail.jsonl")

	first := trailRuntime(t, path)
	if _, err := first.Reason(context.Background(), NewIntent("first session", nil), nil); err != nil {
		t.Fatal(err)
	}
	first.ReasoningLog.Trail.(*TrailFile).Close()

	// A second session opens the same file: the chain links its first record
	// to the last persisted one, and cycle numbering continues.
	second := trailRuntime(t, path)
	if _, err := second.Reason(context.Background(), NewIntent("second session", nil), nil); err != nil {
		t.Fatal(err)
	}
	second.ReasoningLog.Trail.(*TrailFile).Close()

	ok, detail := VerifyTrail(path)
	if !ok {
		t.Fatalf("a resumed trail should verify end to end: %s", detail)
	}
	log, err := ReadTrail(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(log.Cycles) != 2 {
		t.Fatalf("cycles = %d, want 2", len(log.Cycles))
	}
	if log.Cycles[0].IntentText != "first session" || log.Cycles[1].IntentText != "second session" {
		t.Errorf("cycle intents = %q, %q", log.Cycles[0].IntentText, log.Cycles[1].IntentText)
	}
	// Cycle numbers never repeat inside one trail.
	if n := log.Cycles[1].Records[0].Cycle; n != 2 {
		t.Errorf("second session's cycle number = %d, want 2", n)
	}
}

func TestTrailFileMarkdownCodec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trail.md")
	rt := trailRuntime(t, path)
	if _, err := rt.Reason(context.Background(), NewIntent("decide in markdown", nil), nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.Contains(text, "## Cycle 1") {
		t.Error("markdown trail should carry cycle headings")
	}
	if !strings.Contains(text, "### intent") || !strings.Contains(text, "decide in markdown") {
		t.Error("markdown trail should carry the intent record readably")
	}
	if !strings.Contains(text, "<!-- chain: ") {
		t.Error("markdown trail should carry chain hashes in comments")
	}
	ok, detail := VerifyTrail(path)
	if !ok {
		t.Fatalf("markdown trail should verify: %s", detail)
	}
	// Tampering with a record's visible text breaks the chain.
	tampered := strings.Replace(text, "decide in markdown", "decree in markdown", 1)
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _ := VerifyTrail(path); ok {
		t.Error("a tampered markdown trail must not verify")
	}
}

func TestReadTrailUsageReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trail.jsonl")
	rt := trailRuntime(t, path)
	for _, intent := range []string{"first", "second"} {
		if _, err := rt.Reason(context.Background(), NewIntent(intent, nil), nil); err != nil {
			t.Fatal(err)
		}
	}
	log, err := ReadTrail(path)
	if err != nil {
		t.Fatal(err)
	}
	report := log.UsageReport()
	if !strings.Contains(report, "| 1 |") || !strings.Contains(report, "| 2 |") {
		t.Errorf("the offline ledger should carry one row per cycle:\n%s", report)
	}
}

func TestLoaderWiresAuditTrailFromMemoryMd(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("process.md", "# Desk\n\n## Handle\n\nHandle requests.\n\n- W\n\n## W\n\nDecide.\n")
	write("workflow.md", "## W\n\n1. Decide.\n")
	write("memory.md", "# Strategy\n\n## Reasoning Audit Trail\n\nLog every reasoning step to `.ear/reasoning.jsonl`, append-only across sessions.\n")

	rt, err := LoadRuntime(dir, "Desk")
	if err != nil {
		t.Fatal(err)
	}
	if rt.ReasoningLog.Trail == nil {
		t.Fatal("memory.md declared an audit trail but none was wired")
	}
	if _, err := rt.Reason(context.Background(), NewIntent("resolve", nil), nil); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}

	trailPath := filepath.Join(dir, ".ear", "reasoning.jsonl")
	if ok, detail := VerifyTrail(trailPath); !ok {
		t.Fatalf("declared trail should exist and verify: %s", detail)
	}

	// A second load resumes the trail; the whole file still verifies as one
	// chain and the new cycle takes the next number.
	rt2, err := LoadRuntime(dir, "Desk")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt2.Reason(context.Background(), NewIntent("resolve again", nil), nil); err != nil {
		t.Fatal(err)
	}
	if err := rt2.Close(); err != nil {
		t.Fatal(err)
	}
	if ok, detail := VerifyTrail(trailPath); !ok {
		t.Fatalf("resumed trail should verify end to end: %s", detail)
	}
	log, err := ReadTrail(trailPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(log.Cycles) != 2 {
		t.Errorf("persisted cycles = %d, want 2 across the two sessions", len(log.Cycles))
	}
}

func TestRuntimeCloseIsSafeWithoutTrail(t *testing.T) {
	rt := NewRuntime("R")
	if err := rt.Close(); err != nil {
		t.Fatalf("closing a hand-built runtime: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("double close: %v", err)
	}
}
