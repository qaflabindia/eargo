package ear

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestUsageReportDeterministic(t *testing.T) {
	rt := buildRuntime()
	for i := 0; i < 2; i++ {
		if _, err := rt.Reason(context.Background(), NewIntent("Underwrite a loan",
			map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28}), nil); err != nil {
			t.Fatal(err)
		}
	}
	report := rt.ReasoningLog.UsageReport()
	if !strings.Contains(report, "# Usage Report") || !strings.Contains(report, "**total**") {
		t.Errorf("missing header/total:\n%s", report)
	}
	// Two cycles -> rows "| 1 |" and "| 2 |"; no pricing -> cost dashes.
	if !strings.Contains(report, "| 1 |") || !strings.Contains(report, "| 2 |") {
		t.Errorf("expected two cycle rows:\n%s", report)
	}
	if !strings.Contains(report, "| — |") {
		t.Errorf("unpriced cycles should show a dash cost:\n%s", report)
	}
}

func TestUsageReportPriced(t *testing.T) {
	lm := &meteredLM{reply: Reply("complies", "yes", "rationale", "ok", "decision", "APPROVED",
		"explanation", "x", "assessment", "y")}
	proc := &Process{Name: "Underwriting", Description: "Underwrite a loan."}
	proc.AddWorkflow((&Workflow{Name: "W"}).AddStep("Decide.", nil))
	rt := NewRuntime("R", WithLM(lm))
	rt.AddProcess(proc)
	rt.Strategy = StrategyFromMarkdown("## Pricing\n\nInput tokens cost $3 per million; output tokens cost $15 per million.\n")

	if _, err := rt.Reason(context.Background(), NewIntent("Underwrite a loan", nil), nil); err != nil {
		t.Fatal(err)
	}
	report := rt.ReasoningLog.UsageReport()
	// A priced cycle shows a dollar cell (not a dash) and a priced total.
	if strings.Count(report, "$") < 2 {
		t.Errorf("expected dollar cells in a priced report:\n%s", report)
	}
	n := len(lm.Calls())
	if !strings.Contains(report, fmt.Sprintf("| %d | %d+%d |", n, n*100, n*20)) {
		t.Errorf("expected the row to reflect %d calls / %d+%d tokens:\n%s", n, n*100, n*20, report)
	}
}
