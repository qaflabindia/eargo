package ear

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeAndCoerce(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Credit Risk Guru", "credit risk guru"},
		{"credit-risk-guru", "credit risk guru"},
		{"credit_risk_guru", "credit risk guru"},
	} {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if v := Coerce("18500"); v != float64(18500) {
		t.Errorf("Coerce number = %v (%T)", v, v)
	}
	if v := Coerce("0.28"); v != 0.28 {
		t.Errorf("Coerce float = %v", v)
	}
	if v := Coerce("yes"); v != true {
		t.Errorf("Coerce yes = %v", v)
	}
	if v := Coerce("no"); v != false {
		t.Errorf("Coerce no = %v", v)
	}
	if v := Coerce("approve"); v != "approve" {
		t.Errorf("Coerce string = %v", v)
	}
}

func TestParseDocument(t *testing.T) {
	doc := ParseDocument("# Title\n\nSome preamble.\n\n## Context\n\n- loan_amount: 18500\n- flag: yes\n")
	if doc.Title != "Title" {
		t.Fatalf("title = %q", doc.Title)
	}
	if doc.Preamble != "Some preamble." {
		t.Fatalf("preamble = %q", doc.Preamble)
	}
	if len(doc.Sections) != 1 || doc.Sections[0].Name != "Context" {
		t.Fatalf("sections = %+v", doc.Sections)
	}
	body := doc.Sections[0].StructuredBody()
	if len(body.Bullets) != 2 {
		t.Fatalf("bullets = %v", body.Bullets)
	}
}

