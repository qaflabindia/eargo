package ear

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// The runtime's per-cycle pipeline, each operation a named step so concerns
// AI runtimes often blur together stay distinct:
//
//	govern -> discover -> select -> compose -> schedule -> delegate ->
//	recall -> reason -> explain -> audit -> learn -> adapt
//
// The judgment-laden steps reason against a live LLM in the Python package;
// here each falls back to the same deterministic behaviour that package uses
// with no model bound. Rather than one empty struct per step, the mechanical
// steps are Runtime methods and the two real seams (PolicyJudge, Reasoner)
// are interfaces (see stage.go).

// ApprovalVerdict is a human's verdict on an approval-gated policy.
type ApprovalVerdict struct {
	Approver string
	Verdict  *bool // nil = pending, true = approved, false = rejected
	Note     string
}

// judgment is one policy's judged result, carried back from a concurrent
// fan-out so the trail can be written in deterministic policy order.
type judgment struct {
	policy    *Policy
	complies  bool
	rationale string
	err       error
}

// govern judges a set of policies against the intent and returns the
// unresolved violations. Judgment is fanned out concurrently -- independent
// per policy, and a network round-trip each when the judge is an LLM -- then
// folded back in order so the audit trail stays deterministic. A judge error
// fails the cycle closed rather than passing governance silently.
func (r *Runtime) govern(ctx context.Context, policies []*Policy, intent Intent, approval *ApprovalVerdict) ([]*Policy, error) {
	if len(policies) == 0 {
		return nil, nil
	}
	results := parallelMap(ctx, policies, func(ctx context.Context, policy *Policy) judgment {
		complies, rationale, err := r.PolicyJudge.Judge(ctx, policy, intent.Context)
		return judgment{policy, complies, rationale, err}
	})

	var violations []*Policy
	for _, res := range results {
		if res.err != nil {
			return nil, fmt.Errorf("judging policy %q: %w", res.policy.Name, res.err)
		}
		policy, complies, rationale := res.policy, res.complies, res.rationale

		gated := !complies && policy.ApprovalRequired
		var verdict *bool
		if gated && approval != nil {
			verdict = approval.Verdict
		}
		offList := verdict != nil && *verdict && !policy.ApproverAllowed(approval.Approver)
		waived := verdict != nil && *verdict && !offList

		stage := "policy"
		if gated && verdict != nil {
			stage = "approval"
		}
		var output string
		switch {
		case complies:
			output = "complies"
		case !gated:
			output = "VIOLATED"
		case verdict == nil:
			output = "PENDING APPROVAL"
		case offList:
			output = fmt.Sprintf("REFUSED -- %s is not an approved approver for %s (allowed: %s)",
				approvalName(approval), policy.Name, strings.Join(policy.Approvers, ", "))
		case waived:
			output = "approved by " + approvalName(approval)
		default:
			output = "REJECTED by " + approvalName(approval)
		}
		if gated && verdict != nil && approval.Note != "" {
			rationale += " | approver: " + approval.Note
		}
		if r.ReasoningLog != nil {
			r.ReasoningLog.Record(Record{
				Stage:     stage,
				Inputs:    map[string]any{"policy": policy.Name, "statement": policy.Statement, "context": intent.Context},
				Output:    output,
				Rationale: rationale,
			})
		}
		if !complies && !waived {
			violations = append(violations, policy)
		}
	}
	return violations, nil
}

func workflowPolicies(workflows []*Workflow) []*Policy {
	var policies []*Policy
	for _, w := range workflows {
		policies = append(policies, w.Policies...)
	}
	return policies
}

func approvalName(a *ApprovalVerdict) string {
	if a == nil || a.Approver == "" {
		return "an unnamed approver"
	}
	return a.Approver
}

// discover finds which processes are relevant to an intent. Deterministically
// this is keyword overlap against process names.
func (r *Runtime) discover(intent Intent) []*Process {
	found := discoverByKeyword(r.Processes, intent)
	if r.ReasoningLog != nil {
		r.ReasoningLog.Record(Record{
			Stage:  "discovery",
			Inputs: map[string]any{"intent": intent.Text, "available_processes": processNames(r.Processes)},
			Output: orNone(processNames(found)),
		})
	}
	return found
}

func discoverByKeyword(processes []*Process, intent Intent) []*Process {
	words := keywords(intent.Text)
	if len(words) == 0 {
		return append([]*Process{}, processes...)
	}
	var matches []*Process
	for _, p := range processes {
		lower := strings.ToLower(p.Name)
		for w := range words {
			if strings.Contains(lower, w) {
				matches = append(matches, p)
				break
			}
		}
	}
	if len(matches) == 0 {
		return append([]*Process{}, processes...)
	}
	return matches
}

// selectProcesses chooses which discovered processes run this cycle.
// Deterministically it deduplicates in discovery order.
func selectProcesses(candidates []*Process) []*Process {
	seen := make(map[string]bool, len(candidates))
	out := make([]*Process, 0, len(candidates))
	for _, p := range candidates {
		if !seen[p.Name] {
			seen[p.Name] = true
			out = append(out, p)
		}
	}
	return out
}

// compose flattens the workflows of every selected process into one plan.
func compose(selected []*Process) []*Workflow {
	var plan []*Workflow
	for _, p := range selected {
		plan = append(plan, p.Workflows...)
	}
	return plan
}

// schedule orders the composed plan. Deterministically it keeps composition
// order (a defensive copy).
func schedule(plan []*Workflow) []*Workflow {
	return append([]*Workflow{}, plan...)
}

// recall recalls the Memory context relevant to a cycle. Deterministically
// it returns the full context window, so nothing is lost offline.
func (r *Runtime) recall() string { return r.Memory.ContextWindow() }

// explain renders why a decision was reached. Deterministically it pairs the
// evidence basis with the decision.
func explain(evidence *Evidence, decision any) string {
	return fmt.Sprintf("%s -> %v", evidence.Basis, decision)
}

// audit records that the audit point was passed. Deterministically there is
// no model to assess the evidence, so only the control fact is recorded.
func audit(evidence *Evidence) *Evidence {
	if _, ok := evidence.Sources["audited"]; !ok {
		evidence.Sources["audited"] = true
	}
	return evidence
}

// validate rejects a malformed (empty) decision before the next step trusts
// it.
func validate(decision any) (any, error) {
	if s, ok := decision.(string); ok && strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("validator rejected an empty decision")
	}
	return decision, nil
}

func processNames(processes []*Process) []string {
	names := make([]string, len(processes))
	for i, p := range processes {
		names[i] = p.Name
	}
	return names
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// sortedKeys returns a map's keys in sorted order, for deterministic output.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
