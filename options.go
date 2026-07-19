package ear

// Option configures a Runtime at construction. The functional-options
// pattern keeps NewRuntime's signature stable as configuration grows and
// lets a caller override only what they need -- notably the two seams -- with
// deterministic defaults for everything else.
type Option func(*Runtime)

// WithReasoner overrides the deliberation engine (default: DefaultReasoner).
// Pass a provider-backed Reasoner to reason against a live model.
func WithReasoner(reasoner Reasoner) Option {
	return func(r *Runtime) { r.Reasoner = reasoner }
}

// WithPolicyJudge overrides how policies are judged (default:
// DeterministicJudge, the safe-expression evaluator). Pass an LLM-backed
// judge to have policy statements judged in natural language.
func WithPolicyJudge(judge PolicyJudge) Option {
	return func(r *Runtime) { r.PolicyJudge = judge }
}

// WithTenant sets the org this runtime instance belongs to.
func WithTenant(tenant Tenant) Option {
	return func(r *Runtime) { r.Tenant = tenant }
}

// WithMemoryCapacity sets how many recent cycles Memory keeps verbatim
// before compressing older ones.
func WithMemoryCapacity(capacity int) Option {
	return func(r *Runtime) { r.Memory.Capacity = capacity }
}

// WithAdaptEvery sets the cadence (in observed cycles) at which a new
// Adaptation is distilled. Zero disables adaptation.
func WithAdaptEvery(every int) Option {
	return func(r *Runtime) { r.AdaptEvery = every }
}

// WithBudget attaches a non-blocking dollar-budget monitor programmatically.
// The authored path is a `## Budget` section in memory.md ("The budget is
// $50. Alert at 25%, 50% and 90%."), read by LoadRuntime -- prefer that so
// the cap and thresholds live in natural language, not code. This option is
// the escape hatch for a hand-built runtime, and for supplying an onAlert
// callback on top of an authored budget.
//
// As cumulative spend crosses each threshold fraction (0.25 for 25%, ...),
// onAlert fires once, progressively, and the crossing is recorded on the
// trail. It never stops the runtime. Requires a declared `## Pricing` for
// spend to be costed.
func WithBudget(budget float64, onAlert func(BudgetAlert), thresholds ...float64) Option {
	return func(r *Runtime) {
		m := NewBudgetMonitor(budget, onAlert, thresholds...)
		m.Log = r.ReasoningLog
		r.Budget = m
	}
}

// WithSessionStore attaches a cross-session store and restores from it at
// once, so a hand-built runtime resumes warm before its first cycle. The
// authored path is a `## Cross-Session Data` section in memory.md ("Persist
// the session to `state/session.md`."), read by LoadRuntime -- prefer that so
// the location lives in natural language, not code. A missing or corrupt
// store restores nothing and never blocks; every later cycle saves back.
func WithSessionStore(path string) Option {
	return func(r *Runtime) {
		store := &SessionStore{Path: path}
		store.Restore(r)
		r.SessionStore = store
	}
}
