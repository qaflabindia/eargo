package ear

import (
	"context"
	"fmt"
)

// EAR's design calls its judgment-laden stages "seams": swap in a live-LLM
// implementation and it replaces the deterministic default for that step
// only, with the governance, recording and enforcement around it unchanged.
// In Go that seam is an interface. Rather than 14 empty stage structs (a
// Python-ism), this port keeps the mechanical stages as Runtime methods and
// elevates the two genuine extension points -- how a policy is judged, and
// how the runtime deliberates -- to interfaces with deterministic defaults.

// PolicyJudge decides whether a policy complies with a cycle's context and
// returns a rationale for the audit trail. The default is EAR's safe
// expression evaluator; a provider-backed judge (an LLM reading the
// statement in natural language) implements the same interface and slots in
// with no change to the Governor. Judge takes a context.Context so a
// provider call can be cancelled or deadline-bound.
type PolicyJudge interface {
	Judge(ctx context.Context, policy *Policy, context map[string]any) (complies bool, rationale string, err error)
}

// DeterministicJudge is the default PolicyJudge: EAR's fallback-expression
// evaluator. It never performs I/O and never errors, so ctx is unused.
type DeterministicJudge struct{}

// Judge evaluates the policy's fallback expression against the context.
func (DeterministicJudge) Judge(_ context.Context, policy *Policy, context map[string]any) (bool, string, error) {
	complies, rationale := policy.Judge(context)
	return complies, rationale, nil
}

// Reasoner deliberates over an intent given the scheduled plan and returns a
// decision. This is EAR's central seam: the default is the dependency-free
// deterministic reasoner, and a provider-backed reasoner (or a typed-agent
// backend) implements the same interface. Reason takes a context.Context so
// deliberation -- the most I/O-heavy stage in the live runtime -- is
// cancellable.
type Reasoner interface {
	Reason(ctx context.Context, r *Runtime, intent Intent, plan []*Workflow, research *Research) (any, error)
}

// DefaultReasoner is the dependency-free deliberation engine: it renders the
// stacked capabilities block and produces the deterministic decision that
// names the runtime, the processes resolved across, the capabilities
// applied and the memory drawn on -- exactly EAR's offline fallback.
type DefaultReasoner struct{}

// Reason produces the deterministic decision. It honours ctx cancellation
// before doing any work so a cancelled cycle stops promptly.
func (DefaultReasoner) Reason(ctx context.Context, r *Runtime, intent Intent, plan []*Workflow, _ *Research) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	capabilities := renderCapabilities(plan, nil)
	decision := defaultReasoning(r, intent, capabilities)
	if r.ReasoningLog != nil {
		r.ReasoningLog.Record(Record{
			Stage: "deliberation",
			Inputs: map[string]any{
				"intent":       intent.Text,
				"context":      intent.Context,
				"capabilities": capabilities,
				"memory":       r.Memory.ContextWindow(),
			},
			Output: fmt.Sprint(decision),
		})
	}
	return decision, nil
}
