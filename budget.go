package ear

import (
	"fmt"
	"sort"
	"sync"
)

// BudgetAlert is fired once when cumulative dollar spend first crosses a
// threshold. It is a notification, not a stop -- the runtime keeps going.
type BudgetAlert struct {
	Threshold float64 // the fraction crossed, e.g. 0.25 for 25%
	Spent     float64 // cumulative dollars spent so far
	Budget    float64 // the dollar cap the fraction is measured against
	Fraction  float64 // Spent / Budget at the moment of the alert
}

// String renders the alert for a log line or a notification.
func (a BudgetAlert) String() string {
	return fmt.Sprintf("budget alert: %.0f%% reached (~$%.6f of $%.2f, %.1f%% spent)",
		a.Threshold*100, a.Spent, a.Budget, a.Fraction*100)
}

// BudgetMonitor tracks cumulative dollar spend against a cap and fires
// progressive alerts as spend crosses each declared threshold. It is
// deliberately non-blocking: Add never returns an error and never stops a
// cycle -- it only notifies (a callback and, when wired, the audit trail).
// Each threshold fires exactly once, in ascending order, no matter how the
// spend arrives across cycles.
type BudgetMonitor struct {
	Budget     float64           // dollar cap; <= 0 disables alerting
	Thresholds []float64         // fractions in (0, ...], e.g. 0.25, 0.5, 0.9, 1.0
	OnAlert    func(BudgetAlert) // caller notification (optional)
	Log        *ReasoningLog     // audit trail sink (optional)

	mu    sync.Mutex
	spent float64
	fired map[float64]bool
}

// NewBudgetMonitor builds a monitor for a dollar cap and a set of fraction
// thresholds (25% is 0.25). Thresholds are de-duplicated and sorted
// ascending; onAlert may be nil.
func NewBudgetMonitor(budget float64, onAlert func(BudgetAlert), thresholds ...float64) *BudgetMonitor {
	seen := map[float64]bool{}
	var sorted []float64
	for _, t := range thresholds {
		if t > 0 && !seen[t] {
			seen[t] = true
			sorted = append(sorted, t)
		}
	}
	sort.Float64s(sorted)
	return &BudgetMonitor{Budget: budget, Thresholds: sorted, OnAlert: onAlert, fired: map[float64]bool{}}
}

// Add records additional spend and fires any thresholds it newly crosses.
// Non-blocking: alerts are delivered, but the caller is never stopped.
func (b *BudgetMonitor) Add(cost float64) {
	b.mu.Lock()
	b.spent += cost
	spent := b.spent
	var alerts []BudgetAlert
	if b.Budget > 0 {
		fraction := spent / b.Budget
		for _, t := range b.Thresholds {
			if !b.fired[t] && fraction >= t {
				b.fired[t] = true
				alerts = append(alerts, BudgetAlert{Threshold: t, Spent: spent, Budget: b.Budget, Fraction: fraction})
			}
		}
	}
	b.mu.Unlock()

	// Deliver outside the lock so a slow callback can't stall other spend.
	for _, a := range alerts {
		if b.Log != nil {
			b.Log.Record(Record{Stage: "budget", Output: a.String()})
		}
		if b.OnAlert != nil {
			b.OnAlert(a)
		}
	}
}

// Spent returns the cumulative dollars recorded so far.
func (b *BudgetMonitor) Spent() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}
