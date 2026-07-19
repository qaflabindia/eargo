package ear

import (
	"fmt"
	"strings"
)

// Reasoner deliberates over an intent given the scheduled plan. In the
// Python package it either calls the bound LLM over the stacked capabilities
// block or, with no model, produces a deterministic rendering that names the
// runtime, the processes, the capabilities applied and the memory drawn on.
// This port implements that deterministic path, and renders the same stacked
// capabilities block so the author's stacking is what shapes the output.
type Reasoner struct{}

func (Reasoner) reason(r *Runtime, intent Intent, plan []*Workflow) any {
	capabilities := renderCapabilities(plan)
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
	return decision
}

// defaultReasoning is the dependency-free decision: it names the runtime,
// the processes it resolved across, the capabilities it applied and the
// memory it drew on -- exactly the Python package's offline fallback.
func defaultReasoning(r *Runtime, intent Intent, capabilities string) string {
	processes := "none"
	if names := processNames(r.Processes); len(names) > 0 {
		processes = strings.Join(names, ", ")
	}
	memoryNote := ""
	if n := r.Memory.Len(); n > 0 {
		memoryNote = fmt.Sprintf(", drawing on %d remembered cycles", n)
	}
	capabilityNote := ""
	if capabilities != "" {
		var names []string
		for _, line := range strings.Split(capabilities, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			head, _, _ := strings.Cut(line, ":")
			head = strings.Trim(head, " -")
			if head != "" {
				names = append(names, head)
			}
		}
		if len(names) > 0 {
			capabilityNote = ", applying capabilities: " + strings.Join(names, ", ")
		}
	}
	return fmt.Sprintf("[%s] resolved intent '%s' across processes: %s%s%s",
		r.Name, intent.Text, processes, capabilityNote, memoryNote)
}

// renderCapabilities flattens the scheduled plan (Workflows -> ordered Steps
// delegated to Personas -> stacked Skill prompts) into a natural-language
// block, in order. This is what makes the author's stacking matter.
func renderCapabilities(plan []*Workflow) string {
	if len(plan) == 0 {
		return ""
	}
	var lines []string
	for _, w := range plan {
		if w.Name != "" {
			lines = append(lines, "Workflow "+w.Name+":")
		}
		for number, step := range w.Steps {
			delegate := ""
			if step.Persona != nil {
				delegate = " [delegated to Persona " + step.Persona.Name + "]"
			}
			lines = append(lines, fmt.Sprintf("  Step %d: %s%s", number+1, step.Instruction, delegate))
			renderPersona(step.Persona, &lines, "      ", false)
		}
		for _, p := range w.Personas {
			renderPersona(p, &lines, "  ", true)
		}
	}
	return strings.Join(lines, "\n")
}

func renderPersona(persona *Persona, lines *[]string, indent string, header bool) {
	if persona == nil {
		return
	}
	if header {
		line := indent + "Persona " + persona.Name
		if persona.Instructions != "" {
			line += ": " + persona.Instructions
		}
		*lines = append(*lines, line)
	} else if persona.Instructions != "" {
		*lines = append(*lines, indent+"Persona "+persona.Name+": "+persona.Instructions)
	}
	for _, skill := range persona.Skills {
		*lines = append(*lines, indent+"  - Skill "+skill.Name+": "+skill.Instruction())
	}
}
