package ear

import (
	"context"
	"fmt"
)

// This file wires EAR's DSPy-equivalent (the judgment engine + signatures)
// into the runtime's two seams. LMReasoner and LMJudge are drop-in
// implementations of Reasoner and PolicyJudge that reason against a live LM;
// pass either via WithReasoner/WithPolicyJudge, or both at once via WithLM.
// With no LM configured the runtime keeps its deterministic defaults, so the
// seam is opt-in and the offline path is unchanged.

// LMReasoner deliberates by running the ReasonAboutIntent signature against a
// model. It renders the same stacked-capabilities block the deterministic
// reasoner uses, so the author's stacking is what the model sees.
type LMReasoner struct {
	LM    LM
	Model string // label for the audit trail; defaults to "llm"
}

// NewLMReasoner builds an LMReasoner over lm.
func NewLMReasoner(lm LM) LMReasoner { return LMReasoner{LM: lm} }

// Reason runs the intent through the model and returns the decision text.
func (r LMReasoner) Reason(ctx context.Context, rt *Runtime, intent Intent, plan []*Workflow) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	capabilities := renderCapabilities(plan)
	if capabilities == "" {
		capabilities = "none"
	}
	reasoningContext := map[string]any{}
	for k, v := range intent.Context {
		reasoningContext[k] = v
	}
	reasoningContext["_runtime_name"] = rt.Name
	reasoningContext["_available_processes"] = orNone(processNames(rt.Processes))
	if window := rt.Memory.ContextWindow(); window != "" {
		reasoningContext["_remembered_context"] = window
	}
	if rt.Strategy != nil {
		if narrative := rt.Strategy.Ontology.Render(); narrative != "" {
			reasoningContext["_operating_strategy"] = narrative
		}
	}

	pred, err := ReasonAboutIntent.Run(ctx, r.LM, map[string]any{
		"intent":       intent.Text,
		"context":      reasoningContext,
		"capabilities": capabilities,
	})
	if err != nil {
		return nil, fmt.Errorf("reasoning against the model: %w", err)
	}
	decision := pred.Text("decision")
	if rt.ReasoningLog != nil {
		rt.ReasoningLog.Record(Record{
			Stage:  "deliberation",
			Inputs: map[string]any{"intent": intent.Text, "context": intent.Context, "capabilities": capabilities},
			Output: decision,
			Model:  r.modelLabel(),
		})
	}
	return decision, nil
}

func (r LMReasoner) modelLabel() string {
	if r.Model != "" {
		return r.Model
	}
	return "llm"
}

// LMJudge judges a policy's plain-English statement against context by
// running the JudgePolicyCompliance signature. A policy with no statement is
// treated as having nothing to judge (complies), matching the Python
// package. If the model call fails, the error propagates so the Governor
// fails the cycle closed rather than passing governance silently.
type LMJudge struct {
	LM LM
	// Fallback, when set, judges a policy that has no statement but does have
	// a fallback expression -- so a stack can mix natural-language and
	// deterministic policies under one judge. Defaults to DeterministicJudge.
	Fallback PolicyJudge
}

// NewLMJudge builds an LMJudge over lm, with the deterministic evaluator as
// the fallback for statement-less policies.
func NewLMJudge(lm LM) LMJudge { return LMJudge{LM: lm, Fallback: DeterministicJudge{}} }

// Judge judges the policy, preferring its natural-language statement.
func (j LMJudge) Judge(ctx context.Context, policy *Policy, context map[string]any) (bool, string, error) {
	if policy.Statement == "" {
		fallback := j.Fallback
		if fallback == nil {
			fallback = DeterministicJudge{}
		}
		return fallback.Judge(ctx, policy, context)
	}
	pred, err := JudgePolicyCompliance.Run(ctx, j.LM, map[string]any{
		"policy_statement": policy.Statement,
		"context":          context,
	})
	if err != nil {
		return false, "", fmt.Errorf("judging policy %q against the model: %w", policy.Name, err)
	}
	return pred.Bool("complies"), pred.Text("rationale"), nil
}

// WithLM wires both seams -- deliberation and policy judging -- to reason
// against lm. Equivalent to WithReasoner(NewLMReasoner(lm)) plus
// WithPolicyJudge(NewLMJudge(lm)).
func WithLM(lm LM) Option {
	return func(r *Runtime) {
		r.Reasoner = NewLMReasoner(lm)
		r.PolicyJudge = NewLMJudge(lm)
		r.LM = lm
	}
}
