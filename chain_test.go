package ear

import (
	"context"
	"testing"
)

func TestTrailChainVerifies(t *testing.T) {
	rt := buildRuntime()
	for i := 0; i < 3; i++ {
		if _, err := rt.Reason(context.Background(), NewIntent("Underwrite a loan",
			map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28}), nil); err != nil {
			t.Fatal(err)
		}
	}
	ok, detail := rt.ReasoningLog.Verify()
	if !ok {
		t.Fatalf("intact trail should verify: %s", detail)
	}
	// Every record carries a chain link.
	for rec := range rt.ReasoningLog.Records() {
		if rec.Chain == "" {
			t.Fatalf("record %q has no chain link", rec.Stage)
		}
	}
}

func TestTrailChainDetectsTampering(t *testing.T) {
	rt := buildRuntime()
	_, _ = rt.Reason(context.Background(), NewIntent("Underwrite a loan",
		map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28}), nil)

	// Tamper with a stored record's output; the chain must catch it.
	log := rt.ReasoningLog
	if len(log.Cycles) == 0 || len(log.Cycles[0].Records) == 0 {
		t.Fatal("no records to tamper with")
	}
	log.Cycles[0].Records[0].Output = "FORGED"

	ok, detail := log.Verify()
	if ok {
		t.Fatal("a tampered trail must not verify")
	}
	if detail == "" {
		t.Error("verify should name where the chain broke")
	}
}
