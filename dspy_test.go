package ear

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestJudgmentRenderPrompt(t *testing.T) {
	prompt := JudgePolicyCompliance.Render(PolicyComplianceIn{
		Statement: "The loan must not exceed $75,000.",
		Context:   map[string]any{"loan_amount": 90000.0},
	})
	for _, want := range []string{
		"## policy statement", "The loan must not exceed", "## context",
		"- loan_amount: 90000", "## complies", "yes or no", "## rationale",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, prompt)
		}
	}
}

func TestJudgmentParsePerKind(t *testing.T) {
	j := Judgment{Outputs: []Field{
		{Name: "complies", Kind: KindBool},
		{Name: "rationale", Kind: KindText},
		{Name: "names", Kind: KindList},
		{Name: "grade", Kind: KindStr},
	}}
	reply := "## complies\n\nno\n\n## rationale\n\nIt exceeds the cap by $15,000.\n\n" +
		"## names\n\n- Alpha\n- Beta\n\n## grade\n\nB\n"
	pred := j.ParseReply(reply)
	if pred.Bool("complies") {
		t.Error("complies should be false")
	}
	if !strings.Contains(pred.Text("rationale"), "exceeds") {
		t.Errorf("rationale = %q", pred.Text("rationale"))
	}
	if got := pred.List("names"); len(got) != 2 || got[0] != "Alpha" {
		t.Errorf("names = %v", got)
	}
	if pred.Str("grade") != "B" {
		t.Errorf("grade = %q", pred.Str("grade"))
	}
}

func TestJudgmentMissingFieldDegrades(t *testing.T) {
	j := Judgment{Outputs: []Field{
		{Name: "complies", Kind: KindBool},
		{Name: "names", Kind: KindList},
	}}
	pred := j.ParseReply("## something else\n\nnoise\n")
	if pred.Bool("complies") {
		t.Error("missing bool should degrade to false")
	}
	if pred.List("names") == nil || len(pred.List("names")) != 0 {
		t.Errorf("missing list should degrade to empty, got %v", pred.List("names"))
	}
}

func TestJudgmentMapKind(t *testing.T) {
	j := Judgment{Outputs: []Field{{Name: "args", Kind: KindMap}}}
	pred := j.ParseReply("## args\n\n- applicant_id: A-1\n- amount: 5000\n")
	m := pred.Map("args")
	if m["applicant_id"] != "A-1" || m["amount"] != "5000" {
		t.Errorf("map = %v", m)
	}
}

func TestJudgmentCacheBoundary(t *testing.T) {
	j := Judgment{
		Instruction:   "do it",
		Inputs:        []Field{NewField("stable", ""), NewField("volatile", "")},
		Outputs:       []Field{{Name: "out", Kind: KindText}},
		CacheBoundary: "volatile",
	}
	lm := &ScriptedLM{Default: section("out", "done")}
	if _, err := j.Run(context.Background(), lm, map[string]any{"stable": "S", "volatile": "V"}); err != nil {
		t.Fatal(err)
	}
	call := lm.History[0]
	if call.CachePrefix == "" || !strings.HasPrefix(call.Prompt, call.CachePrefix) {
		t.Errorf("cache prefix not a leading span of the prompt: %q", call.CachePrefix)
	}
	if strings.Contains(call.CachePrefix, "V") {
		t.Error("cache prefix should end before the volatile value")
	}
}

