package ear

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyExampleStack copies the repository's example stack into a temp dir --
// the stack declares persistence (trail, session), so reasoning through it
// in place would mutate the repo fixture on every test run. Runtime state
// (.ear and the like) is not copied; each test starts the stack cold.
func copyExampleStack(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "examples", "credit_risk_stack")
	if _, err := os.Stat(filepath.Join(src, "policy.md")); err != nil {
		t.Skipf("example stack not present: %v", err)
	}
	dst := t.TempDir()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip runtime state dirs; only the authored stack is the fixture.
		if strings.HasPrefix(filepath.Base(path), ".") || strings.HasPrefix(rel, "ear") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copying the example stack: %v", err)
	}
	return dst
}

// TestLoadExampleStack loads the real credit_risk_stack markdown from the
// repository (copied to a temp dir) and reasons two intents through it,
// exercising the whole deterministic spine end to end: parser, loader, scope
// wiring, the pipeline, and the fallback-expression governor.
func TestLoadExampleStack(t *testing.T) {
	// The example's memory.md declares a model; force the deterministic path
	// so the test is hermetic regardless of the ambient environment.
	t.Setenv("ANTHROPIC_API_KEY", "")
	rt, err := LoadRuntime(copyExampleStack(t), "")
	if err != nil {
		t.Fatalf("LoadRuntime error: %v", err)
	}
	defer rt.Close()

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
	decision, err := rt.Reason(context.Background(), NewIntent("Underwrite a $20,000 consumer loan application",
		map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28, "credit_score": 742.0}), nil)
	if err != nil {
		t.Fatalf("compliant cycle errored: %v", err)
	}
	if _, ok := decision.(string); !ok {
		t.Fatalf("expected string decision, got %T", decision)
	}

	// Over the DTI ceiling -> runtime policy hard-block.
	_, err = rt.Reason(context.Background(), NewIntent("Underwrite a loan",
		map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.60, "credit_score": 742.0}), nil)
	var pv *PolicyViolationError
	if !errors.As(err, &pv) {
		t.Fatalf("expected DTI block, got %v", err)
	}

	// Over the loan cap -> workflow policy block once the plan is composed.
	_, err = rt.Reason(context.Background(), NewIntent("Underwrite a $90,000 consumer loan application",
		map[string]any{"loan_amount": 90000.0, "debt_to_income": 0.28, "credit_score": 742.0}), nil)
	if !errors.As(err, &pv) {
		t.Fatalf("expected loan-cap block, got %v", err)
	}
}

// TestLoadResolvesSkillsAndDelegation confirms persona skills resolve by
// name and workflow steps delegate to the persona named in parentheses.
func TestLoadResolvesSkillsAndDelegation(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	rt, err := LoadRuntime(copyExampleStack(t), "")
	if err != nil {
		t.Fatalf("LoadRuntime error: %v", err)
	}
	defer rt.Close()
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
