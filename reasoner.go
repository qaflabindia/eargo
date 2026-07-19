package ear

import (
	"fmt"
	"strings"
)

// This file holds the deterministic deliberation helpers used by
// DefaultReasoner (see stage.go). Keeping them free functions -- rather than
// methods on an empty struct -- lets a provider-backed Reasoner reuse the
// exact same stacked-capabilities rendering the deterministic path uses, so
// what the author stacked is what any reasoner sees.

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
// filter, when non-nil, returns the subset of a persona's skills to stack
// (progressive skill selection); nil stacks all of them, the deterministic
// default.
func renderCapabilities(plan []*Workflow, filter func(*Persona) []*Skill) string {
	if len(plan) == 0 {
		return ""
	}
	var b strings.Builder
	first := true
	writeLine := func(s string) {
		if !first {
			b.WriteByte('\n')
		}
		b.WriteString(s)
		first = false
	}
	for _, w := range plan {
		if w.Name != "" {
			writeLine("Workflow " + w.Name + ":")
		}
		for number, step := range w.Steps {
			delegate := ""
			if step.Persona != nil {
				delegate = " [delegated to Persona " + step.Persona.Name + "]"
			}
			writeLine(fmt.Sprintf("  Step %d: %s%s", number+1, step.Instruction, delegate))
			renderPersona(step.Persona, writeLine, "      ", false, filter)
		}
		for _, p := range w.Personas {
			renderPersona(p, writeLine, "  ", true, filter)
		}
	}
	return b.String()
}

func renderPersona(persona *Persona, writeLine func(string), indent string, header bool, filter func(*Persona) []*Skill) {
	if persona == nil {
		return
	}
	if header {
		line := indent + "Persona " + persona.Name
		if persona.Instructions != "" {
			line += ": " + persona.Instructions
		}
		writeLine(line)
	} else if persona.Instructions != "" {
		writeLine(indent + "Persona " + persona.Name + ": " + persona.Instructions)
	}
	skills := persona.Skills
	if filter != nil {
		skills = filter(persona)
	}
	for _, skill := range skills {
		writeLine(indent + "  - Skill " + skill.Name + ": " + skill.Instruction())
	}
}
