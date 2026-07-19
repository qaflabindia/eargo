package ear

import (
	"testing"
)

func TestModelSelectionParse(t *testing.T) {
	s := StrategyFromMarkdown("# Memory\n\n## Model Selection\n\n" +
		"Reason with anthropic/claude-opus-4-8, reading the credential from " +
		"ANTHROPIC_API_KEY, at a temperature of 0.2, up to 8000 tokens.\n")
	if s.Provider != "anthropic" || s.Model != "anthropic/claude-opus-4-8" {
		t.Errorf("provider/model = %q / %q", s.Provider, s.Model)
	}
	if s.APIKeyEnvVar != "ANTHROPIC_API_KEY" {
		t.Errorf("key env var = %q", s.APIKeyEnvVar)
	}
	if s.Temperature == nil || *s.Temperature != 0.2 {
		t.Errorf("temperature = %v", s.Temperature)
	}
	if s.MaxOutputTokens != 8000 {
		t.Errorf("max tokens = %d", s.MaxOutputTokens)
	}
}

func TestModelClientGracefulWhenNoKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "") // credential absent
	s := StrategyFromMarkdown("## Model Selection\n\nReason with anthropic/claude-opus-4-8, key from ANTHROPIC_API_KEY.\n")
	if _, ok := s.ModelClient(); ok {
		t.Error("no credential in the environment must degrade to no client, not crash")
	}
}

func TestModelClientBuildsWhenKeyPresent(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-not-a-real-key")
	s := StrategyFromMarkdown("## Model Selection\n\nReason with anthropic/claude-opus-4-8, key from ANTHROPIC_API_KEY.\n")
	c, ok := s.ModelClient()
	if !ok {
		t.Fatal("a present credential should build the client")
	}
	if c.Provider != "anthropic" || c.bareModel() != "claude-opus-4-8" {
		t.Errorf("client = %q / %q", c.Provider, c.bareModel())
	}
}

func TestLoaderAttachesModelFromMarkdown(t *testing.T) {
	md := "## Model Selection\n\nReason with anthropic/claude-opus-4-8, credential from ANTHROPIC_API_KEY.\n"

	// With the credential absent, the runtime stays deterministic.
	t.Setenv("ANTHROPIC_API_KEY", "")
	rt := NewRuntime("R")
	applyMemoryStrategy(rt, md)
	if rt.LM != nil {
		t.Error("no key -> runtime must stay on the deterministic fallback")
	}

	// With the credential present, the model is bound across the seams.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	rt2 := NewRuntime("R")
	applyMemoryStrategy(rt2, md)
	if rt2.LM == nil {
		t.Fatal("declared model + present key -> runtime.LM should be bound")
	}
	if _, ok := rt2.Reasoner.(LMReasoner); !ok {
		t.Errorf("reasoner should be LMReasoner, got %T", rt2.Reasoner)
	}
}
