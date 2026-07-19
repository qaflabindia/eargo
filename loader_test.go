package ear

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadExampleStack loads the real credit_risk_stack markdown from the
// repository and reasons two intents through it, exercising the whole
// deterministic spine end to end: parser, loader, scope wiring, the
// pipeline, and the fallback-expression governor.
func TestLoadExampleStack(t *testing.T) {
	dir := filepath.Join("..", "examples", "credit_risk_stack")
	if _, err := os.Stat(filepath.Join(dir, "policy.md")); err != nil {
		t.Skipf("example stack not present: %v", err)
	}
	rt, err := LoadRuntime(dir, "")
	if err != nil {
		t.Fatalf("LoadRuntime error: %v", err)
	}

	// Personas' skills stacked, processes discovered, policies wired.
	if len(rt.Processes) == 0 {
		t.Fatal("no processes loaded")
	}
	if len(rt.Policies) == 0 {
		t.Fatal("no runtime-scoped policies wired")
	}

	// The Debt-to-Income Ceiling is runtime-scoped; the Loan Amount Cap and
	// Large Loan Human Approval are workflow-scoped onto the Underwriting
	// Workflow. Confirm the runtime carries the DTI ceiling.
	if !hasPolicyNamed(rt.Policies, "Debt-to-Income Ceiling") {
		t.Errorf("runtime policies = %v", policyNamesOf(rt.Policies))
	}

	// A compliant application reasons to a decision.
	decision, err := rt.Reason(NewIntent("Underwrite a $20,000 consumer loan application",
		map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28, "credit_score": 742.0}), nil)
	if err != nil {
		t.Fatalf("compliant cycle errored: %v", err)
	}
	if _, ok := decision.(string); !ok {
		t.Fatalf("expected string decision, got %T", decision)
	}

	// Over the DTI ceiling -> runtime policy hard-block.
	_, err = rt.Reason(NewIntent("Underwrite a loan",
		map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.60, "credit_score": 742.0}), nil)
	var pv *PolicyViolationError
	if !errors.As(err, &pv) {
		t.Fatalf("expected DTI block, got %v", err)
	}

	// Over the loan cap -> workflow policy block once the plan is composed.
	_, err = rt.Reason(NewIntent("Underwrite a $90,000 consumer loan application",
		map[string]any{"loan_amount": 90000.0, "debt_to_income": 0.28, "credit_score": 742.0}), nil)
	if !errors.As(err, &pv) {
		t.Fatalf("expected loan-cap block, got %v", err)
	}
}

// TestLoadResolvesSkillsAndDelegation confirms persona skills resolve by
// name and workflow steps delegate to the persona named in parentheses.
func TestLoadResolvesSkillsAndDelegation(t *testing.T) {
	dir := filepath.Join("..", "examples", "credit_risk_stack")
	if _, err := os.Stat(filepath.Join(dir, "workflow.md")); err != nil {
		t.Skipf("example stack not present: %v", err)
	}
	rt, err := LoadRuntime(dir, "")
	if err != nil {
		t.Fatalf("LoadRuntime error: %v", err)
	}
	var found bool
	for _, p := range rt.Processes {
		for _, w := range p.Workflows {
			for _, step := range w.Steps {
				if step.Persona != nil {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("expected at least one delegated step across the loaded stack")
	}
}

func hasPolicyNamed(policies []*Policy, name string) bool {
	for _, p := range policies {
		if p.Name == name {
			return true
		}
	}
	return false
}

func policyNamesOf(policies []*Policy) string {
	names := make([]string, len(policies))
	for i, p := range policies {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}
