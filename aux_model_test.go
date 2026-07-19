package ear

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStrategyParsesAuxiliaryModelWithoutCollision(t *testing.T) {
	md := "# Strategy\n\n" +
		"## Model Selection\n\nReason with anthropic/claude-opus-4-8, reading the credential from PRIMARY_KEY, at a temperature of 0.2.\n\n" +
		"## Auxiliary Model\n\nSummarize with anthropic/claude-haiku-4-5, reading the credential from AUX_KEY.\n"
	s := StrategyFromMarkdown(md)

	if s.Model != "anthropic/claude-opus-4-8" || s.APIKeyEnvVar != "PRIMARY_KEY" {
		t.Errorf("primary = {model:%q key:%q}", s.Model, s.APIKeyEnvVar)
	}
	if s.AuxModel != "anthropic/claude-haiku-4-5" || s.AuxAPIKeyEnvVar != "AUX_KEY" {
		t.Errorf("auxiliary = {model:%q key:%q}", s.AuxModel, s.AuxAPIKeyEnvVar)
	}
	// The primary temperature must not bleed into the auxiliary fields.
	if s.AuxTemperature != nil {
		t.Errorf("auxiliary temperature leaked: %v", *s.AuxTemperature)
	}
}

func TestAuxiliaryModelDoesTheMechanicalWork(t *testing.T) {
	primary := &ScriptedLM{Default: Reply("decision", "APPROVED")}
	aux := &ScriptedLM{Default: Reply("summary", "compressed by aux", "insight", "aux lesson")}
	rt := NewRuntime("R", WithLM(primary), WithAuxiliaryLM(aux))

	// Force a memory compression: capacity 1, so the second record overflows
	// and the summarizer runs -- it must be the auxiliary model.
	rt.Memory.Capacity = 1
	rt.Memory.Record("first", "d1", nil, nil)
	rt.Memory.Record("second", "d2", nil, nil)

	if len(rt.Memory.Compressed) != 1 || rt.Memory.Compressed[0] != "compressed by aux" {
		t.Fatalf("compression should run through the auxiliary model: %#v", rt.Memory.Compressed)
	}
	// Adaptation distillation also runs on the auxiliary model.
	rt.Experience.ObserveEntry(rt.Memory.Working[0])
	a := rt.Adaptations.LearnFrom(rt.Experience)
	if a == nil || a.Insight != "aux lesson" {
		t.Fatalf("distillation should run through the auxiliary model: %#v", a)
	}
	// The primary model was never called for mechanical work.
	if len(primary.Calls()) != 0 {
		t.Errorf("primary model was used for mechanical work: %d calls", len(primary.Calls()))
	}
	if len(aux.Calls()) != 2 {
		t.Errorf("auxiliary calls = %d, want 2 (one summarize, one distill)", len(aux.Calls()))
	}
}

func TestLoaderWiresAuxiliaryModelFromMemoryMd(t *testing.T) {
	t.Setenv("EAR_PRIMARY_KEY", "sk-primary")
	t.Setenv("EAR_AUX_KEY", "sk-aux")

	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("process.md", "# Desk\n\n## Handle\n\nHandle requests.\n\n- W\n\n## W\n\nDecide.\n")
	write("workflow.md", "## W\n\n1. Decide.\n")
	write("memory.md", "# Strategy\n\n"+
		"## Model Selection\n\nReason with anthropic/claude-opus-4-8, reading the credential from EAR_PRIMARY_KEY.\n\n"+
		"## Auxiliary Model\n\nSummarize with anthropic/claude-haiku-4-5, reading the credential from EAR_AUX_KEY.\n")

	rt, err := LoadRuntime(dir, "Desk")
	if err != nil {
		t.Fatal(err)
	}
	if rt.LM == nil {
		t.Fatal("primary model not bound")
	}
	if rt.AuxLM == nil {
		t.Fatal("auxiliary model declared in memory.md but not wired")
	}
	if rt.LM == rt.AuxLM {
		t.Error("auxiliary model should be a distinct client from the primary")
	}
}

func TestAuxiliaryModelAbsentLeavesPrimaryOnMechanicalWork(t *testing.T) {
	primary := &ScriptedLM{Default: Reply("summary", "compressed by primary")}
	rt := NewRuntime("R", WithLM(primary)) // no auxiliary
	if rt.AuxLM != nil {
		t.Fatal("no auxiliary should be bound")
	}
	rt.Memory.Capacity = 1
	rt.Memory.Record("first", "d1", nil, nil)
	rt.Memory.Record("second", "d2", nil, nil)
	if len(rt.Memory.Compressed) != 1 || rt.Memory.Compressed[0] != "compressed by primary" {
		t.Errorf("without an auxiliary, the primary should compress: %#v", rt.Memory.Compressed)
	}
}
