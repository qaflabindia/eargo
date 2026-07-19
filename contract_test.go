package ear

import (
	"context"
	"strings"
	"testing"
)

func TestContractExtractTyped(t *testing.T) {
	lm := &ScriptedLM{Default: Reply("risk_grade", "B", "amount", "20000")}
	c := &Contract{Name: "Deliverable"}
	c.AddField("risk grade", "a letter A-E")
	c.AddField("amount", "the approved amount")

	data, err := c.Extract(context.Background(), lm, "APPROVE at grade B for $20,000", "underwrite", "")
	if err != nil {
		t.Fatal(err)
	}
	if data["risk grade"] != "B" {
		t.Errorf("risk grade = %v", data["risk grade"])
	}
	// "amount" is number-like, so Coerce types it as a float64.
	if data["amount"] != float64(20000) {
		t.Errorf("amount = %v (%T)", data["amount"], data["amount"])
	}
}

func TestContractJudgeWithModel(t *testing.T) {
	lm := &ScriptedLM{Default: Reply("conforms", "yes", "rationale", "honours the meanings")}
	c := &Contract{Name: "Deliverable"}
	c.AddField("risk grade", "a letter A-E")
	conforms, rationale, err := c.JudgeWithModel(context.Background(), lm, map[string]any{"risk grade": "B"})
	if err != nil {
		t.Fatal(err)
	}
	if !conforms || !strings.Contains(rationale, "honours") {
		t.Errorf("conforms=%v rationale=%q", conforms, rationale)
	}
}

// evidenceCapture grabs the built evidence so a test can inspect what the
// cycle attached to the decision.
type evidenceCapture struct{ ev **Evidence }

func (evidenceCapture) Name() string { return "capture" }
func (s evidenceCapture) Run(c *Cycle) error {
	*s.ev = c.Evidence
	return nil
}

func TestCycleFormalizesContractIntoEvidence(t *testing.T) {
	lm := &ScriptedLM{Default: Reply(
		"complies", "yes", "rationale", "ok",
		"decision", "APPROVE at grade B",
		"risk_grade", "B", // extraction fills the "risk grade" field
		"conforms", "yes",
		"explanation", "because B", "assessment", "supported",
	)}
	guru := &Persona{Name: "Guru"}
	w := &Workflow{Name: "Underwriting Workflow"}
	w.AddStep("Decide and grade.", guru)
	w.Contract = (&Contract{Name: "W Deliverable"}).AddField("risk grade", "a letter A-E")
	proc := &Process{Name: "Underwriting", Description: "Underwrite a loan."}
	proc.AddWorkflow(w)

	rt := NewRuntime("R", WithLM(lm))
	rt.AddProcess(proc)
	var captured *Evidence
	rt.Pipeline = append(rt.Pipeline, evidenceCapture{&captured})

	if _, err := rt.Reason(context.Background(), NewIntent("Underwrite a loan", nil), nil); err != nil {
		t.Fatalf("cycle errored: %v", err)
	}

	// A conformant contract record lands on the trail...
	var sawConformant bool
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "contract" && strings.Contains(rec.Output, "conformant") {
			sawConformant = true
		}
	}
	if !sawConformant {
		t.Error("expected a conformant contract record on the trail")
	}

	// ...and the extracted deliverable reaches the decision's evidence.
	if captured == nil {
		t.Fatal("evidence was not captured")
	}
	data, ok := captured.Sources["data"].(map[string]any)
	if !ok || data["risk grade"] != "B" {
		t.Errorf("evidence data = %v", captured.Sources["data"])
	}
}

func TestCycleWithdrawsNonconformingContract(t *testing.T) {
	// The model never conforms; even after the hinted retry the data is
	// withheld and the record says so.
	lm := &ScriptedLM{Default: Reply(
		"complies", "yes", "decision", "APPROVE",
		"risk_grade", "Z", // not a valid A-E grade
		"conforms", "no", "rationale", "Z is not a letter A-E",
		"explanation", "x", "assessment", "y",
	)}
	w := &Workflow{Name: "W"}
	w.AddStep("Decide.", &Persona{Name: "G"})
	w.Contract = (&Contract{Name: "W Deliverable"}).AddField("risk grade", "a letter A-E")
	proc := &Process{Name: "P", Description: "Do it."}
	proc.AddWorkflow(w)
	rt := NewRuntime("R", WithLM(lm))
	rt.AddProcess(proc)
	var captured *Evidence
	rt.Pipeline = append(rt.Pipeline, evidenceCapture{&captured})

	if _, err := rt.Reason(context.Background(), NewIntent("go", nil), nil); err != nil {
		t.Fatalf("cycle errored: %v", err)
	}
	var sawNonconforming bool
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "contract" && strings.Contains(rec.Output, "NONCONFORMING") {
			sawNonconforming = true
		}
	}
	if !sawNonconforming {
		t.Error("expected a NONCONFORMING contract record")
	}
	if _, hasData := captured.Sources["data"]; hasData {
		t.Error("nonconforming data must be withheld from the evidence")
	}
}
