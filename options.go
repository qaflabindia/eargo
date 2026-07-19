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
