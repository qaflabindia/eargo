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

// writeTenantStack lays down a minimal loadable stack, plus whatever tenant
// file the case under test needs. A nil tenant map writes no tenant file at
// all -- the "never declared one" case.
func writeTenantStack(t *testing.T, tenantFiles map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("process.md", "# Desk\n\n## Handle\n\nHandle requests.\n\n- W\n\n## W\n\nDecide.\n")
	write("workflow.md", "## W\n\n1. Decide.\n")
	for name, body := range tenantFiles {
		write(name, body)
	}
	return dir
}

func TestNoTenantFileIsTheDefaultOrg(t *testing.T) {
	// The documented off-unless-declared posture: a stack that never declares
	// a tenant belongs to the default org, and that is not an error.
	rt, err := LoadRuntime(writeTenantStack(t, nil), "Desk")
	if err != nil {
		t.Fatalf("a stack with no tenant.md must load: %v", err)
	}
	if rt.Tenant.OrgID != DefaultOrgID {
		t.Errorf("want the default org %q, got %q", DefaultOrgID, rt.Tenant.OrgID)
	}
}

func TestDeclaredTenantIsParsed(t *testing.T) {
	rt, err := LoadRuntime(writeTenantStack(t, map[string]string{
		"tenant.md": "## Acme Corp\n\nOrg id: acme\nTimezone: Asia/Kolkata\n",
	}), "Desk")
	if err != nil {
		t.Fatalf("LoadRuntime: %v", err)
	}
	if rt.Tenant.OrgID != "acme" {
		t.Errorf("want org acme, got %q", rt.Tenant.OrgID)
	}
	if rt.Tenant.Name != "Acme Corp" {
		t.Errorf("want name Acme Corp, got %q", rt.Tenant.Name)
	}
}

func TestTenantFileThatParsesToNothingFailsLoudly(t *testing.T) {
	// A single '#' is a document title, not a section, so this file parses to
	// zero sections. Falling back to the default org here would silently
	// disable a security boundary the author plainly meant to declare.
	_, err := LoadRuntime(writeTenantStack(t, map[string]string{
		"tenant.md": "# Tenant\n\nOrg ID: acme\n",
	}), "Desk")
	if err == nil {
		t.Fatal("a tenant.md that declares nothing must not load silently")
	}
	// The message has to be actionable: which file, and what shape it needs.
	for _, want := range []string{"tenant.md", "##", "Org id:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q omits %q", err.Error(), want)
		}
	}
}

func TestEmptyTenantFileFailsLoudly(t *testing.T) {
	_, err := LoadRuntime(writeTenantStack(t, map[string]string{"tenant.md": "\n\n"}), "Desk")
	if err == nil {
		t.Fatal("an empty tenant.md declares nothing and must not load silently")
	}
}

func TestTenantErrorNamesTheFileItActuallyFound(t *testing.T) {
	// org.md is the alternate candidate; the diagnostic must point at the file
	// the author actually wrote, not the canonical name.
	_, err := LoadRuntime(writeTenantStack(t, map[string]string{
		"org.md": "# Org\n\nOrg id: acme\n",
	}), "Desk")
	if err == nil {
		t.Fatal("an org.md that declares nothing must not load silently")
	}
	if !strings.Contains(err.Error(), "org.md") {
		t.Errorf("error should name org.md, got %q", err.Error())
	}
}

func TestTenantDeclaringNoOrgIDStillFailsLoudly(t *testing.T) {
	// The pre-existing loud path, kept honest alongside the new one.
	_, err := LoadRuntime(writeTenantStack(t, map[string]string{
		"tenant.md": "## Acme Corp\n\nTimezone: Asia/Kolkata\n",
	}), "Desk")
	if err == nil {
		t.Fatal("a tenant section with no 'Org id:' must fail")
	}
	if !strings.Contains(err.Error(), "Org id") {
		t.Errorf("error should name the missing field, got %q", err.Error())
	}
}

func TestDroppedTenantCannotSilentlyAdmitAForeignClaim(t *testing.T) {
	// The consequence the loud failure exists to prevent. Before the fix this
	// stack loaded on the default org, so a claim scoped to "default" -- an
	// org the author never declared -- was admitted to acme's data.
	_, err := LoadRuntime(writeTenantStack(t, map[string]string{
		"tenant.md": "# Tenant\n\nOrg ID: acme\n",
	}), "Desk")
	if err == nil {
		t.Fatal("the stack must refuse to load rather than run on a boundary nobody declared")
	}

	// Loaded correctly, the boundary does its job.
	rt, err := LoadRuntime(writeTenantStack(t, map[string]string{
		"tenant.md": "## Acme Corp\n\nOrg id: acme\n",
	}), "Desk")
	if err != nil {
		t.Fatalf("LoadRuntime: %v", err)
	}
	foreign := WithClaim(context.Background(), Claim{Subject: "svc:intruder", OrgIDs: []string{DefaultOrgID}})
	var boundary *TenantBoundaryError
	if _, err := rt.Reason(foreign, NewIntent("Read the book.", nil), nil); !errors.As(err, &boundary) {
		t.Fatalf("a claim for the default org must not reach acme's data, got %v", err)
	}
}
