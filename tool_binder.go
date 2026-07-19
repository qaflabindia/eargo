package ear

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// BoundTool is one executable tool for a cycle: the declared name and
// description the model reads, the parameter names it is told the tool
// takes, and the handler that runs. The handler returns a string result (or
// an error, which is fed back to the model as text rather than crashing the
// cycle).
type BoundTool struct {
	Name        string
	Description string
	Parameters  []string
	Handler     func(args map[string]any) (string, error)
}

// Describe renders the tool for the model's catalogue: name(params): desc.
func (t *BoundTool) Describe() string {
	return fmt.Sprintf("%s(%s): %s", t.Name, strings.Join(t.Parameters, ", "), t.Description)
}

// ToolBinder holds the tools bound for a runtime, keyed by normalized name.
// MaxIterations bounds the reasoner tool-use loop (Slice 2).
type ToolBinder struct {
	MaxIterations int
	bound         map[string]*BoundTool
	order         []string
}

// NewToolBinder builds an empty binder with the default iteration budget.
func NewToolBinder() *ToolBinder {
	return &ToolBinder{MaxIterations: 6, bound: map[string]*BoundTool{}}
}

func toolKey(name string) string { return Normalize(name) }

// add registers a bound tool.
func (b *ToolBinder) add(t *BoundTool) {
	key := toolKey(t.Name)
	if _, exists := b.bound[key]; !exists {
		b.order = append(b.order, key)
	}
	b.bound[key] = t
}

// Get returns the bound tool for a name (case/punctuation-insensitive).
func (b *ToolBinder) Get(name string) (*BoundTool, bool) {
	t, ok := b.bound[toolKey(name)]
	return t, ok
}

// Tools returns the bound tools in bind order.
func (b *ToolBinder) Tools() []*BoundTool {
	out := make([]*BoundTool, 0, len(b.order))
	for _, key := range b.order {
		out = append(out, b.bound[key])
	}
	return out
}

// BindTool attaches an executable to a tool the stack declares (a `## Tools`
// bullet in memory.md). Binding an undeclared name fails loudly -- code never
// grows the runtime a capability the natural-language authoring doesn't show.
// params names the arguments surfaced to the model (Go handlers take a
// map[string]any, so parameter names are supplied explicitly).
func (r *Runtime) BindTool(name string, handler func(args map[string]any) (string, error), params ...string) error {
	var declared *Tool
	for i := range r.Tools {
		if toolKey(r.Tools[i].Name) == toolKey(name) {
			declared = &r.Tools[i]
			break
		}
	}
	if declared == nil {
		return fmt.Errorf("cannot bind undeclared tool %q -- declare it in memory.md's ## Tools first", name)
	}
	r.ToolBinder.add(&BoundTool{
		Name:        declared.Name,
		Description: declared.Description,
		Parameters:  params,
		Handler:     handler,
	})
	return nil
}

// InvokeTool runs a bound tool under tool-scoped policy governance, recording
// the call on the trail. It never crashes the cycle: an unknown tool, a
// policy block or a handler error all return to the caller (and the model) as
// text. Tool-scoped policies are judged against this call's name and
// arguments, so a policy ("the wire-transfer tool must not send over
// $10,000", or a fallback `amount <= 10000`) blocks *this* call, not the
// cycle.
func (r *Runtime) InvokeTool(ctx context.Context, name string, args map[string]any) string {
	tool, ok := r.ToolBinder.Get(name)
	if !ok {
		return fmt.Sprintf("no tool named %q is bound", name)
	}

	callContext := map[string]any{"tool": tool.Name}
	for k, v := range args {
		callContext[k] = v
	}
	for _, policy := range r.ToolPolicies {
		complies, rationale, err := r.PolicyJudge.Judge(ctx, policy, callContext)
		if err != nil {
			// Fail closed: a judge that cannot rule blocks the call.
			complies, rationale = false, "policy could not be judged: "+err.Error()
		}
		if !complies {
			refusal := fmt.Sprintf("Tool '%s' blocked by policy '%s': %s", tool.Name, policy.Name, rationale)
			r.ReasoningLog.Record(Record{
				Stage:     "tool",
				Inputs:    map[string]any{"tool": tool.Name, "arguments": args, "policy": policy.Name},
				Output:    fmt.Sprintf("BLOCKED by policy '%s'", policy.Name),
				Rationale: rationale,
			})
			return refusal
		}
	}

	started := time.Now()
	result, err := tool.Handler(args)
	output := result
	failed := false
	if err != nil {
		output = fmt.Sprintf("Tool '%s' failed: %v", tool.Name, err)
		failed = true
	}
	recordOutput := output
	if failed {
		recordOutput = "FAILED -- " + output
	}
	r.ReasoningLog.Record(Record{
		Stage:  "tool",
		Inputs: map[string]any{"tool": tool.Name, "arguments": args, "duration_ms": time.Since(started).Milliseconds()},
		Output: recordOutput,
	})
	return output
}
