package ear

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Policy is governance mapped onto one or more processes. Its statement is
// plain English an LLM judges against the intent's context; the optional
// FallbackExpression (a short boolean/arithmetic expression over the same
// context, safely evaluated) enforces it deterministically when no model is
// configured, so governance never silently passes through.
type Policy struct {
	Name               string
	Statement          string
	FallbackExpression string
	ApprovalRequired   bool
	Approvers          []string
	Escalation         string
	EscalationDays     *float64

	// Gate, when non-nil, is a custom judgment for this policy -- the Go analog
	// of subclassing Policy.judge in the Python package. govern calls it in
	// place of the runtime's PolicyJudge, passing that judge in so the Gate can
	// delegate the model-judged part of its decision (an authority envelope,
	// say, judging scope and tier above a deterministic, non-waivable floor).
	//
	// It must treat the context map as read-only -- policies are judged
	// concurrently over one shared context, so a Gate that needs to enrich the
	// context judges over a copy.
	Gate PolicyGate `json:"-"`
}

// PolicyGate is a policy's own judgment: it receives the runtime's PolicyJudge
// (to delegate any model-judged sub-decision), the policy, and the shared
// read-only context, and returns whether the policy is satisfied.
type PolicyGate func(ctx context.Context, base PolicyJudge, policy *Policy, context map[string]any) (complies bool, rationale string, err error)

// ApproverAllowed reports whether approver may waive this gate. With no
// declared allow-list anyone may; otherwise only a listed name/address,
// matched case- and punctuation-insensitively.
func (p *Policy) ApproverAllowed(approver string) bool {
	if len(p.Approvers) == 0 {
		return true
	}
	want := Normalize(approver)
	for _, allowed := range p.Approvers {
		if Normalize(allowed) == want {
			return true
		}
	}
	return false
}

// Judge judges the policy against the context and returns (complies,
// rationale). With no model bound it evaluates the fallback expression; a
// policy with neither model nor fallback is treated as not applicable. This
// Go port has no live-LLM path, so judgment is always the deterministic
// fallback -- exactly what the Python package does when no model is bound.
func (p *Policy) Judge(context map[string]any) (bool, string) {
	if p.FallbackExpression == "" {
		return true, "no model active and no fallback expression -- policy treated as not applicable"
	}
	value, err := SafeEval(p.FallbackExpression, context)
	if err != nil {
		var missing *MissingVariableError
		if errors.As(err, &missing) {
			return true, "not applicable to this intent: " + missing.Error()
		}
		return true, "fallback expression could not be evaluated: " + err.Error()
	}
	complies := truthy(value)
	return complies, fmt.Sprintf("fallback expression '%s' evaluated to %v", p.FallbackExpression, complies)
}

// Evaluate returns true when the policy is satisfied (or not applicable).
func (p *Policy) Evaluate(context map[string]any) bool {
	complies, _ := p.Judge(context)
	return complies
}

// ToMarkdown renders this policy the way policy.md stacks one.
func (p *Policy) ToMarkdown() string {
	lines := []string{"## " + p.Name, ""}
	if p.FallbackExpression != "" {
		lines = append(lines, "Fallback: "+p.FallbackExpression)
	}
	if p.ApprovalRequired {
		lines = append(lines, "Approval: required")
	}
	if len(p.Approvers) > 0 {
		lines = append(lines, "Approvers: "+strings.Join(p.Approvers, ", "))
	}
	if p.Escalation != "" {
		lines = append(lines, "Escalate: "+p.Escalation)
	}
	lines = append(lines, "")
	if p.Statement != "" {
		lines = append(lines, p.Statement)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}
