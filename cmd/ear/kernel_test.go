package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeScheduledStack lays down a stack whose memory.md authors standing work,
// plus a tenant.md so the boundary has something to enforce.
func writeScheduledStack(t *testing.T, orgID string) string {
	t.Helper()
	dir := writeStack(t)
	memory := "# Strategy\n\n## Reasoning Audit Trail\n\n" +
		"Log every reasoning step to `.ear/reasoning.jsonl`, append-only across sessions.\n\n" +
		"## Scheduled Work\n\n" +
		"- Every 15 minutes, reason \"Sweep the overnight application queue.\"\n" +
		"- Every 24 hours, reason \"Produce the daily underwriting summary.\"\n"
	if err := os.WriteFile(filepath.Join(dir, "memory.md"), []byte(memory), 0o644); err != nil {
		t.Fatal(err)
	}
	tenant := "## Test Org\n\nOrg id: " + orgID + "\n"
	if err := os.WriteFile(filepath.Join(dir, "tenant.md"), []byte(tenant), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestKernelOnceRunsAuthoredWork(t *testing.T) {
	dir := writeScheduledStack(t, "acme")
	if code := run([]string{"kernel", dir, "-once"}); code != exitDecided {
		t.Errorf("a one-shot run of authored work should exit %d, got %d", exitDecided, code)
	}
}

func TestKernelRefusesAStackWithNoAuthoredSchedule(t *testing.T) {
	// A kernel with nothing armed would sleep forever having been asked to do
	// nothing; that is a mis-authored memory.md, and it should be said so.
	dir := writeStack(t)
	if code := run([]string{"kernel", dir, "-once"}); code != exitError {
		t.Errorf("an unscheduled stack should exit %d, got %d", exitError, code)
	}
}

func TestKernelTenantBoundaryBlocksForeignClaim(t *testing.T) {
	dir := writeScheduledStack(t, "acme")

	if code := run([]string{"kernel", dir, "-once", "-subject", "svc:nightly", "-org", "acme"}); code != exitDecided {
		t.Errorf("an authorized claim should decide and exit %d, got %d", exitDecided, code)
	}
	if code := run([]string{"kernel", dir, "-once", "-subject", "svc:intruder", "-org", "globex"}); code != exitBlocked {
		t.Errorf("a foreign claim should block and exit %d, got %d", exitBlocked, code)
	}
}

func TestKernelClaimFlagsMustBePaired(t *testing.T) {
	dir := writeScheduledStack(t, "acme")
	if code := run([]string{"kernel", dir, "-once", "-subject", "svc:x"}); code != exitError {
		t.Errorf("-subject without -org should exit %d, got %d", exitError, code)
	}
	if code := run([]string{"kernel", dir, "-once", "-org", "acme"}); code != exitError {
		t.Errorf("-org without -subject should exit %d, got %d", exitError, code)
	}
}

func TestKernelUsageErrors(t *testing.T) {
	if code := run([]string{"kernel"}); code != exitError {
		t.Errorf("no stack directory should exit %d, got %d", exitError, code)
	}
	if code := run([]string{"kernel", "/nonexistent", "-once"}); code != exitError {
		t.Errorf("a missing stack should exit %d, got %d", exitError, code)
	}
}

func TestKernelRunsSeveralStacksAsOneFleet(t *testing.T) {
	a := writeScheduledStack(t, "acme")
	b := writeScheduledStack(t, "acme")
	if code := run([]string{"kernel", a, b, "-once", "-workers", "2"}); code != exitDecided {
		t.Errorf("a two-stack fleet should exit %d, got %d", exitDecided, code)
	}
}

func TestInstanceNameIsTheStackDirectory(t *testing.T) {
	if got := instanceName("/srv/stacks/underwriting"); got != "underwriting" {
		t.Errorf("want underwriting, got %q", got)
	}
	if got := instanceName("/srv/stacks/underwriting/"); got != "underwriting" {
		t.Errorf("a trailing separator should not change the name, got %q", got)
	}
}

func TestParseClaim(t *testing.T) {
	claim, err := parseClaim("svc:a", "acme, acme-eu")
	if err != nil {
		t.Fatalf("parseClaim: %v", err)
	}
	if claim.Subject != "svc:a" || len(claim.OrgIDs) != 2 || claim.OrgIDs[1] != "acme-eu" {
		t.Errorf("claim parsed wrong: %+v", claim)
	}

	none, err := parseClaim("", "")
	if err != nil || none != nil {
		t.Errorf("no flags should mean no claim, got %v %v", none, err)
	}
	if _, err := parseClaim("svc:a", " , "); err == nil {
		t.Error("-org naming no ids should error")
	}
}
