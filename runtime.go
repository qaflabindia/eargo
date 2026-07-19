package ear

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PolicyViolationError is returned when a cycle is hard-blocked by one or
// more non-gated policy violations (or a human rejected an approval gate).
type PolicyViolationError struct {
	Scope    string
	Policies []*Policy
}

func (e *PolicyViolationError) Error() string {
	return fmt.Sprintf("%s violated: %s", e.Scope, strings.Join(policyNames(e.Policies), ", "))
}

// ApprovalRequiredError parks a cycle for a human verdict: only
// approval-gated policies remain violated and none has been rejected.
type ApprovalRequiredError struct {
	Policies []*Policy
}

func (e *ApprovalRequiredError) Error() string {
	return "human approval required for: " + strings.Join(policyNames(e.Policies), ", ")
}

func policyNames(policies []*Policy) []string {
	names := make([]string, len(policies))
	for i, p := range policies {
		names[i] = p.Name
	}
	return names
}

// Runtime is the battlefield: every cycle runs through the full govern ->
// discover -> select -> compose -> schedule -> delegate -> reason pipeline,
// and is recorded across the Evidence (why) / Memory (what) / Experience
// (pattern) / Adaptation (adaptation) layers.
//
// The two extension seams -- PolicyJudge and Reasoner -- are interfaces, so a
// provider-backed implementation slots in without touching the pipeline. The
// mechanical steps are methods. Construct with NewRuntime and configure with
// Options.
type Runtime struct {
	Name        string
	Tenant      Tenant
	Processes   []*Process
	Policies    []*Policy
	Memory      *Memory
	Experience  *Experience
	Adaptations *AdaptationBank

	ReasoningLog *ReasoningLog

	// Seams: swap either for a provider-backed implementation.
	Reasoner    Reasoner
	PolicyJudge PolicyJudge

	// LM is the bound model, set by WithLM. It is read only for per-cycle
	// usage accounting (the cycle's calls, tokens and latency); the seams
	// above are what actually reason. Nil leaves usage on the deterministic
	// path.
	LM LM

	// AdaptEvery throttles adaptation distillation to every Nth observed
	// cycle. Zero disables it.
	AdaptEvery int

	// Strategy is the operating strategy parsed from memory.md (context
	// history, audit retention, declared tools, ontology, subagent limits,
	// skills-discovery guidance). Nil for a hand-built runtime never loaded
	// from a directory. Tools are the strategy's declared tools, surfaced
	// for convenience.
	Strategy *Strategy
	Tools    []Tool

	// Pipeline is the ordered list of stages Reason runs. It defaults to
	// defaultPipeline(); replace or reorder it to customize the cycle.
	Pipeline []Stage

	// Budget, when set (via WithBudget), tracks cumulative dollar spend and
	// fires progressive threshold alerts. It never blocks the cycle.
	Budget *BudgetMonitor

	// ToolBinder holds the tools bound to executables (BindTool); ToolPolicies
	// are the tool-scoped policies (memory.md `Applies to: tools`) judged
	// against each tool call rather than the cycle.
	ToolBinder   *ToolBinder
	ToolPolicies []*Policy

	// Librarian retrieves relevant Knowledge (RAG) when a `## Knowledge`
	// corpus is declared in memory.md. Nil leaves the research stage a no-op.
	Librarian *Librarian
}

