package ear

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// workingRuntime is a minimal instance whose cycles complete.
func workingRuntime(name string) *Runtime {
	r := NewRuntime(name)
	workflow := (&Workflow{Name: "Work"}).AddPersona(&Persona{Name: "Analyst", Instructions: "Analyse."})
	r.AddProcess((&Process{Name: "Process", Description: "Do the work."}).AddWorkflow(workflow))
	return r
}

// blockedRuntime is an instance whose every cycle is stopped by policy.
func blockedRuntime(name string) *Runtime {
	r := workingRuntime(name)
	r.AddPolicy(&Policy{Name: "Hard Stop", Statement: "Nothing may run.", FallbackExpression: "false"})
	return r
}

func TestKernelDispatchesSubmittedWork(t *testing.T) {
	k := NewKernel()
	k.Register("underwriting", workingRuntime("underwriting"))
	k.Submit("underwriting", NewIntent("Grade the applications.", nil))

	done := k.Drain(context.Background(), 0)

	if len(done) != 1 {
		t.Fatalf("want 1 dispatch, got %d", len(done))
	}
	if done[0].Status != StatusRan {
		t.Errorf("want %q, got %q (%s)", StatusRan, done[0].Status, done[0].Summary)
	}
	if k.Pending() != 0 {
		t.Errorf("a one-shot task should leave the queue empty, got %d", k.Pending())
	}
}

func TestKernelIdlesWhenThereIsNoWork(t *testing.T) {
	k := NewKernel()
	if dispatch := k.Tick(context.Background()); dispatch != nil {
		t.Fatalf("an empty kernel must report idle, got %v", dispatch)
	}
	if snap := k.Snapshot(); snap.IdleWaits != 1 {
		t.Errorf("idle turns should be counted, got %d", snap.IdleWaits)
	}
}

func TestKernelGovernanceStopParksRatherThanFails(t *testing.T) {
	// The distinction the whole control plane rests on: a policy refusal is
	// the system working, not a fault.
	k := NewKernel()
	k.Register("locked", blockedRuntime("locked"))
	k.Submit("locked", NewIntent("Do the thing.", nil))

	done := k.Drain(context.Background(), 0)

	if len(done) != 1 {
		t.Fatalf("want 1 dispatch, got %d", len(done))
	}
	if done[0].Status != StatusBlocked {
		t.Errorf("a violated policy must land %q, got %q (%s)", StatusBlocked, done[0].Status, done[0].Summary)
	}
}

func TestKernelTenantRefusalParksRatherThanFails(t *testing.T) {
	k := NewKernel()
	k.Register("acme", tenantRuntime("acme"))
	k.Submit("acme", NewIntent("Read the loan book.", nil),
		ActingAs(Claim{Subject: "svc:intruder", OrgIDs: []string{"globex"}}))

	done := k.Drain(context.Background(), 0)

	if len(done) != 1 || done[0].Status != StatusBlocked {
		t.Fatalf("a cross-tenant task must land blocked, got %+v", done)
	}
}

func TestKernelAuthorizedClaimRuns(t *testing.T) {
	k := NewKernel()
	k.Register("acme", tenantRuntime("acme"))
	k.Submit("acme", NewIntent("Grade the applications.", nil),
		ActingAs(Claim{Subject: "svc:nightly", OrgIDs: []string{"acme"}}))

	done := k.Drain(context.Background(), 0)

	if len(done) != 1 || done[0].Status != StatusRan {
		t.Fatalf("an authorized task should run, got %+v", done)
	}
}

func TestKernelUnknownInstanceFailsWithoutStopping(t *testing.T) {
	k := NewKernel()
	k.Register("real", workingRuntime("real"))
	k.Submit("ghost", NewIntent("Do the thing.", nil))
	k.Submit("real", NewIntent("Do the thing.", nil))

	done := k.Drain(context.Background(), 0)

	if len(done) != 2 {
		t.Fatalf("one bad task must not stop the rest, got %d dispatches", len(done))
	}
	byInstance := map[string]DispatchStatus{}
	for _, d := range done {
		byInstance[d.Instance] = d.Status
	}
	if byInstance["ghost"] != StatusFailed {
		t.Errorf("unknown instance should fail, got %q", byInstance["ghost"])
	}
	if byInstance["real"] != StatusRan {
		t.Errorf("the healthy instance should still run, got %q", byInstance["real"])
	}
}

