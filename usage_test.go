package ear

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestAnthropicParseUsage(t *testing.T) {
	c := &HTTPClient{Provider: "anthropic"}
	_, _, _, parse := c.anthropic("p", "s", "")
	text, usage := parse(map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "hi"}},
		"usage": map[string]any{
			"input_tokens": float64(12), "output_tokens": float64(3),
			"cache_read_input_tokens": float64(4), "cache_creation_input_tokens": float64(5),
		},
	})
	if text != "hi" {
		t.Errorf("text = %q", text)
	}
	if usage.PromptTokens != 12 || usage.CompletionTokens != 3 || usage.CacheReadTokens != 4 || usage.CacheWriteTokens != 5 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestOpenAIParseUsage(t *testing.T) {
	c := &HTTPClient{Provider: "openai"}
	_, _, _, parse := c.openai("p", "s")
	text, usage := parse(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": "ok"}}},
		"usage": map[string]any{
			"prompt_tokens": float64(30), "completion_tokens": float64(7),
			"prompt_tokens_details": map[string]any{"cached_tokens": float64(10)},
		},
	})
	if text != "ok" {
		t.Errorf("text = %q", text)
	}
	if usage.PromptTokens != 30 || usage.CompletionTokens != 7 || usage.CacheReadTokens != 10 {
		t.Errorf("usage = %+v", usage)
	}
}

// meteredLM reports fixed token usage per call and exposes its history, so a
// cycle's accounting can be checked without a network.
type meteredLM struct {
	mu    sync.Mutex
	reply string
	calls []Call
}

func (m *meteredLM) Complete(_ context.Context, prompt, system, cachePrefix string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, Call{
		Prompt: prompt, System: system, CachePrefix: cachePrefix, Reply: m.reply,
		Usage: Usage{PromptTokens: 100, CompletionTokens: 20}, LatencyMs: 5,
	})
	return m.reply, nil
}

func (m *meteredLM) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Call{}, m.calls...)
}

func TestCycleAccountsModelUsage(t *testing.T) {
	lm := &meteredLM{reply: Reply("complies", "yes", "rationale", "ok", "decision", "APPROVED")}
	proc := &Process{Name: "Underwriting", Description: "Underwrite a loan."}
	proc.AddWorkflow((&Workflow{Name: "W"}).AddStep("Decide.", nil))
	rt := NewRuntime("R", WithLM(lm))
	rt.AddProcess(proc)
	rt.AddPolicy(&Policy{Name: "DTI", Statement: "debt-to-income must not exceed 0.43"})

	if _, err := rt.Reason(context.Background(), NewIntent("Underwrite a loan",
		map[string]any{"debt_to_income": 0.28}), nil); err != nil {
		t.Fatalf("cycle errored: %v", err)
	}

	var usage string
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "usage" {
			usage = rec.Output
		}
	}
	// The wired pipeline calls the model in several judged stages (govern,
	// discover, reason, explain, audit); the usage record must match however
	// many calls actually fired -- each metered at 100 in / 20 out.
	n := len(lm.Calls())
	if n < 2 {
		t.Fatalf("expected the model-bound cycle to make several calls, got %d", n)
	}
	wantCalls := fmt.Sprintf("%d model calls", n)
	wantTokens := fmt.Sprintf("%d+%d tokens", n*100, n*20)
	if !strings.Contains(usage, wantCalls) || !strings.Contains(usage, wantTokens) {
		t.Errorf("usage accounting = %q; want %q and %q", usage, wantCalls, wantTokens)
	}
}

func TestDeterministicCycleReportsNoModelCalls(t *testing.T) {
	rt := buildRuntime()
	_, _ = rt.Reason(context.Background(), NewIntent("Underwrite a loan",
		map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28}), nil)
	var usage string
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "usage" {
			usage = rec.Output
		}
	}
	if !strings.Contains(usage, "deterministic fallbacks") {
		t.Errorf("expected deterministic-fallback usage, got %q", usage)
	}
}
