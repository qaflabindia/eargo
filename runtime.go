package ear

import (
	"fmt"
	"strings"
)

// PolicyViolationError is raised when a cycle is hard-blocked by one or more
// non-gated policy violations (or a human rejected an approval gate).
type PolicyViolationError struct {
	Scope    string
	Policies []*Policy
}

func (e *PolicyViolationError) Error() string {
	names := make([]string, len(e.Policies))
	for i, p := range e.Policies {
		names[i] = p.Name
	}
	return fmt.Sprintf("%s violated: %s", e.Scope, strings.Join(names, ", "))
}

// ApprovalRequiredError parks a cycle for a human verdict: only
// approval-gated policies remain violated and none has been rejected.
type ApprovalRequiredError struct {
	Policies []*Policy
}

func (e *ApprovalRequiredError) Error() string {
	names := make([]string, len(e.Policies))
	for i, p := range e.Policies {
		names[i] = p.Name
	}
	return "human approval required for: " + strings.Join(names, ", ")
}

// Runtime is the battlefield: every cycle runs through the full
// Governor/Discoverer/Selector/Composer/Scheduler/Delegator/Reasoner
// pipeline, and is recorded across the Evidence (why) / Memory (what) /
// Experience (pattern) / Adaptation (adaptation) layers.
type Runtime struct {
	Name        string
	Tenant      Tenant
	Processes   []*Process
	Policies    []*Policy
	Memory      *Memory
	Experience  *Experience
	Adaptations *AdaptationBank

	ReasoningLog *ReasoningLog

	// Per-cycle pipeline stages.
	Reasoner   Reasoner
	Governor   Governor
	Discoverer Discoverer
	Selector   Selector
	Composer   Composer
	Scheduler  Scheduler
	Delegator  Delegator
	Validator  Validator
	Recaller   Recaller
	Explainer  Explainer
	Auditor    Auditor
	Learner    Learner
	Adapter    Adapter
}

// NewRuntime builds a Runtime with all layers and stages initialized to
// their defaults.
func NewRuntime(name string) *Runtime {
	return &Runtime{
		Name:         name,
		Tenant:       NewTenant(),
		Memory:       NewMemory(),
		Experience:   NewExperience(),
		Adaptations:  NewAdaptationBank(),
		ReasoningLog: &ReasoningLog{},
		Adapter:      Adapter{AdaptEvery: 5},
	}
}

// AddProcess stacks a process onto the runtime.
func (r *Runtime) AddProcess(p *Process) *Runtime {
	r.Processes = append(r.Processes, p)
	return r
}

// AddPolicy attaches a runtime-wide policy.
func (r *Runtime) AddPolicy(p *Policy) *Runtime {
	r.Policies = append(r.Policies, p)
	return r
}

// EnforcePolicies returns the runtime-wide policies violated by the context.
func (r *Runtime) EnforcePolicies(context map[string]any) []*Policy {
	var out []*Policy
	for _, p := range r.Policies {
		if !p.Evaluate(context) {
			out = append(out, p)
		}
	}
	return out
}

// Reason runs one reasoning cycle end to end through the named pipeline and
// returns the decision. A hard policy block returns *PolicyViolationError; a
// parked approval gate returns *ApprovalRequiredError. approval may be nil.
func (r *Runtime) Reason(intent Intent, approval *ApprovalVerdict) (any, error) {
	r.ReasoningLog.BeginCycle(intent)

	// Governance gate 1: runtime-wide policies.
	if v := r.Governor.govern(r, intent, approval); len(v) > 0 {
		if err := r.enforce(v, approval, "Policy"); err != nil {
			return nil, err
		}
	}

	candidates := r.Discoverer.discover(r, intent)
	selected := r.Selector.selectProcesses(candidates)
	plan := r.Composer.compose(selected)
	scheduled := r.Scheduler.schedule(plan)

	// Governance gate 2: workflow-scoped policies, once the plan is known.
	if v := r.Governor.governWorkflows(r, intent, scheduled, approval); len(v) > 0 {
		if err := r.enforce(v, approval, "Workflow policy"); err != nil {
			return nil, err
		}
	}

	r.Delegator.delegate(r, intent, scheduled)
	recalled := r.Recaller.recall(r.Memory)

	decision := r.Reasoner.reason(r, intent, scheduled)
	decision, err := r.Validator.validate(decision)
	if err != nil {
		return nil, err
	}

	evidence := r.buildEvidence(intent, scheduled, recalled)
	explanation := r.Explainer.explain(evidence, decision)
	evidence.Sources["explanation"] = explanation
	r.ReasoningLog.Record(Record{
		Stage:  "explanation",
		Inputs: map[string]any{"basis": evidence.Basis, "decision": fmt.Sprint(decision)},
		Output: explanation,
	})
	r.Auditor.audit(evidence)

	entry := r.Memory.Record(intent.Text, decision, intent.Context, evidence)
	r.Learner.learn(r.Experience, entry)
	if learned := r.Adapter.adapt(r.Adaptations, r.Experience); learned != nil {
		r.ReasoningLog.Record(Record{
			Stage:  "adaptation",
			Inputs: map[string]any{"experience": r.Experience.Summary()},
			Output: learned.Insight,
		})
	}
	return decision, nil
}

// enforce turns the Governor's unresolved violations into a stop: a hard
// block when any non-gated policy is violated (or a human rejected the
// gate), a parked approval when only approval-gated policies remain.
func (r *Runtime) enforce(violations []*Policy, approval *ApprovalVerdict, scope string) error {
	rejected := approval != nil && approval.Verdict != nil && !*approval.Verdict
	var blocking, pending []*Policy
	for _, p := range violations {
		if !p.ApprovalRequired || rejected {
			blocking = append(blocking, p)
		} else {
			pending = append(pending, p)
		}
	}
	if len(blocking) > 0 {
		return &PolicyViolationError{Scope: scope, Policies: blocking}
	}
	names := make([]string, len(pending))
	for i, p := range pending {
		names[i] = p.Name
	}
	r.ReasoningLog.Record(Record{
		Stage:  "approval",
		Inputs: map[string]any{"policies": names},
		Output: "PENDING -- human approval required for: " + strings.Join(names, ", "),
	})
	return &ApprovalRequiredError{Policies: pending}
}

// buildEvidence captures why this decision was reached -- separately from
// what was decided (Memory) or any pattern drawn from repeating it.
func (r *Runtime) buildEvidence(intent Intent, plan []*Workflow, recalled string) *Evidence {
	evidence := NewEvidence("Resolved via the Reasoner's dependency-free default")
	policyNames := make([]string, len(r.Policies))
	for i, p := range r.Policies {
		policyNames[i] = p.Name
	}
	planNames := make([]string, len(plan))
	for i, w := range plan {
		planNames[i] = w.Name
	}
	evidence.Sources["policies_checked"] = policyNames
	evidence.Sources["context"] = intent.Context
	evidence.Sources["plan"] = planNames
	evidence.Sources["recalled_memory"] = recalled
	return evidence
}