func TestKernelPanicDoesNotTakeTheKernelDown(t *testing.T) {
	// A control plane meant never to stop cannot let a seam's panic reach the
	// goroutine boundary and kill the process.
	k := NewKernel()
	rt := workingRuntime("exploding")
	rt.Reasoner = panickingReasoner{}
	k.Register("exploding", rt)
	k.Submit("exploding", NewIntent("Do the thing.", nil))

	done := k.Drain(context.Background(), 0)

	if len(done) != 1 {
		t.Fatalf("want 1 dispatch, got %d", len(done))
	}
	if done[0].Status != StatusFailed {
		t.Errorf("a panic should land %q, got %q", StatusFailed, done[0].Status)
	}
	if got := done[0].Summary; !strings.Contains(got, "panic") {
		t.Errorf("the summary should name the panic, got %q", got)
	}
}

type panickingReasoner struct{}

func (panickingReasoner) Reason(ctx context.Context, r *Runtime, intent Intent, plan []*Workflow, research *Research) (any, error) {
	panic("the seam exploded")
}

func TestKernelRecurringTaskReArms(t *testing.T) {
	k := NewKernel()
	k.Register("sweeper", workingRuntime("sweeper"))
	task := k.Schedule("sweeper", NewIntent("Sweep.", nil), time.Millisecond)

	// Scheduled work is due one period out, not immediately.
	if got := k.Drain(context.Background(), 0); len(got) != 0 {
		t.Fatalf("a scheduled task must not fire immediately, got %d", len(got))
	}

	total := 0
	for range 3 {
		time.Sleep(2 * time.Millisecond)
		total += len(k.Drain(context.Background(), 0))
	}
	if total < 3 {
		t.Errorf("a recurring task should fire each period, got %d firings", total)
	}
	if k.Pending() != 1 {
		t.Errorf("a recurring task stays queued, pending=%d", k.Pending())
	}
	if !task.Recurring() {
		t.Error("Schedule should produce a recurring task")
	}
}

func TestKernelCancelUnqueuesWork(t *testing.T) {
	k := NewKernel()
	k.Register("sweeper", workingRuntime("sweeper"))
	task := k.Schedule("sweeper", NewIntent("Sweep.", nil), time.Millisecond)

	if !k.Cancel(task.ID) {
		t.Fatal("cancelling a queued task should report it was there")
	}
	if k.Cancel(task.ID) {
		t.Error("cancelling twice should report nothing was removed")
	}
	time.Sleep(2 * time.Millisecond)
	if got := k.Drain(context.Background(), 0); len(got) != 0 {
		t.Errorf("a cancelled task must not fire, got %d", len(got))
	}
}

func TestKernelRunsDueOrderFirst(t *testing.T) {
	k := NewKernel()
	k.Register("a", workingRuntime("a"))
	late := k.Submit("a", NewIntent("Later.", nil), StartAfter(50*time.Millisecond))
	early := k.Submit("a", NewIntent("Sooner.", nil))

	done := k.Drain(context.Background(), 0)
	if len(done) != 1 || done[0].TaskID != early.ID {
		t.Fatalf("the due task should run and the deferred one wait, got %+v", done)
	}
	if k.Pending() != 1 {
		t.Errorf("the deferred task should still be queued, pending=%d", k.Pending())
	}
	_ = late
}

