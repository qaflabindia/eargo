package main

import (
	"fmt"
	"os"
	"strings"

	ear "github.com/qaflabindia/ear"
)

// cmdInspect loads a stack and renders what was assembled: the org, every
// process with its workflows, steps, personas and governing policies, the
// runtime-wide and tool-scoped policies, the declared tools, and the
// operating strategy memory.md stacked -- so an author can see exactly how
// their markdown was read before reasoning anything through it.
func cmdInspect(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: ear inspect <stack-dir>")
		return exitError
	}
	runtime, err := loadStack(args[0], "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ear inspect:", err)
		return exitError
	}
	defer runtime.Close()
	fmt.Print(renderStack(runtime))
	return exitDecided
}

func renderStack(runtime *ear.Runtime) string {
	var b strings.Builder
	write := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	write("# %s\n\n", runtime.Name)
	if runtime.Tenant.OrgID != "" {
		write("Org: %s", runtime.Tenant.OrgID)
		if runtime.Tenant.Name != "" {
			write(" (%s)", runtime.Tenant.Name)
		}
		write("\n\n")
	}

	write("## Processes (%d)\n\n", len(runtime.Processes))
	for _, process := range runtime.Processes {
		write("- **%s** -- %s\n", process.Name, firstLine(process.Description))
		for _, workflow := range process.Workflows {
			write("  - workflow **%s** (%d steps)\n", workflow.Name, len(workflow.Steps))
			for i, step := range workflow.Steps {
				who := "undelegated"
				if step.Persona != nil {
					who = step.Persona.Name
				}
				write("    %d. %s  [%s]\n", i+1, firstLine(step.Instruction), who)
			}
			for _, policy := range workflow.Policies {
				write("    - policy: %s%s\n", policy.Name, policyMarks(policy))
			}
			if workflow.Contract != nil {
				names := make([]string, len(workflow.Contract.Fields))
				for i, f := range workflow.Contract.Fields {
					names[i] = f.Name
				}
				write("    - deliverable: %s (%s)\n", workflow.Contract.Name, strings.Join(names, ", "))
			}
			if workflow.Pattern != "" {
				write("    - pattern: %s\n", workflow.Pattern)
			}
		}
	}

	if len(runtime.Policies) > 0 {
		write("\n## Runtime policies (%d)\n\n", len(runtime.Policies))
		for _, policy := range runtime.Policies {
			write("- **%s**%s -- %s\n", policy.Name, policyMarks(policy), firstLine(policy.Statement))
		}
	}
	if len(runtime.ToolPolicies) > 0 {
		write("\n## Tool policies (%d)\n\n", len(runtime.ToolPolicies))
		for _, policy := range runtime.ToolPolicies {
			write("- **%s**%s -- %s\n", policy.Name, policyMarks(policy), firstLine(policy.Statement))
		}
	}
	if len(runtime.Tools) > 0 {
		write("\n## Tools (%d)\n\n", len(runtime.Tools))
		for _, tool := range runtime.Tools {
			write("- %s\n", tool.Describe())
		}
	}

	write("\n## Strategy\n\n")
	s := runtime.Strategy
	if s == nil {
		write("- none declared (no memory.md)\n")
		return b.String()
	}
	if s.Model != "" {
		bound := "credential absent -- deterministic fallback"
		if runtime.LM != nil {
			bound = "bound"
		}
		write("- model: %s (key from %s; %s)\n", s.Model, orUnset(s.APIKeyEnvVar), bound)
	} else {
		write("- model: none declared -- deterministic reasoning\n")
	}
	if s.AuxModel != "" {
		bound := "credential absent"
		if runtime.AuxLM != nil {
			bound = "bound"
		}
		write("- auxiliary model: %s (key from %s; %s)\n", s.AuxModel, orUnset(s.AuxAPIKeyEnvVar), bound)
	}
	write("- context history: %d recent cycles kept verbatim\n", runtime.Memory.Capacity)
	if s.AuditEnabled && s.AuditPath != "" {
		write("- reasoning trail: %s (persisted, hash-chained)\n", s.AuditPath)
	} else {
		write("- reasoning trail: in memory only\n")
	}
	if s.RetentionDays > 0 {
		write("- trail retention: %.3g days (in-memory window)\n", s.RetentionDays)
	}
	if s.CrossSessionPath != "" {
		write("- cross-session data: %s\n", s.CrossSessionPath)
	}
	if s.Budget > 0 {
		write("- budget: $%.2f", s.Budget)
		if len(s.AlertThresholds) > 0 {
			marks := make([]string, len(s.AlertThresholds))
			for i, t := range s.AlertThresholds {
				marks[i] = fmt.Sprintf("%.0f%%", t*100)
			}
			write(", alerts at %s", strings.Join(marks, ", "))
		}
		write("\n")
	}
	if s.InputRatePerMillion != nil || s.OutputRatePerMillion != nil {
		write("- pricing: declared (dollar costing on)\n")
	}
	if runtime.Spawner != nil {
		if runtime.Spawner.Enabled {
			limit := "unbounded"
			if runtime.Spawner.Limit > 0 {
				limit = fmt.Sprintf("up to %d", runtime.Spawner.Limit)
			}
			write("- subagents: %s\n", limit)
		} else {
			write("- subagents: disabled\n")
		}
	}
	if len(s.KnowledgeSources) > 0 {
		passages := 0
		if runtime.Librarian != nil && runtime.Librarian.Knowledge != nil {
			passages = runtime.Librarian.Knowledge.Len()
		}
		write("- knowledge: %d source(s), %d passages indexed\n", len(s.KnowledgeSources), passages)
	}
	if len(s.Ontology.Order) > 0 {
		write("- ontology: %d terms\n", len(s.Ontology.Order))
	}
	return b.String()
}

func policyMarks(policy *ear.Policy) string {
	var marks []string
	if policy.FallbackExpression != "" {
		marks = append(marks, "fallback")
	}
	if policy.ApprovalRequired {
		marks = append(marks, "approval-gated")
	}
	if policy.Escalation != "" {
		marks = append(marks, "escalates")
	}
	if len(marks) == 0 {
		return ""
	}
	return " [" + strings.Join(marks, ", ") + "]"
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return clipText(line, 100)
}

func clipText(text string, width int) string {
	if len(text) <= width {
		return text
	}
	return text[:width-3] + "..."
}

func orUnset(s string) string {
	if s == "" {
		return "(no env var named)"
	}
	return s
}
