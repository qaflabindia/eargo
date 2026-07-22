package ear

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// monitorRuntime is a minimal instance whose cycles complete, with an
// in-memory trail so the monitor has something to read.
func monitorRuntime(name string) *Runtime {
	r := NewRuntime(name)
	workflow := (&Workflow{Name: "Work"}).AddPersona(&Persona{Name: "Analyst", Instructions: "Analyse."})
	r.AddProcess((&Process{Name: "Process", Description: "Do the work."}).AddWorkflow(workflow))
	return r
}

// tamperFirstRecord forges the first record's output in place, breaking the
// hash chain -- the records live under Cycles[].Records, so a monitor test
// reaches through there rather than a flat slice.
func tamperFirstRecord(r *Runtime) bool {
	for c := range r.ReasoningLog.Cycles {
		if len(r.ReasoningLog.Cycles[c].Records) > 0 {
			r.ReasoningLog.Cycles[c].Records[0].Output = "forged"
			return true
		}
	}
	return false
}

func TestInspectHealthyRuntime(t *testing.T) {
	r := monitorRuntime("desk")
	r.Reason(context.Background(), NewIntent("Do the work.", nil), nil)

	health := InspectRuntime("desk", r, time.Now())
	if health.Status != Healthy {
		t.Errorf("a clean cycle should be healthy, got %s (%s)", health.Status, health.Reason)
	}
	if health.Cycles != 1 {
		t.Errorf("want 1 cycle, got %d", health.Cycles)
	}
	if health.Reason != "all clear" {
		t.Errorf("reason = %q", health.Reason)
	}
}

func TestPolicyBlockIsHealthyNotAFault(t *testing.T) {
	// The distinction the whole classifier rests on: governance refusing
	// something is the system working. Reporting it as a fault would train
	// operators to ignore the signal that actually means "act".
	r := monitorRuntime("desk")
	r.AddPolicy(&Policy{Name: "Hard Stop", Statement: "Nothing runs.", FallbackExpression: "false"})
	r.Reason(context.Background(), NewIntent("Do the work.", nil), nil)

	health := InspectRuntime("desk", r, time.Now())
	if health.Status != Healthy {
		t.Errorf("a policy block should stay healthy, got %s", health.Status)
	}
	if health.Blocked != 1 {
		t.Errorf("the block should be counted, got %d", health.Blocked)
	}
	if !strings.Contains(health.Reason, "governance working") {
		t.Errorf("reason should frame the block as governance, got %q", health.Reason)
	}
}

func TestParkedApprovalNeedsAttention(t *testing.T) {
	r := monitorRuntime("desk")
	r.AddPolicy(&Policy{
		Name: "Large Loan Approval", Statement: "Large loans need approval.",
		FallbackExpression: "loan_amount <= 50000", ApprovalRequired: true,
	})
	r.Reason(context.Background(), NewIntent("Underwrite a large loan",
		map[string]any{"loan_amount": 60000.0}), nil)

	health := InspectRuntime("desk", r, time.Now())
	if health.Status != Attention {
		t.Errorf("a parked gate needs a human, got %s", health.Status)
	}
	if health.Pending != 1 {
		t.Errorf("the pending gate should be counted, got %d", health.Pending)
	}
}

func TestBrokenChainIsTheOnlyHardFault(t *testing.T) {
	r := monitorRuntime("desk")
	r.Reason(context.Background(), NewIntent("Do the work.", nil), nil)

	// Tamper with a record after the fact: the chain must no longer verify.
	if !tamperFirstRecord(r) {
		t.Fatal("expected a record to tamper with")
	}

	health := InspectRuntime("desk", r, time.Now())
	if health.Status != Broken {
		t.Errorf("a broken chain must be broken, got %s (%s)", health.Status, health.Reason)
	}
	if health.ChainIntact {
		t.Error("the chain should not verify after tampering")
	}
}

