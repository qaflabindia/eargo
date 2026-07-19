package ear

import (
	"context"
	"strings"
	"testing"
)

func TestProgressiveSkillSelection(t *testing.T) {
	// The model ranks two of the three skills relevant; only those are stacked.
	lm := &ScriptedLM{Default: Reply(
		"complies", "yes", "rationale", "ok",
		"relevant skill names", "- band_credit\n- assign_grade",
		"decision", "APPROVED",
		"explanation", "x", "assessment", "y",
	)}
	guru := &Persona{Name: "Guru"}
	guru.AddSkill(&Skill{Name: "band_credit", Prompt: "band it"})
	guru.AddSkill(&Skill{Name: "assign_grade", Prompt: "grade it"})
	guru.AddSkill(&Skill{Name: "gut_feel", Prompt: "wing it"})
	w := &Workflow{Name: "W"}
	w.AddStep("Do underwriting.", guru)
	proc := &Process{Name: "Underwriting", Description: "Underwrite."}
	proc.AddWorkflow(w)
	rt := NewRuntime("R", WithLM(lm))
	rt.AddProcess(proc)

	if _, err := rt.Reason(context.Background(), NewIntent("Underwrite a loan", nil), nil); err != nil {
		t.Fatal(err)
	}

	var caps string
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "deliberation" {
			if c, ok := rec.Inputs["capabilities"].(string); ok {
				caps = c
			}
		}
	}
	if !strings.Contains(caps, "Skill band_credit") || !strings.Contains(caps, "Skill assign_grade") {
		t.Errorf("selected skills missing from capabilities:\n%s", caps)
	}
	if strings.Contains(caps, "Skill gut_feel") {
		t.Errorf("unselected skill 'gut_feel' must not be stacked:\n%s", caps)
	}
}

func TestSkillSelectionSingleSkillNotRanked(t *testing.T) {
	// A persona with one skill is never ranked (the >1 guard); it stacks as-is
	// and the model is not asked to rank.
	lm := &ScriptedLM{Default: Reply("complies", "yes", "decision", "OK",
		"explanation", "x", "assessment", "y")}
	guru := &Persona{Name: "Guru"}
	guru.AddSkill(&Skill{Name: "only_skill", Prompt: "do it"})
	w := &Workflow{Name: "W"}
	w.AddStep("Go.", guru)
	proc := &Process{Name: "P", Description: "Do."}
	proc.AddWorkflow(w)
	rt := NewRuntime("R", WithLM(lm))
	rt.AddProcess(proc)

	if _, err := rt.Reason(context.Background(), NewIntent("go", nil), nil); err != nil {
		t.Fatal(err)
	}
	var caps string
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "deliberation" {
			caps, _ = rec.Inputs["capabilities"].(string)
		}
	}
	if !strings.Contains(caps, "Skill only_skill") {
		t.Errorf("single skill should be stacked as-is:\n%s", caps)
	}
}