// NewRuntime builds a Runtime with deterministic defaults for both seams and
// all memory layers initialized, then applies any Options.
func NewRuntime(name string, opts ...Option) *Runtime {
	r := &Runtime{
		Name:         name,
		Tenant:       NewTenant(),
		Memory:       NewMemory(),
		Experience:   NewExperience(),
		Adaptations:  NewAdaptationBank(),
		ReasoningLog: &ReasoningLog{},
		Reasoner:     DefaultReasoner{},
		PolicyJudge:  DeterministicJudge{},
		AdaptEvery:   5,
		ToolBinder:   NewToolBinder(),
	}
	r.Pipeline = defaultPipeline()
	for _, opt := range opts {
		opt(r)
	}
	return r
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

// EnforcePolicies returns the runtime-wide policies violated by the context,
// judged with the runtime's PolicyJudge.
func (r *Runtime) EnforcePolicies(ctx context.Context, context map[string]any) ([]*Policy, error) {
	var out []*Policy
	for _, p := range r.Policies {
		complies, _, err := r.PolicyJudge.Judge(ctx, p, context)
		if err != nil {
			return nil, err
		}
		if !complies {
			out = append(out, p)
		}
	}
	return out, nil
}

// Reason runs one reasoning cycle end to end through the named pipeline and
// returns the decision. It honours ctx: a cancelled or deadline-exceeded
// context aborts the cycle at the next checkpoint and returns ctx.Err(). A
// hard policy block returns *PolicyViolationError; a parked approval gate
// returns *ApprovalRequiredError.
func (r *Runtime) Reason(ctx context.Context, intent Intent, approval *ApprovalVerdict) (decision any, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	started := time.Now()
	callsBefore := r.modelCallsSoFar()
	r.ReasoningLog.BeginCycle(intent)
	// Close the cycle's accounting on every exit -- completed, blocked or
	// parked, a refusal costs whatever it cost -- then apply any declared
	// retention window. Both run after the return value is settled.
	defer func() {
		r.recordUsage(started, callsBefore)
		r.applyRetention()
	}()

	// Run the composable pipeline over one shared Cycle. Each stage checks
	// ctx first, so a cancelled context aborts at the next stage boundary.
	c := &Cycle{Ctx: ctx, Runtime: r, Intent: intent, Approval: approval}
	for _, stage := range r.Pipeline {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := stage.Run(c); err != nil {
			return nil, err
		}
	}
	return c.Decision, nil
}

// formalize honours each workflow Contract in the plan. This port binds no
// model, so there is nothing honest to extract: it records the skip per
// contract (the deterministic path the Python package takes with no model
// bound) rather than fabricating deliverable values. The structural
// conformance check (Contract.Judge) is available for a caller that fills
// the fields itself.
func (r *Runtime) formalize(plan []*Workflow) {
	for _, w := range plan {
		if w.Contract == nil {
			continue
		}
		r.ReasoningLog.Record(Record{
			Stage:  "contract",
			Inputs: map[string]any{"contract": w.Contract.Name, "fields": w.Contract.RenderFields()},
			Output: "skipped -- no model bound to extract the deliverable",
		})
	}
}

// modelCallsSoFar is how many calls the bound LM's history holds before this
// cycle, so the cycle's usage is the delta. Zero when no LM is bound or the
// LM does not expose its history.
func (r *Runtime) modelCallsSoFar() int {
	if h, ok := r.LM.(CallHistory); ok {
		return len(h.Calls())
	}
	return 0
}

// cycleCalls is the slice of LM calls made during this cycle -- the history
// delta since callsBefore.
func (r *Runtime) cycleCalls(callsBefore int) []Call {
	h, ok := r.LM.(CallHistory)
	if !ok {
		return nil
	}
	calls := h.Calls()
	if callsBefore > len(calls) {
		return nil
	}
	return calls[callsBefore:]
}

// recordUsage closes the cycle's accounting: wall-clock latency always, and,
// when an LM is bound, the model calls, tokens and retries this cycle
// consumed, read from the LM's own call history. Written on blocked cycles
// too -- a refusal costs whatever it cost.
func (r *Runtime) recordUsage(started time.Time, callsBefore int) {
	latencyMs := time.Since(started).Milliseconds()
	calls := r.cycleCalls(callsBefore)
	var in, out, cacheRead, cacheWrite, retries int
	for _, c := range calls {
		in += c.Usage.PromptTokens
		out += c.Usage.CompletionTokens
		cacheRead += c.Usage.CacheReadTokens
		cacheWrite += c.Usage.CacheWriteTokens
		retries += c.Retries
	}
	// Dollar cost only when the stack declared Pricing in memory.md -- a
	// figure nobody declared is never invented.
	cost, priced := 0.0, false
	if r.Strategy != nil {
		cost, priced = r.Strategy.Dollars(in, out, cacheRead, cacheWrite)
	}

	var output string
	switch {
	case len(calls) > 0:
		output = fmt.Sprintf("%d model calls, %d+%d tokens", len(calls), in, out)
		if cacheRead > 0 || cacheWrite > 0 {
			output += fmt.Sprintf(" (%d cache read, %d cache write)", cacheRead, cacheWrite)
		}
		if priced {
			output += fmt.Sprintf(", ~$%.6f", cost)
		}
		if retries > 0 {
			output += fmt.Sprintf(", %d retried", retries)
		}
		output += fmt.Sprintf(", %d ms", latencyMs)
	case r.LM != nil:
		// A bound model with no new history means the calls were served from
		// cache; the accounting says so rather than implying no model ran.
		output = fmt.Sprintf("0 new model calls recorded (cached), %d ms", latencyMs)
	default:
		output = fmt.Sprintf("0 model calls (deterministic fallbacks), %d ms", latencyMs)
	}
	inputs := map[string]any{
		"model_calls": len(calls), "input_tokens": in, "output_tokens": out,
		"cache_read_tokens": cacheRead, "cache_write_tokens": cacheWrite,
		"retries": retries, "latency_ms": latencyMs,
	}
	if priced {
		inputs["cost"] = cost
	}
	r.ReasoningLog.Record(Record{Stage: "usage", Inputs: inputs, Output: output})

	// Feed the budget monitor after the usage record, so any threshold alert
	// reads on the trail right after the spend that triggered it. Non-blocking.
	if r.Budget != nil && priced && cost > 0 {
		r.Budget.Add(cost)
	}
}

// applyRetention rotates the reasoning trail down to the declared retention
// window (memory.md's "keep N days"), if the strategy set one. A no-op
// otherwise.
func (r *Runtime) applyRetention() {
	if r.Strategy != nil && r.Strategy.RetentionDays > 0 {
		r.ReasoningLog.Rotate(r.Strategy.RetentionDays, time.Time{})
	}
}

// adapt distills a new Adaptation every AdaptEvery observed cycles.
func (r *Runtime) adapt() *Adaptation {
	if r.AdaptEvery <= 0 || r.Experience.Observations == 0 {
		return nil
	}
	if r.Experience.Observations%r.AdaptEvery != 0 {
		return nil
	}
	return r.Adaptations.LearnFrom(r.Experience)
}

// enforce turns unresolved violations into a stop: a hard block when any
// non-gated policy is violated (or a human rejected the gate), a parked
// approval when only approval-gated policies remain, nil when clear.
func (r *Runtime) enforce(violations []*Policy, approval *ApprovalVerdict, scope string) error {
	if len(violations) == 0 {
		return nil
	}
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
	r.ReasoningLog.Record(Record{
		Stage:  "approval",
		Inputs: map[string]any{"policies": policyNames(pending)},
		Output: "PENDING -- human approval required for: " + strings.Join(policyNames(pending), ", "),
	})
	return &ApprovalRequiredError{Policies: pending}
}

// buildEvidence captures why this decision was reached -- separately from
// what was decided (Memory) or any pattern drawn from repeating it.
func (r *Runtime) buildEvidence(intent Intent, plan []*Workflow, recalled string) *Evidence {
	evidence := NewEvidence("Resolved via the Reasoner's dependency-free default")
	planNames := make([]string, len(plan))
	for i, w := range plan {
		planNames[i] = w.Name
	}
	evidence.Sources["policies_checked"] = policyNames(r.Policies)
	evidence.Sources["context"] = intent.Context
	evidence.Sources["plan"] = planNames
	evidence.Sources["recalled_memory"] = recalled
	return evidence
}