func TestBrokenChainOutranksEverythingElse(t *testing.T) {
	// A runtime can be blocked, pending and broken at once; the record being
	// untrustworthy is the thing an operator has to see first.
	r := monitorRuntime("desk")
	r.AddPolicy(&Policy{Name: "Cap", Statement: "Capped.", FallbackExpression: "amount <= 100"})
	r.Reason(context.Background(), NewIntent("Do it", map[string]any{"amount": 1000.0}), nil)
	tamperFirstRecord(r)

	health := InspectRuntime("desk", r, time.Now())
	if health.Status != Broken {
		t.Errorf("broken must outrank blocked, got %s", health.Status)
	}
}

func TestFreshnessFromLastActivity(t *testing.T) {
	r := monitorRuntime("desk")
	r.Reason(context.Background(), NewIntent("Do the work.", nil), nil)

	if got := InspectRuntime("desk", r, time.Now()).Freshness; got != Active {
		t.Errorf("a just-run cycle is active, got %s", got)
	}
	if got := InspectRuntime("desk", r, time.Now().Add(time.Hour)).Freshness; got != Stale {
		t.Errorf("an hour on it is stale, got %s", got)
	}
	if got := InspectRuntime("desk", r, time.Now().Add(5*time.Minute)).Freshness; got != Idle {
		t.Errorf("five minutes on it is idle, got %s", got)
	}

	// A runtime that never ran has no heartbeat.
	if got := InspectRuntime("cold", monitorRuntime("cold"), time.Now()).Freshness; got != Idle {
		t.Errorf("a runtime that never ran is idle, got %s", got)
	}
}

func TestHealthRankOrders(t *testing.T) {
	if !(Broken.Rank() > Attention.Rank() && Attention.Rank() > Healthy.Rank()) {
		t.Error("broken > attention > healthy")
	}
}

// -- the fleet ----------------------------------------------------------------

func TestInspectFleetSortsWorstFirst(t *testing.T) {
	kernel := NewKernel()

	healthy := monitorRuntime("healthy")
	healthy.Reason(context.Background(), NewIntent("Do the work.", nil), nil)

	pending := monitorRuntime("pending")
	pending.AddPolicy(&Policy{Name: "Gate", Statement: "Needs approval.",
		FallbackExpression: "false", ApprovalRequired: true})
	pending.Reason(context.Background(), NewIntent("Do the work.", nil), nil)

	broken := monitorRuntime("broken")
	broken.Reason(context.Background(), NewIntent("Do the work.", nil), nil)
	tamperFirstRecord(broken)

	kernel.Register("healthy", healthy)
	kernel.Register("pending", pending)
	kernel.Register("broken", broken)

	fleet := InspectFleet(kernel, time.Now())
	if len(fleet.Instances) != 3 {
		t.Fatalf("want 3 instances, got %d", len(fleet.Instances))
	}
	// Worst first: the operator should not have to hunt.
	if fleet.Instances[0].Name != "broken" {
		t.Errorf("broken should sort first, got %s", fleet.Instances[0].Name)
	}
	if fleet.Instances[2].Name != "healthy" {
		t.Errorf("healthy should sort last, got %s", fleet.Instances[2].Name)
	}
	if fleet.Status() != Broken {
		t.Errorf("the fleet is only as well as its worst instance, got %s", fleet.Status())
	}
	if fleet.Broken != 1 {
		t.Errorf("want 1 broken, got %d", fleet.Broken)
	}
}

func TestFleetFoldsInKernelDispatchFailures(t *testing.T) {
	// A cycle that panicked never wrote a record saying so, which is exactly
	// why the failure comes from the kernel's own history.
	kernel := NewKernel()
	rt := monitorRuntime("exploding")
	rt.Reasoner = panickingReasoner{}
	kernel.Register("exploding", rt)
	kernel.Submit("exploding", NewIntent("Do the work.", nil))
	kernel.Drain(context.Background(), 0)

	fleet := InspectFleet(kernel, time.Now())
	if len(fleet.Instances) != 1 {
		t.Fatalf("want 1 instance, got %d", len(fleet.Instances))
	}
	instance := fleet.Instances[0]
	if instance.Failed != 1 {
		t.Errorf("the dispatch failure should be folded in, got %d", instance.Failed)
	}
	if instance.Status != Attention {
		t.Errorf("a failed cycle needs attention, got %s (%s)", instance.Status, instance.Reason)
	}
}