func TestSignatureOverScriptedLM(t *testing.T) {
	lm := &ScriptedLM{Replies: []string{Reply("complies", "no", "rationale", "It exceeds the cap.")}}
	out, err := JudgePolicyCompliance.Run(context.Background(), lm, PolicyComplianceIn{
		Statement: "cap it", Context: map[string]any{"loan_amount": 90000.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	// out is a typed PolicyComplianceOut -- out.Complies is a bool, not a cast.
	if out.Complies {
		t.Error("expected complies=false")
	}
	if !strings.Contains(out.Rationale, "exceeds") {
		t.Errorf("rationale = %q", out.Rationale)
	}
}

func TestLMJudgeUsesStatementElseFallback(t *testing.T) {
	lm := &ScriptedLM{Default: Reply("complies", "no", "rationale", "violates")}
	judge := NewLMJudge(lm)

	// Statement present -> judged by the model.
	stmt := &Policy{Name: "Cap", Statement: "must not exceed 75000"}
	complies, rationale, err := judge.Judge(context.Background(), stmt, map[string]any{"loan_amount": 90000.0})
	if err != nil || complies || !strings.Contains(rationale, "violates") {
		t.Fatalf("statement path: complies=%v rationale=%q err=%v", complies, rationale, err)
	}

	// No statement, only a fallback expression -> deterministic evaluator.
	expr := &Policy{Name: "Cap", FallbackExpression: "loan_amount <= 75000"}
	complies, _, err = judge.Judge(context.Background(), expr, map[string]any{"loan_amount": 20000.0})
	if err != nil || !complies {
		t.Fatalf("fallback path: complies=%v err=%v", complies, err)
	}
	if len(lm.History) != 1 {
		t.Errorf("fallback path must not call the model; calls=%d", len(lm.History))
	}
}

func TestLMReasonerDrivesCycle(t *testing.T) {
	// One combined reply serves both stages: the judge reads complies/rationale,
	// the reasoner reads decision. Order- and concurrency-independent.
	lm := &ScriptedLM{Default: Reply(
		"complies", "yes", "rationale", "within limits",
		"decision", "APPROVED at grade B",
	)}
	guru := &Persona{Name: "Guru", Instructions: "Underwrite conservatively."}
	w := &Workflow{Name: "Underwriting Workflow"}
	w.AddStep("Decide.", guru)
	proc := &Process{Name: "Underwriting", Description: "Underwrite a loan."}
	proc.AddWorkflow(w)

	rt := NewRuntime("Credit Risk Runtime", WithLM(lm))
	rt.AddProcess(proc)
	rt.AddPolicy(&Policy{Name: "DTI", Statement: "debt-to-income must not exceed 0.43"})

	decision, err := rt.Reason(context.Background(), NewIntent("Underwrite a loan",
		map[string]any{"debt_to_income": 0.28}), nil)
	if err != nil {
		t.Fatalf("cycle errored: %v", err)
	}
	if decision != "APPROVED at grade B" {
		t.Errorf("decision = %v", decision)
	}
	// The deliberation record is attributed to the model, not the fallback.
	var attributed bool
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "deliberation" && rec.Model == "llm" {
			attributed = true
		}
	}
	if !attributed {
		t.Error("deliberation record should be attributed to the llm")
	}
}

func TestWithLMPolicyBlocksCycle(t *testing.T) {
	lm := &ScriptedLM{Default: Reply("complies", "no", "rationale", "DTI too high")}
	proc := &Process{Name: "Underwriting", Description: "Underwrite a loan."}
	proc.AddWorkflow(&Workflow{Name: "W"})
	rt := NewRuntime("R", WithLM(lm))
	rt.AddProcess(proc)
	rt.AddPolicy(&Policy{Name: "DTI Ceiling", Statement: "debt-to-income must not exceed 0.43"})

	_, err := rt.Reason(context.Background(), NewIntent("Underwrite a loan",
		map[string]any{"debt_to_income": 0.60}), nil)
	var pv *PolicyViolationError
	if !errors.As(err, &pv) {
		t.Fatalf("expected the model-judged policy to block the cycle, got %v", err)
	}
}

type failingLM struct{}

func (failingLM) Complete(context.Context, string, string, string) (string, error) {
	return "", errors.New("provider down")
}

func TestLMJudgeFailurePropagates(t *testing.T) {
	proc := &Process{Name: "P"}
	proc.AddWorkflow(&Workflow{Name: "W"})
	rt := NewRuntime("R", WithLM(failingLM{}))
	rt.AddProcess(proc)
	rt.AddPolicy(&Policy{Name: "P", Statement: "must hold"})
	_, err := rt.Reason(context.Background(), NewIntent("go", nil), nil)
	if err == nil || !strings.Contains(err.Error(), "provider down") {
		t.Fatalf("a failing judge must fail the cycle closed, got %v", err)
	}
}
