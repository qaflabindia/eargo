package ear

import (
	"fmt"
	"sort"
	"strings"
)

// This file holds the runtime's per-cycle pipeline stages, each its own
// type so operations AI runtimes often blur together stay distinct:
//
//	Governor -> Discoverer -> Selector -> Composer -> Scheduler ->
//	Delegator -> Recaller -> Explainer -> Auditor -> Learner -> Adapter
//
// Every judgment-laden stage reasons against a live LLM in the Python
// package; here each falls back to the same deterministic behaviour the
// Python package uses when no model is bound. The mechanical stages
// (Composer flattening, Validator shape checks) are identical either way.

// ApprovalVerdict is a human's verdict on an approval-gated policy.
type ApprovalVerdict struct {
	Approver string
	Verdict  *bool // nil = pending, true = approved, false = rejected
	Note     string
}

// Governor is the regulation gate a cycle must clear before anything else
// runs. It checks the runtime's policies against an intent's context and
// reports which are violated, writing every judgment to the ReasoningLog.
type Governor struct{}

func (Governor) govern(r *Runtime, intent Intent, approval *ApprovalVerdict) []*Policy {
	return violations(r.Policies, intent, r.ReasoningLog, approval)
}

func (Governor) governWorkflows(r *Runtime, intent Intent, workflows []*Workflow, approval *ApprovalVerdict) []*Policy {
	var policies []*Policy
	for _, w := range workflows {
		policies = append(policies, w.Policies...)
	}
	return violations(policies, intent, r.ReasoningLog, approval)
}

func violations(policies []*Policy, intent Intent, log *ReasoningLog, approval *ApprovalVerdict) []*Policy {
	var out []*Policy
	for _, policy := range policies {
		complies, rationale := policy.Judge(intent.Context)

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
			approver := approvalName(approval)
			output = fmt.Sprintf("REFUSED -- %s is not an approved approver for %s (allowed: %s)",
				approver, policy.Name, strings.Join(policy.Approvers, ", "))
		case waived:
			output = "approved by " + approvalName(approval)
		default:
			output = "REJECTED by " + approvalName(approval)
		}
		if gated && verdict != nil && approval.Note != "" {
			rationale = rationale + " | approver: " + approval.Note
		}
		if log != nil {
			log.Record(Record{
				Stage:     stage,
				Inputs:    map[string]any{"policy": policy.Name, "statement": policy.Statement, "context": intent.Context},
				Output:    output,
				Rationale: rationale,
			})
		}
		if !complies && !waived {
			out = append(out, policy)
		}
	}
	return out
}

func approvalName(a *ApprovalVerdict) string {
	if a == nil || a.Approver == "" {
		return "an unnamed approver"
	}
	return a.Approver
}

// Discoverer finds which of the runtime's processes are relevant to an
// intent. Deterministically this is keyword overlap against process names.
type Discoverer struct{}

func (Discoverer) discover(r *Runtime, intent Intent) []*Process {
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

// Selector chooses which discovered processes actually run this cycle.
// Deterministically it deduplicates in discovery order.
type Selector struct{}

func (Selector) selectProcesses(candidates []*Process) []*Process {
	seen := map[string]bool{}
	var out []*Process
	for _, p := range candidates {
		if !seen[p.Name] {
			seen[p.Name] = true
			out = append(out, p)
		}
	}
	return out
}

// Composer flattens the workflows of every selected process into one
// composed plan. Purely mechanical -- no judgment.
type Composer struct{}

func (Composer) compose(selected []*Process) []*Workflow {
	var plan []*Workflow
	for _, p := range selected {
		plan = append(plan, p.Workflows...)
	}
	return plan
}

// Scheduler orders the composed plan before execution. Deterministically it
// keeps composition order (a defensive copy).
type Scheduler struct{}

func (Scheduler) schedule(plan []*Workflow) []*Workflow {
	return append([]*Workflow{}, plan...)
}

// Delegator assigns undelegated workflow steps to personas.
// Deterministically (no model) it leaves each step exactly as authored.
type Delegator struct{}

func (Delegator) delegate(_ *Runtime, _ Intent, plan []*Workflow) []*Workflow { return plan }

// Recaller recalls the Memory context relevant to a cycle. Deterministically
// it returns the full context window, so nothing is ever lost offline.
type Recaller struct{}

func (Recaller) recall(m *Memory) string { return m.ContextWindow() }

// Explainer renders a human-readable explanation of why a decision was
// reached. Deterministically it pairs the evidence basis with the decision.
type Explainer struct{}

func (Explainer) explain(evidence *Evidence, decision any) string {
	return fmt.Sprintf("%s -> %v", evidence.Basis, decision)
}

// Auditor inspects a cycle's Evidence for compliance before it is committed
// to Memory. Deterministically it records only that the audit point passed.
type Auditor struct{}

func (Auditor) audit(evidence *Evidence) *Evidence {
	if _, ok := evidence.Sources["audited"]; !ok {
		evidence.Sources["audited"] = true
	}
	return evidence
}

// Learner folds a committed Memory entry into Experience.
type Learner struct{}

func (Learner) learn(x *Experience, entry MemoryEntry) *Experience { return x.ObserveEntry(entry) }

// Adapter periodically distills Experience into a new Adaptation, throttled
// by AdaptEvery observed cycles.
type Adapter struct {
	AdaptEvery int
}

func (a Adapter) adapt(bank *AdaptationBank, x *Experience) *Adaptation {
	if a.AdaptEvery <= 0 || x.Observations == 0 {
		return nil
	}
	if x.Observations%a.AdaptEvery != 0 {
		return nil
	}
	return bank.LearnFrom(x)
}

// Validator checks the output of every maker stage in the pipeline.
type Validator struct{}

func (Validator) validate(decision any) (any, error) {
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