func TestInspectEmptyFleet(t *testing.T) {
	fleet := InspectFleet(NewKernel(), time.Now())
	if len(fleet.Instances) != 0 {
		t.Errorf("an empty kernel has no instances, got %d", len(fleet.Instances))
	}
	if fleet.Status() != Healthy {
		t.Errorf("an empty fleet is trivially healthy, got %s", fleet.Status())
	}
}

// -- rendering ----------------------------------------------------------------

func TestSparkline(t *testing.T) {
	// An all-zero series is flat, not full: "no activity" and "steady
	// activity" must never look the same.
	if got := Sparkline([]int{0, 0, 0}, 10); got != "▁▁▁" {
		t.Errorf("all-zero should be flat, got %q", got)
	}
	// A rising series rises.
	rising := Sparkline([]int{1, 4, 8}, 10)
	runes := []rune(rising)
	if len(runes) != 3 || runes[0] >= runes[2] {
		t.Errorf("a rising series should rise, got %q", rising)
	}
	// Width clips to the most recent values.
	if got := Sparkline([]int{1, 2, 3, 4, 5}, 2); len([]rune(got)) != 2 {
		t.Errorf("width should clip, got %q", got)
	}
	if got := Sparkline(nil, 10); got != "" {
		t.Errorf("an empty series renders empty, got %q", got)
	}
}

func TestRenderFleetIsSelfContained(t *testing.T) {
	kernel := NewKernel()
	rt := monitorRuntime("desk")
	rt.Reason(context.Background(), NewIntent("Do the work.", nil), nil)
	kernel.Register("desk", rt)

	frame := RenderFleet(InspectFleet(kernel, time.Now()))
	for _, want := range []string{"EAR FLEET", "HEALTHY", "desk", "INSTANCE", "1 cycles"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame omits %q\n%s", want, frame)
		}
	}

	empty := RenderFleet(InspectFleet(NewKernel(), time.Now()))
	if !strings.Contains(empty, "no instances") {
		t.Errorf("an empty fleet should say so, got:\n%s", empty)
	}
}

func TestInspectTrailFileUsesTheFileCanonicalVerifier(t *testing.T) {
	// A trail read back from disk cannot be verified with the in-memory chain
	// check: that re-marshals each record, and the JSON key order need not
	// match the stored bytes. An intact file must read intact -- the bug this
	// guards against reported every read-back trail as broken.
	dir := t.TempDir()
	path := filepath.Join(dir, "desk.jsonl")

	r := monitorRuntime("desk")
	trail, err := OpenTrailFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r.ReasoningLog.Trail = trail
	r.Reason(context.Background(), NewIntent("Do the work.", nil), nil)
	r.Reason(context.Background(), NewIntent("Do more work.", nil), nil)
	trail.Close()

	health, err := InspectTrailFile("desk", path, time.Now())
	if err != nil {
		t.Fatalf("InspectTrailFile: %v", err)
	}
	if !health.ChainIntact {
		t.Errorf("an untampered trail must read intact, got broken: %s", health.ChainDetail)
	}
	if health.Status == Broken {
		t.Errorf("an intact trail must not read broken, got %s", health.Reason)
	}
	if health.Cycles != 2 {
		t.Errorf("want 2 cycles from the file, got %d", health.Cycles)
	}

	// A genuinely altered file still reads broken.
	data, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(data), "Do the work.", "FORGED", 1)), 0o644)
	broken, err := InspectTrailFile("desk", path, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if broken.ChainIntact || broken.Status != Broken {
		t.Errorf("a tampered file must read broken, got %s (%s)", broken.Status, broken.Reason)
	}
}