func TestKernelDispatcherSeamReplacesInProcessExecution(t *testing.T) {
	k := NewKernel()
	k.Register("remote", workingRuntime("remote"))
	var seen string
	k.Dispatcher = func(ctx context.Context, task *Task, rt *Runtime) (DispatchStatus, string) {
		seen = task.Intent.Text
		return StatusRan, "ran on the cluster"
	}
	k.Submit("remote", NewIntent("Do it elsewhere.", nil))

	done := k.Drain(context.Background(), 0)

	if len(done) != 1 || done[0].Summary != "ran on the cluster" {
		t.Fatalf("the seam should have handled the dispatch, got %+v", done)
	}
	if seen != "Do it elsewhere." {
		t.Errorf("the seam should receive the task's intent, got %q", seen)
	}
	// The instance's own cycle must not also have run.
	rt, _ := k.Instance("remote")
	if n := rt.Experience.Observations; n != 0 {
		t.Errorf("the in-process cycle should have been bypassed, observations=%d", n)
	}
}

func TestKernelArmSchedulesAuthoredWork(t *testing.T) {
	rt := workingRuntime("underwriting")
	rt.Strategy = StrategyFromMarkdown(`# Memory

## Scheduled Work

- Every 15 minutes, reason "Sweep the overnight application queue."
- Every 24 hours, reason "Produce the daily underwriting summary."
`)

	k := NewKernel()
	k.Register("underwriting", rt)
	armed, err := k.Arm("underwriting")
	if err != nil {
		t.Fatalf("Arm: %v", err)
	}

	if len(armed) != 2 {
		t.Fatalf("want 2 armed tasks, got %d", len(armed))
	}
	if armed[0].Every != 15*time.Minute {
		t.Errorf("first task period: want 15m, got %v", armed[0].Every)
	}
	if armed[1].Every != 24*time.Hour {
		t.Errorf("second task period: want 24h, got %v", armed[1].Every)
	}
	if got := armed[0].Intent.Text; got != "Sweep the overnight application queue." {
		t.Errorf("first task intent: got %q", got)
	}
}

func TestKernelArmIsANoOpWithoutAnAuthoredSchedule(t *testing.T) {
	k := NewKernel()
	k.Register("plain", workingRuntime("plain"))
	armed, err := k.Arm("plain")
	if err != nil {
		t.Fatalf("an unscheduled stack is a normal stack: %v", err)
	}
	if len(armed) != 0 {
		t.Errorf("nothing should be armed, got %d", len(armed))
	}
	if _, err := k.Arm("missing"); err == nil {
		t.Error("arming an unregistered instance should error")
	}
}

func TestKernelRunStopsOnContextCancel(t *testing.T) {
	k := NewKernel()
	k.Register("a", workingRuntime("a"))
	ctx, cancel := context.WithCancel(context.Background())

	errc := make(chan error, 1)
	go func() { errc <- k.Run(ctx) }()

	// The loop should be asleep, not spinning: submit and see it picked up.
	k.Submit("a", NewIntent("Do it.", nil))
	deadline := time.After(2 * time.Second)
	for {
		if k.Snapshot().Dispatched > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the running loop never picked up submitted work")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if k.Running() {
		t.Error("a returned Run should leave the kernel not running")
	}
}

