package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ear "github.com/qaflabindia/ear"
)

// writeStack lays down a minimal governed stack: one process, one workflow,
// a hard cap policy, an approval-gated policy, and a persisted trail.
func writeStack(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"process.md": "# Lending Desk\n\n## Underwrite\n\nUnderwrite a consumer loan.\n\nWorkflows: Underwriting\n",
		"workflow.md": "## Underwriting\n\n1. Decide approve or decline.\n\n" +
			"Policies: Loan Amount Cap, Large Loan Approval\n",
		"policy.md": "# Policies\n\n## Loan Amount Cap\n\nThe loan must not exceed $75,000.\n\n" +
			"Fallback: loan_amount <= 75000\n\n" +
			"## Large Loan Approval\n\nLoans above $50,000 need a human approval.\n\n" +
			"Fallback: loan_amount <= 50000\nApproval: required\n",
		"memory.md": "# Strategy\n\n## Reasoning Audit Trail\n\n" +
			"Log every reasoning step to `.ear/reasoning.jsonl`, append-only across sessions.\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRunExitCodes(t *testing.T) {
	dir := writeStack(t)

	if code := run([]string{"run", dir, "Underwrite a small loan", "loan_amount=20000"}); code != exitDecided {
		t.Errorf("a compliant run should exit %d, got %d", exitDecided, code)
	}
	if code := run([]string{"run", dir, "Underwrite an oversized loan", "loan_amount=90000"}); code != exitBlocked {
		t.Errorf("a policy block should exit %d, got %d", exitBlocked, code)
	}
	if code := run([]string{"run", dir, "Underwrite a large loan", "loan_amount=60000"}); code != exitApproval {
		t.Errorf("a parked approval should exit %d, got %d", exitApproval, code)
	}
	if code := run([]string{"run", dir, "Underwrite a large loan", "loan_amount=60000", "-approve", "-approver", "riya"}); code != exitDecided {
		t.Errorf("an approved gate should decide and exit %d, got %d", exitDecided, code)
	}
	if code := run([]string{"run", dir, "Underwrite a large loan", "loan_amount=60000", "-reject"}); code != exitBlocked {
		t.Errorf("a rejected gate should block and exit %d, got %d", exitBlocked, code)
	}
	if code := run([]string{"run", "/nonexistent", "intent"}); code != exitError {
		t.Errorf("a missing stack should exit %d, got %d", exitError, code)
	}
}

func TestRunFlagsAfterPositionals(t *testing.T) {
	dir := writeStack(t)
	// Flags work wherever they appear, including after the positionals, and
	// the -c flag form carries context facts too.
	if code := run([]string{"run", dir, "Underwrite", "-c", "loan_amount=20000", "-json"}); code != exitDecided {
		t.Errorf("flags after positionals should parse; exit = %d", code)
	}
	if code := run([]string{"run", "-c", "loan_amount=90000", dir, "Underwrite"}); code != exitBlocked {
		t.Errorf("flags before positionals should also parse; exit = %d", code)
	}
}

func TestReorderArgs(t *testing.T) {
	flags, positionals := reorderArgs(
		[]string{"dir", "intent", "-approve", "-approver", "riya", "-note=looks fine", "amount=5"},
		map[string]bool{"approver": true, "note": true},
	)
	wantFlags := []string{"-approve", "-approver", "riya", "-note=looks fine"}
	wantPos := []string{"dir", "intent", "amount=5"}
	if strings.Join(flags, "|") != strings.Join(wantFlags, "|") {
		t.Errorf("flags = %v", flags)
	}
	if strings.Join(positionals, "|") != strings.Join(wantPos, "|") {
		t.Errorf("positionals = %v", positionals)
	}
}

func TestTrailAndVerifyCommands(t *testing.T) {
	dir := writeStack(t)
	if code := run([]string{"run", dir, "Underwrite a small loan", "loan_amount=20000"}); code != exitDecided {
		t.Fatal("seed run failed")
	}
	trailPath := filepath.Join(dir, ".ear", "reasoning.jsonl")

	// verify accepts both the stack dir and the file itself.
	if code := run([]string{"verify", dir}); code != exitDecided {
		t.Errorf("verify on the stack dir should exit %d", exitDecided)
	}
	if code := run([]string{"verify", trailPath}); code != exitDecided {
		t.Errorf("verify on the file should exit %d", exitDecided)
	}
	if code := run([]string{"trail", dir}); code != exitDecided {
		t.Errorf("trail should render and exit %d", exitDecided)
	}
	if code := run([]string{"usage", dir}); code != exitDecided {
		t.Errorf("usage should render and exit %d", exitDecided)
	}

	// A tampered trail turns verify into a failure exit.
	data, _ := os.ReadFile(trailPath)
	tampered := strings.Replace(string(data), "small", "smell", 1)
	if err := os.WriteFile(trailPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"verify", dir}); code != exitBlocked {
		t.Errorf("a broken chain should exit %d", exitBlocked)
	}
}

func TestInspectCommand(t *testing.T) {
	dir := writeStack(t)
	if code := run([]string{"inspect", dir}); code != exitDecided {
		t.Errorf("inspect should exit %d", exitDecided)
	}
}

func TestDemoAndHelp(t *testing.T) {
	if code := run([]string{"demo"}); code != exitDecided {
		t.Error("demo should succeed")
	}
	if code := run([]string{"help"}); code != exitDecided {
		t.Error("help should succeed")
	}
	if code := run([]string{"no-such-command"}); code != exitError {
		t.Error("an unknown command should exit with a usage error")
	}
	if code := run(nil); code != exitError {
		t.Error("no arguments should exit with a usage error")
	}
}

func TestParseContextPairs(t *testing.T) {
	context, err := parseContextPairs([]string{"loan_amount=18500", "expedited=yes", "note=first home"})
	if err != nil {
		t.Fatal(err)
	}
	if context["loan_amount"] != float64(18500) {
		t.Errorf("loan_amount = %T %v", context["loan_amount"], context["loan_amount"])
	}
	if context["expedited"] != true {
		t.Errorf("expedited = %v", context["expedited"])
	}
	if context["note"] != "first home" {
		t.Errorf("note = %v", context["note"])
	}
	if _, err := parseContextPairs([]string{"not-a-pair"}); err == nil {
		t.Error("a fact without '=' should be rejected")
	}
}

func TestClassifyOutcome(t *testing.T) {
	rt := ear.NewRuntime("R")
	if got := classifyOutcome(rt, "APPROVED", nil); got.Status != "decided" || got.Decision != "APPROVED" {
		t.Errorf("decided outcome = %+v", got)
	}
	blocked := classifyOutcome(rt, nil, &ear.PolicyViolationError{Scope: "runtime", Policies: []*ear.Policy{{Name: "Cap"}}})
	if blocked.Status != "blocked" || blocked.BlockedPolicies[0] != "Cap" {
		t.Errorf("blocked outcome = %+v", blocked)
	}
	parked := classifyOutcome(rt, nil, &ear.ApprovalRequiredError{Policies: []*ear.Policy{{Name: "Gate"}}})
	if parked.Status != "approval_required" || parked.PendingPolicies[0] != "Gate" {
		t.Errorf("parked outcome = %+v", parked)
	}
	if parked.exitCode() != exitApproval || blocked.exitCode() != exitBlocked {
		t.Error("exit codes should follow the outcome status")
	}
}