func TestSafeEval(t *testing.T) {
	cases := []struct {
		expr string
		vars map[string]any
		want bool
	}{
		{"loan_amount <= 75000", map[string]any{"loan_amount": 20000.0}, true},
		{"loan_amount <= 75000", map[string]any{"loan_amount": 90000.0}, false},
		{"debt_to_income <= 0.43 and credit_score >= 700", map[string]any{"debt_to_income": 0.28, "credit_score": 742.0}, true},
		{"not (a or b)", map[string]any{"a": false, "b": false}, true},
		{"tier in [\"A\", \"B\"]", map[string]any{"tier": "B"}, true},
		{"amount * 2 > 100", map[string]any{"amount": 60.0}, true},
	}
	for _, tc := range cases {
		got, err := SafeEval(tc.expr, tc.vars)
		if err != nil {
			t.Errorf("SafeEval(%q) error: %v", tc.expr, err)
			continue
		}
		if truthy(got) != tc.want {
			t.Errorf("SafeEval(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}
}

func TestSafeEvalMissingVariable(t *testing.T) {
	_, err := SafeEval("unknown_var > 3", map[string]any{})
	var missing *MissingVariableError
	if !errors.As(err, &missing) {
		t.Fatalf("expected MissingVariableError, got %v", err)
	}
}

func TestSafeEvalRejectsCalls(t *testing.T) {
	// A parenthesized name is not a call in this grammar -- it must fail to
	// parse rather than execute anything.
	if _, err := SafeEval("open(\"/etc/passwd\")", map[string]any{}); err == nil {
		t.Fatal("expected error on call-like expression")
	}
}

func TestPolicyJudge(t *testing.T) {
	p := &Policy{Name: "Cap", FallbackExpression: "loan_amount <= 75000"}
	if complies, _ := p.Judge(map[string]any{"loan_amount": 20000.0}); !complies {
		t.Error("expected compliance under cap")
	}
	if complies, _ := p.Judge(map[string]any{"loan_amount": 90000.0}); complies {
		t.Error("expected violation over cap")
	}
	// Missing variable -> not applicable -> complies.
	if complies, _ := p.Judge(map[string]any{}); !complies {
		t.Error("missing variable should be treated as not applicable")
	}
}

func TestApproverAllowed(t *testing.T) {
	open := &Policy{Name: "Open"}
	if !open.ApproverAllowed("anyone") {
		t.Error("no allow-list should permit anyone")
	}
	gated := &Policy{Name: "Gated", Approvers: []string{"Risk Officer"}}
	if !gated.ApproverAllowed("risk-officer") {
		t.Error("allow-list should match normalized")
	}
	if gated.ApproverAllowed("intern") {
		t.Error("off-list approver should be refused")
	}
}

func TestMemoryCompression(t *testing.T) {
	m := &Memory{Capacity: 3}
	for i := 0; i < 5; i++ {
		m.Record("intent", "decision", nil, nil)
	}
	if len(m.Working) != 3 {
		t.Errorf("working = %d, want 3", len(m.Working))
	}
	if len(m.Compressed) == 0 {
		t.Error("expected compressed summary after overflow")
	}
}

func TestExperienceAndAdaptation(t *testing.T) {
	x := NewExperience()
	for i := 0; i < 3; i++ {
		x.ObserveEntry(MemoryEntry{Decision: "approve"})
	}
	x.ObserveEntry(MemoryEntry{Decision: "decline"})
	decision, count := x.MostCommonDecision()
	if decision != "approve" || count != 3 {
		t.Errorf("most common = %q %d", decision, count)
	}
	bank := NewAdaptationBank()
	a := bank.LearnFrom(x)
	if a == nil || !strings.Contains(a.Insight, "approve") {
		t.Errorf("adaptation = %+v", a)
	}
}

func buildRuntime() *Runtime {
	guru := &Persona{Name: "Credit Risk Guru", Instructions: "Underwrite conservatively."}
	guru.AddSkill(&Skill{Name: "risk_grade", Prompt: "Combine tier and DTI into a grade."})
	w := &Workflow{Name: "Underwriting Workflow"}
	w.AddStep("Band the credit profile.", guru)
	w.AddPolicy(&Policy{Name: "Loan Amount Cap", FallbackExpression: "loan_amount <= 75000"})
	proc := &Process{Name: "Underwriting", Description: "Underwrite a loan."}
	proc.AddWorkflow(w)
	rt := NewRuntime("Credit Risk Runtime")
	rt.AddProcess(proc)
	rt.AddPolicy(&Policy{Name: "DTI Ceiling", FallbackExpression: "debt_to_income <= 0.43"})
	return rt
}

func TestReasonCompliant(t *testing.T) {
	rt := buildRuntime()
	decision, err := rt.Reason(NewIntent("Underwrite a loan",
		map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28}), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s, _ := decision.(string)
	if !strings.Contains(s, "Credit Risk Runtime") || !strings.Contains(s, "Underwriting") {
		t.Errorf("decision = %q", s)
	}
	if rt.Memory.Len() != 1 {
		t.Errorf("expected one remembered cycle, got %d", rt.Memory.Len())
	}
}

func TestReasonRuntimePolicyBlock(t *testing.T) {
	rt := buildRuntime()
	_, err := rt.Reason(NewIntent("Underwrite a loan",
		map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.60}), nil)
	var pv *PolicyViolationError
	if !errors.As(err, &pv) {
		t.Fatalf("expected PolicyViolationError, got %v", err)
	}
	if pv.Scope != "Policy" || pv.Policies[0].Name != "DTI Ceiling" {
		t.Errorf("violation = %+v", pv)
	}
}

func TestReasonWorkflowPolicyBlock(t *testing.T) {
	rt := buildRuntime()
	_, err := rt.Reason(NewIntent("Underwrite a loan",
		map[string]any{"loan_amount": 90000.0, "debt_to_income": 0.28}), nil)
	var pv *PolicyViolationError
	if !errors.As(err, &pv) {
		t.Fatalf("expected PolicyViolationError, got %v", err)
	}
	if pv.Scope != "Workflow policy" {
		t.Errorf("scope = %q", pv.Scope)
	}
}

func TestApprovalGate(t *testing.T) {
	rt := NewRuntime("Approver Runtime")
	w := &Workflow{Name: "Large Loan Workflow"}
	w.AddStep("Underwrite the large loan.", nil)
	w.AddPolicy(&Policy{
		Name: "Large Loan Human Approval", FallbackExpression: "loan_amount <= 50000",
		ApprovalRequired: true, Approvers: []string{"Risk Officer"},
	})
	proc := &Process{Name: "Large Loan", Description: "Handle large loans."}
	proc.AddWorkflow(w)
	rt.AddProcess(proc)

	intent := NewIntent("Underwrite a large loan", map[string]any{"loan_amount": 60000.0})

	// No verdict -> parked for approval.
	_, err := rt.Reason(intent, nil)
	var ar *ApprovalRequiredError
	if !errors.As(err, &ar) {
		t.Fatalf("expected ApprovalRequiredError, got %v", err)
	}

	// Approved by an allowed approver -> proceeds.
	yes := true
	decision, err := rt.Reason(intent, &ApprovalVerdict{Approver: "Risk Officer", Verdict: &yes})
	if err != nil {
		t.Fatalf("approved cycle errored: %v", err)
	}
	if _, ok := decision.(string); !ok {
		t.Fatalf("expected a decision, got %v", decision)
	}

	// Approved by an off-list approver -> still blocked.
	_, err = rt.Reason(intent, &ApprovalVerdict{Approver: "Intern", Verdict: &yes})
	if err == nil {
		t.Fatal("off-list approver should not waive the gate")
	}

	// Rejected -> hard block.
	no := false
	_, err = rt.Reason(intent, &ApprovalVerdict{Approver: "Risk Officer", Verdict: &no})
	var pv *PolicyViolationError
	if !errors.As(err, &pv) {
		t.Fatalf("rejected gate should hard-block, got %v", err)
	}
}

func TestIntentFromMarkdown(t *testing.T) {
	md := "# Underwrite a loan\n\nConsider it carefully.\n\n## Context\n\n- loan_amount: 18500\n- flagged: yes\n"
	intent := IntentFromMarkdown(md)
	if !strings.Contains(intent.Text, "Underwrite a loan") {
		t.Errorf("text = %q", intent.Text)
	}
	if intent.Context["loan_amount"] != float64(18500) {
		t.Errorf("loan_amount = %v", intent.Context["loan_amount"])
	}
	if intent.Context["flagged"] != true {
		t.Errorf("flagged = %v", intent.Context["flagged"])
	}
}

func TestMarkdownRoundTrip(t *testing.T) {
	skill := &Skill{Name: "risk_grade", Description: "grade risk", Prompt: "Do the grading."}
	if !strings.Contains(skill.ToMarkdown(), "## risk_grade") {
		t.Error("skill markdown missing heading")
	}
	p := &Policy{Name: "Cap", FallbackExpression: "loan_amount <= 75000", Statement: "Cap it."}
	md := p.ToMarkdown()
	if !strings.Contains(md, "Fallback: loan_amount <= 75000") || !strings.Contains(md, "Cap it.") {
		t.Errorf("policy markdown = %q", md)
	}
}
