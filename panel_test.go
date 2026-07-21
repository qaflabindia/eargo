package ear

import (
	"context"
	"strings"
	"testing"
)

// panelStack builds a runtime whose one workflow carries an authored Pattern
// and two delegated personas, so the reason stage convenes a panel.
func panelStack(t *testing.T, opts ...Option) *Runtime {
	t.Helper()
	risk := &Persona{Name: "Risk Officer", Instructions: "Guard the downside."}
	growth := &Persona{Name: "Growth Lead", Instructions: "Find the upside."}
	wf := &Workflow{Name: "Deliberation", Pattern: "adversarial debate; the risk officer has the last word"}
	wf.AddStep("Weigh the risk.", risk)
	wf.AddStep("Weigh the opportunity.", growth)
	proc := &Process{Name: "Decide", Description: "Deliberate the call."}
	proc.AddWorkflow(wf)
	rt := NewRuntime("Boardroom", opts...)
	rt.AddProcess(proc)
	return rt
}

func TestPanelDeterministicRotation(t *testing.T) {
	rt := panelStack(t) // no model bound
	decision, err := rt.Reason(context.Background(), NewIntent("Enter the new market?", nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := decision.(string)
	if !strings.Contains(text, "no model bound") || !strings.Contains(text, "Risk Officer") || !strings.Contains(text, "Growth Lead") {
		t.Errorf("deterministic panel should name its personas and say no model synthesized: %q", text)
	}
	// Each persona took at least one rotated turn, on the trail.
	var conversationTurns int
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "conversation" {
			conversationTurns++
		}
	}
	if conversationTurns < 2 {
		t.Errorf("conversation turns = %d, want at least one per persona", conversationTurns)
	}
}

func TestPanelSingleVoicedWithoutPattern(t *testing.T) {
	// A workflow with no Pattern reasons single-voiced -- no conversation
	// records, the ordinary deliberation path.
	guru := &Persona{Name: "Analyst", Instructions: "Assess."}
	wf := (&Workflow{Name: "Straight"}).AddStep("Decide.", guru)
	proc := &Process{Name: "P", Description: "Decide."}
	proc.AddWorkflow(wf)
	rt := NewRuntime("R")
	rt.AddProcess(proc)
	if _, err := rt.Reason(context.Background(), NewIntent("go", nil), nil); err != nil {
		t.Fatal(err)
	}
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "conversation" {
			t.Fatal("a patternless workflow must not convene a panel")
		}
	}
}

func TestPanelSinglePersonaStaysSingleVoiced(t *testing.T) {
	// A Pattern with only one persona has nobody to deliberate with.
	solo := &Persona{Name: "Solo", Instructions: "Decide alone."}
	wf := &Workflow{Name: "OneVoice", Pattern: "debate"}
	wf.AddStep("Decide.", solo)
	proc := &Process{Name: "P", Description: "Decide."}
	proc.AddWorkflow(wf)
	rt := NewRuntime("R")
	rt.AddProcess(proc)
	if _, err := rt.Reason(context.Background(), NewIntent("go", nil), nil); err != nil {
		t.Fatal(err)
	}
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "conversation" {
			t.Fatal("a one-persona pattern should not convene a panel")
		}
	}
}

func TestPanelLiveModeratesSpeaksAndSynthesizes(t *testing.T) {
	// The moderator names a speaker each turn; personas speak; synthesis
	// concludes. The scripted default answers every signature field the
	// pipeline touches, so the cycle runs end to end.
	lm := &ScriptedLM{Default: Reply(
		"complies", "yes",
		"speaker", "Risk Officer", "rationale", "the pattern gives risk the floor",
		"statement", "The downside is capped; proceed carefully.",
		"decision", "PROCEED, with a capped exposure.",
		"explanation", "risk and growth aligned on a capped entry",
		"assessment", "supported",
	)}
	rt := panelStack(t, WithLM(lm))
	decision, err := rt.Reason(context.Background(), NewIntent("Enter the new market?", nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := decision.(string); !strings.Contains(got, "PROCEED") {
		t.Errorf("panel decision should be the synthesis: %q", got)
	}
	// The deliberation record is the panel's synthesis, and turns were logged.
	var sawSynthesis, sawConversation bool
	for rec := range rt.ReasoningLog.Records() {
		switch rec.Stage {
		case "deliberation":
			if strings.Contains(rec.Output, "PROCEED") {
				sawSynthesis = true
			}
		case "conversation":
			sawConversation = true
		}
	}
	if !sawSynthesis {
		t.Error("the cycle's deliberation record should carry the panel synthesis")
	}
	if !sawConversation {
		t.Error("panel turns should land on the trail as conversation records")
	}
}

func TestPanelConclusionRefusedBeforeEveryoneSpeaks(t *testing.T) {
	// The moderator says "conclude" immediately; the guard refuses until every
	// persona has spoken, and the refusal is on the record.
	lm := &ScriptedLM{Default: Reply(
		"complies", "yes",
		"speaker", "conclude", "rationale", "I think we are done",
		"statement", "A turn.",
		"decision", "DECIDED.",
		"explanation", "x", "assessment", "y",
	)}
	rt := panelStack(t, WithLM(lm))
	if _, err := rt.Reason(context.Background(), NewIntent("Decide already", nil), nil); err != nil {
		t.Fatal(err)
	}
	var refusals, turns int
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "conversation" {
			turns++
			if by, _ := rec.Inputs["chosen_by"].(string); strings.Contains(by, "conclusion refused") {
				refusals++
			}
		}
	}
	if turns < 2 {
		t.Errorf("both personas should have spoken despite the early conclude, turns = %d", turns)
	}
	if refusals == 0 {
		t.Error("an early conclusion before everyone spoke should be refused on the record")
	}
}

func TestPanelCall(t *testing.T) {
	a := &Persona{Name: "A"}
	b := &Persona{Name: "B"}
	w1 := &Workflow{Name: "W1", Pattern: "debate"}
	w1.AddStep("s", a)
	w1.AddStep("s", b)
	w2 := &Workflow{Name: "W2", Pattern: "review"}
	w2.AddStep("s", b) // b already seen -> de-duplicated
	style, personas := panelCall([]*Workflow{w1, w2})
	if style != "debate; review" {
		t.Errorf("styles should join: %q", style)
	}
	if len(personas) != 2 || personas[0] != a || personas[1] != b {
		t.Errorf("personas should be de-duplicated in order: %v", personaNames(personas))
	}
	// A patternless plan yields nothing to convene.
	if style, personas := panelCall([]*Workflow{{Name: "plain"}}); style != "" || len(personas) != 0 {
		t.Errorf("a patternless plan should not convene: %q %v", style, personas)
	}
}
