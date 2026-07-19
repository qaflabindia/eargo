package ear

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// maxToolRecoveries bounds how many times the loop corrects a bad turn (a
// nonexistent tool, or neither a call nor a decision) before concluding with
// the facts gathered so far.
const maxToolRecoveries = 3

// reasonWithTools is the native tool-use loop: ask the model to call one tool
// or decide, run the chosen tool through the governed InvokeTool (so the call
// is policy-checked and on the trail), feed the result back, and repeat until
// the model decides or the iteration budget is spent. No framework -- the
// model's choices are markdown, parsed by the shared codec.
//
// Recovery discipline (ported from the Python loop): a bad turn is corrected
// within a small budget rather than abandoned, and an unchanged failed call
// is refused before it runs a second time -- so a hallucinated or failing
// call becomes a self-corrected one instead of a lost turn. The pure
// prompt-size optimisations (result compression, context checkpointing,
// resolved-failure pruning) are omitted here; they need the auxiliary model.
func reasonWithTools(ctx context.Context, rt *Runtime, lm LM, intent, capabilities string, context map[string]any, tools []*BoundTool) (string, error) {
	catalogue := toolCatalogue(tools)
	available := toolNames(tools)
	maxIter := rt.ToolBinder.MaxIterations
	if maxIter <= 0 {
		maxIter = 6
	}

	var gathered []string
	failedCalls := map[string]bool{}
	recoveries := 0

	for i := 0; i < maxIter; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		gatheredText := "none yet"
		if len(gathered) > 0 {
			gatheredText = strings.Join(gathered, "\n")
		}
		out, err := ChooseToolAction.Run(ctx, lm, ChooseToolIn{
			Intent:       intent,
			Context:      context,
			Capabilities: capabilities,
			Tools:        catalogue,
			Gathered:     gatheredText,
		})
		if err != nil {
			return "", err
		}
		toolName := strings.TrimSpace(out.Tool)
		decision := strings.TrimSpace(out.Decision)

		chosen, ok := rt.ToolBinder.Get(toolName)
		if toolName == "" || !ok {
			if decision != "" {
				return decision, nil
			}
			if recoveries < maxToolRecoveries {
				recoveries++
				note := "no tool call and no decision were given"
				if toolName != "" {
					note = fmt.Sprintf("no tool named '%s' -- available tools: %s", toolName, available)
				}
				gathered = append(gathered, "(recovered: "+note+"; call a listed tool or give your final decision)")
				continue
			}
			break // recoveries spent -- conclude with the gathered facts
		}

		args := coerceArgs(out.Arguments)
		callKey := toolKey(chosen.Name) + "::" + argsKey(args)
		if failedCalls[callKey] {
			gathered = append(gathered, fmt.Sprintf(
				"(refused: the previous identical %s call already failed -- diagnose that error and change the input before retrying, or give your final decision)",
				chosen.Name))
			continue
		}

		result := rt.InvokeTool(ctx, chosen.Name, args) // governed + recorded
		gathered = append(gathered, fmt.Sprintf("%s(%s) -> %s", chosen.Name, renderArgs(args), result))
		if toolResultFailed(result) {
			failedCalls[callKey] = true
		} else {
			// A call succeeded -- earlier failures are recovered from.
			failedCalls = map[string]bool{}
		}
	}

	// Budget spent (or the model declined to decide): conclude with the
	// gathered facts in view.
	enriched := map[string]any{}
	for k, v := range context {
		enriched[k] = v
	}
	if len(gathered) > 0 {
		enriched["_tool_results"] = strings.Join(gathered, "\n")
	}
	out, err := ReasonAboutIntent.Run(ctx, lm, ReasonIn{Intent: intent, Context: enriched, Capabilities: capabilities})
	if err != nil {
		return "", err
	}
	return out.Decision, nil
}

func toolCatalogue(tools []*BoundTool) string {
	lines := make([]string, len(tools))
	for i, t := range tools {
		lines[i] = t.Describe()
	}
	return strings.Join(lines, "\n")
}

func toolNames(tools []*BoundTool) string {
	if len(tools) == 0 {
		return "none"
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}

// coerceArgs types the string arguments the model gave, so a numeric tool
// argument reaches a tool-scoped policy expression as a number.
func coerceArgs(raw map[string]string) map[string]any {
	args := make(map[string]any, len(raw))
	for k, v := range raw {
		args[k] = Coerce(v)
	}
	return args
}

// argsKey is a deterministic key for a set of arguments, so an identical
// repeated call is recognised.
func argsKey(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%v;", k, args[k])
	}
	return b.String()
}

func renderArgs(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%v", k, args[k])
	}
	return strings.Join(parts, ", ")
}

// toolResultFailed reports whether an InvokeTool result is a failure or a
// policy block (both are returned as text), so the loop feeds the error back
// and applies the failed-call guard.
func toolResultFailed(result string) bool {
	return strings.Contains(result, "failed:") ||
		strings.Contains(result, "blocked by policy") ||
		strings.HasPrefix(result, "no tool named")
}
