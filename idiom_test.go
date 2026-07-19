package ear

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// slowJudge is a PolicyJudge that blocks for a fixed duration, standing in
// for the latency of a real provider call so the concurrency and
// cancellation behaviour can be exercised deterministically.
type slowJudge struct{ delay time.Duration }

func (s slowJudge) Judge(ctx context.Context, policy *Policy, _ map[string]any) (bool, string, error) {
	select {
	case <-time.After(s.delay):
		return true, "slow judge: complied", nil
	case <-ctx.Done():
		return false, "", ctx.Err()
	}
}

func TestContextCancellationAbortsCycle(t *testing.T) {
	rt := buildRuntime()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the cycle even starts
	_, err := rt.Reason(ctx, NewIntent("Underwrite a loan",
		map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28}), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestConcurrentGovernanceIsBoundByMaxNotSum(t *testing.T) {
	// Ten policies, each a 40ms "provider call". Serial would take ~400ms;
	// the concurrent fan-out should finish far sooner.
	proc := &Process{Name: "Underwriting", Description: "Underwrite a loan."}
	proc.AddWorkflow(&Workflow{Name: "W"})
	rt := NewRuntime("R", WithPolicyJudge(slowJudge{delay: 40 * time.Millisecond}))
	rt.AddProcess(proc)
	for i := 0; i < 10; i++ {
		rt.AddPolicy(&Policy{Name: "P", FallbackExpression: "x <= 1"})
	}
	start := time.Now()
	if _, err := rt.Reason(context.Background(), NewIntent("go", nil), nil); err != nil {
		t.Fatalf("cycle errored: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 250*time.Millisecond {
		t.Errorf("governance took %v; expected concurrent fan-out well under the ~400ms serial sum", elapsed)
	}
}

func TestParallelMapPreservesOrder(t *testing.T) {
	in := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	out := parallelMap(context.Background(), in, func(_ context.Context, n int) int { return n * n })
	for i, v := range out {
		if v != i*i {
			t.Fatalf("out[%d] = %d, want %d (order not preserved)", i, v, i*i)
		}
	}
}

// erroringJudge fails, and the cycle must fail closed rather than passing
// governance silently.
type erroringJudge struct{}

func (erroringJudge) Judge(context.Context, *Policy, map[string]any) (bool, string, error) {
	return false, "", errors.New("provider unavailable")
}

func TestJudgeErrorFailsClosed(t *testing.T) {
	rt := NewRuntime("R", WithPolicyJudge(erroringJudge{}))
	rt.AddPolicy(&Policy{Name: "P", FallbackExpression: "x <= 1"})
	rt.AddProcess((&Process{Name: "P"}).AddWorkflow(&Workflow{Name: "W"}))
	_, err := rt.Reason(context.Background(), NewIntent("go", nil), nil)
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("expected the judge error to fail the cycle, got %v", err)
	}
}

// staticReasoner shows the Reasoner seam: swap deliberation wholesale.
type staticReasoner struct{ decision string }

func (s staticReasoner) Reason(context.Context, *Runtime, Intent, []*Workflow) (any, error) {
	return s.decision, nil
}

func TestReasonerSeam(t *testing.T) {
	rt := NewRuntime("R", WithReasoner(staticReasoner{decision: "APPROVE"}))
	rt.AddProcess((&Process{Name: "P"}).AddWorkflow(&Workflow{Name: "W"}))
	decision, err := rt.Reason(context.Background(), NewIntent("go", nil), nil)
	if err != nil {
		t.Fatalf("errored: %v", err)
	}
	if decision != "APPROVE" {
		t.Errorf("decision = %v, want APPROVE", decision)
	}
}

func TestReasoningLogSinkAndIterator(t *testing.T) {
	var buf bytes.Buffer
	rt := buildRuntime()
	rt.ReasoningLog.Sink = &buf
	if _, err := rt.Reason(context.Background(), NewIntent("Underwrite a loan",
		map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28}), nil); err != nil {
		t.Fatalf("errored: %v", err)
	}
	// JSONL sink: one JSON object per line, at least the deliberation record.
	if !strings.Contains(buf.String(), `"stage":"deliberation"`) {
		t.Errorf("sink missing deliberation record:\n%s", buf.String())
	}
	lines := strings.Count(strings.TrimSpace(buf.String()), "\n") + 1
	// Iterator counts the same records the sink streamed.
	iterated := 0
	for range rt.ReasoningLog.Records() {
		iterated++
	}
	if iterated != lines {
		t.Errorf("iterator saw %d records, sink streamed %d lines", iterated, lines)
	}
}

func TestWithOptions(t *testing.T) {
	rt := NewRuntime("R", WithMemoryCapacity(3), WithAdaptEvery(0))
	if rt.Memory.Capacity != 3 {
		t.Errorf("capacity = %d, want 3", rt.Memory.Capacity)
	}
	if rt.AdaptEvery != 0 {
		t.Errorf("AdaptEvery = %d, want 0", rt.AdaptEvery)
	}
}

func BenchmarkReasonDeterministic(b *testing.B) {
	rt := buildRuntime()
	intent := NewIntent("Underwrite a loan", map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28})
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Reset the per-cycle accumulators so the benchmark measures one
		// cold cycle rather than an ever-growing trail and memory.
		rt.ReasoningLog = &ReasoningLog{}
		rt.Memory = NewMemory()
		rt.Experience = NewExperience()
		if _, err := rt.Reason(ctx, intent, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSafeEval(b *testing.B) {
	vars := map[string]any{"debt_to_income": 0.28, "credit_score": 742.0}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := SafeEval("debt_to_income <= 0.43 and credit_score >= 700", vars); err != nil {
			b.Fatal(err)
		}
	}
}
