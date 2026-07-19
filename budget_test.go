package ear

import (
	"context"
	"testing"
)

func TestPricingParseAndDollars(t *testing.T) {
	s := StrategyFromMarkdown("# Memory\n\n## Pricing\n\n" +
		"Input tokens cost $3 per million; output tokens cost $15 per million.\n")
	if s.InputRatePerMillion == nil || *s.InputRatePerMillion != 3 {
		t.Fatalf("input rate = %v", s.InputRatePerMillion)
	}
	if s.OutputRatePerMillion == nil || *s.OutputRatePerMillion != 15 {
		t.Fatalf("output rate = %v", s.OutputRatePerMillion)
	}
	cost, ok := s.Dollars(1_000_000, 1_000_000, 0, 0)
	if !ok || cost != 18 {
		t.Errorf("Dollars = %v, %v; want 18, true", cost, ok)
	}
	// Cached input is discounted; cache write is a premium.
	cost, _ = s.Dollars(0, 0, 1_000_000, 0)
	if diff := cost - 0.3; diff < -1e-9 || diff > 1e-9 {
		t.Errorf("cache-read cost = %v, want ~0.3", cost)
	}
}

func TestDollarsUnpricedReturnsFalse(t *testing.T) {
	s := &Strategy{}
	if _, ok := s.Dollars(1000, 1000, 0, 0); ok {
		t.Error("undeclared pricing must return ok=false, never invent a figure")
	}
}

func TestBudgetMonitorProgressive(t *testing.T) {
	var fired []float64
	// Thresholds intentionally unsorted; the monitor sorts them.
	m := NewBudgetMonitor(1.0, func(a BudgetAlert) { fired = append(fired, a.Threshold) }, 0.5, 0.25, 1.0)

	m.Add(0.30) // 30% -> crosses 25%
	m.Add(0.30) // 60% -> crosses 50%
	m.Add(0.30) // 90% -> nothing new
	m.Add(0.30) // 120% -> crosses 100%

	want := []float64{0.25, 0.5, 1.0}
	if len(fired) != len(want) {
		t.Fatalf("fired %v, want %v", fired, want)
	}
	for i := range want {
		if fired[i] != want[i] {
			t.Errorf("fired[%d] = %v, want %v (must be progressive/in order)", i, fired[i], want[i])
		}
	}
}

func TestBudgetMonitorFiresEachThresholdOnce(t *testing.T) {
	count := 0
	m := NewBudgetMonitor(1.0, func(BudgetAlert) { count++ }, 0.25)
	m.Add(0.5) // crosses 25%
	m.Add(0.5) // still above 25%, must not re-fire
	m.Add(0.5)
	if count != 1 {
		t.Errorf("threshold fired %d times, want exactly 1", count)
	}
}

func TestBudgetMonitorNonBlockingAndDisabled(t *testing.T) {
	// No budget cap -> no alerts, and Add is still safe.
	m := NewBudgetMonitor(0, nil, 0.25)
	m.Add(1000)
	if m.Spent() != 1000 {
		t.Errorf("spent = %v, want 1000", m.Spent())
	}
}

func TestBudgetAlertsDuringCyclesNonBlocking(t *testing.T) {
	lm := &meteredLM{reply: Reply("complies", "yes", "rationale", "ok", "decision", "APPROVED",
		"explanation", "x", "assessment", "y")}
	proc := &Process{Name: "Underwriting", Description: "Underwrite a loan."}
	proc.AddWorkflow((&Workflow{Name: "W"}).AddStep("Decide.", nil))

	var alerts []BudgetAlert
	rt := NewRuntime("R",
		WithLM(lm),
		WithBudget(0.01, func(a BudgetAlert) { alerts = append(alerts, a) }, 0.25, 0.5, 1.0),
	)
	rt.AddProcess(proc)
	rt.Strategy = StrategyFromMarkdown("## Pricing\n\nInput tokens cost $3 per million; " +
		"output tokens cost $15 per million.\n")

	// Run cycles until the budget is exhausted; every cycle must still
	// complete -- alerts never stop the runtime.
	for i := 0; i < 12 && rt.Budget.Spent() < 0.01; i++ {
		if _, err := rt.Reason(context.Background(), NewIntent("Underwrite a loan", nil), nil); err != nil {
			t.Fatalf("cycle %d errored (alerts must not block): %v", i, err)
		}
	}

	if len(alerts) != 3 {
		t.Fatalf("expected 3 progressive alerts (25/50/100%%), got %d: %+v", len(alerts), alerts)
	}
	for i, want := range []float64{0.25, 0.5, 1.0} {
		if alerts[i].Threshold != want {
			t.Errorf("alert[%d] threshold = %v, want %v", i, alerts[i].Threshold, want)
		}
	}
	// The crossings are also on the audit trail.
	var budgetRecords int
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "budget" {
			budgetRecords++
		}
	}
	if budgetRecords != 3 {
		t.Errorf("budget records on trail = %d, want 3", budgetRecords)
	}
}

func TestBudgetAuthoredInMarkdown(t *testing.T) {
	s := StrategyFromMarkdown("# Memory\n\n## Budget\n\n" +
		"The monthly budget is $500. Send progressive alerts at 25%, 50%, 75%, 90% and 100%.\n")
	if s.Budget != 500 {
		t.Errorf("Budget = %v, want 500", s.Budget)
	}
	want := []float64{0.25, 0.5, 0.75, 0.9, 1.0}
	if len(s.AlertThresholds) != len(want) {
		t.Fatalf("thresholds = %v, want %v", s.AlertThresholds, want)
	}
	for i := range want {
		if s.AlertThresholds[i] != want[i] {
			t.Errorf("threshold[%d] = %v, want %v", i, s.AlertThresholds[i], want[i])
		}
	}
}

func TestBudgetWiredFromMemoryMd(t *testing.T) {
	rt := NewRuntime("R")
	applyMemoryStrategy(rt, "## Budget\n\nThe budget is $1,000. Warn at 25% and 50%.\n")
	if rt.Budget == nil {
		t.Fatal("a declared budget should wire the monitor")
	}
	if rt.Budget.Budget != 1000 {
		t.Errorf("cap = %v, want 1000", rt.Budget.Budget)
	}
	if len(rt.Budget.Thresholds) != 2 {
		t.Errorf("thresholds = %v, want 2", rt.Budget.Thresholds)
	}
}