func TestKernelRunRefusesToRunTwice(t *testing.T) {
	k := NewKernel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = k.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for !k.Running() {
		select {
		case <-deadline:
			t.Fatal("Run never started")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := k.Run(ctx); err == nil {
		t.Error("a second concurrent Run should be refused")
	}
}

func TestKernelKeepsOneCyclePerInstance(t *testing.T) {
	// The single-writer invariant: an instance's memory and hash-chained trail
	// must never have two concurrent writers, however wide the fleet fans out.
	var mu sync.Mutex
	concurrent := map[string]int{}
	peak := map[string]int{}

	k := &Kernel{Workers: 8}
	k.Dispatcher = func(ctx context.Context, task *Task, rt *Runtime) (DispatchStatus, string) {
		mu.Lock()
		concurrent[task.Instance]++
		if concurrent[task.Instance] > peak[task.Instance] {
			peak[task.Instance] = concurrent[task.Instance]
		}
		mu.Unlock()

		time.Sleep(time.Millisecond)

		mu.Lock()
		concurrent[task.Instance]--
		mu.Unlock()
		return StatusRan, "ok"
	}

	for i := range 4 {
		name := fmt.Sprintf("instance-%d", i)
		k.Register(name, workingRuntime(name))
		for range 5 {
			k.Submit(name, NewIntent("Do it.", nil))
		}
	}

	done := k.Drain(context.Background(), 0)

	if len(done) != 20 {
		t.Fatalf("want 20 dispatches, got %d", len(done))
	}
	mu.Lock()
	defer mu.Unlock()
	for name, p := range peak {
		if p > 1 {
			t.Errorf("instance %s had %d concurrent cycles; the single-writer invariant is broken", name, p)
		}
	}
	if len(peak) != 4 {
		t.Errorf("all 4 instances should have run, got %d", len(peak))
	}
}

func TestKernelFansOutAcrossInstances(t *testing.T) {
	// The other half of the invariant: different instances genuinely overlap,
	// or the parallel path is buying nothing.
	var mu sync.Mutex
	inFlight, peak := 0, 0

	k := &Kernel{Workers: 4}
	k.Dispatcher = func(ctx context.Context, task *Task, rt *Runtime) (DispatchStatus, string) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()

		time.Sleep(10 * time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()
		return StatusRan, "ok"
	}

	for i := range 4 {
		name := fmt.Sprintf("instance-%d", i)
		k.Register(name, workingRuntime(name))
		k.Submit(name, NewIntent("Do it.", nil))
	}

	k.Drain(context.Background(), 0)

	mu.Lock()
	defer mu.Unlock()
	if peak < 2 {
		t.Errorf("four instances on four workers should overlap; peak concurrency was %d", peak)
	}
}

func TestKernelSnapshotReportsTheProcessTable(t *testing.T) {
	k := NewKernel()
	k.Register("a", workingRuntime("a"))
	k.Register("b", workingRuntime("b"))
	k.Submit("a", NewIntent("Do it.", nil))
	k.Drain(context.Background(), 0)
	k.Submit("b", NewIntent("Later.", nil), StartAfter(time.Hour))

	snap := k.Snapshot()

	if len(snap.Instances) != 2 {
		t.Errorf("want 2 instances, got %v", snap.Instances)
	}
	if snap.Pending != 1 {
		t.Errorf("want 1 pending, got %d", snap.Pending)
	}
	if snap.Dispatched != 1 {
		t.Errorf("want 1 dispatched, got %d", snap.Dispatched)
	}
	if len(snap.Recent) != 1 || snap.Recent[0].Instance != "a" {
		t.Errorf("recent should hold the dispatch for a, got %+v", snap.Recent)
	}
}

func TestKernelHistoryIsCapped(t *testing.T) {
	k := &Kernel{HistoryLimit: 4}
	k.Register("a", workingRuntime("a"))
	for range 10 {
		k.Submit("a", NewIntent("Do it.", nil))
	}
	k.Drain(context.Background(), 0)

	if got := len(k.History()); got != 4 {
		t.Errorf("history should be capped at 4, got %d", got)
	}
	if snap := k.Snapshot(); snap.Dispatched != 10 {
		t.Errorf("the dispatch count should survive trimming, got %d", snap.Dispatched)
	}
}

func TestKernelZeroValueIsUsable(t *testing.T) {
	k := &Kernel{}
	k.Register("a", workingRuntime("a"))
	k.Submit("a", NewIntent("Do it.", nil))
	if done := k.Drain(context.Background(), 0); len(done) != 1 {
		t.Fatalf("the zero-value kernel should work, got %d dispatches", len(done))
	}
}

func TestKernelDrainRespectsMaxUnits(t *testing.T) {
	k := NewKernel()
	k.Register("a", workingRuntime("a"))
	for range 10 {
		k.Submit("a", NewIntent("Do it.", nil))
	}
	if done := k.Drain(context.Background(), 3); len(done) != 3 {
		t.Errorf("want 3 dispatches, got %d", len(done))
	}
	if k.Pending() != 7 {
		t.Errorf("want 7 still queued, got %d", k.Pending())
	}
}
