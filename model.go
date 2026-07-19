package ear

import (
	"fmt"
	"strings"
)

// This file holds EAR's core data model: the stack an author builds, from
// the Intent that starts a cycle down through Skill, Persona, Step,
// Workflow, Process, Tool and Contract. Every type is a plain value the
// runtime reasons over; none requires the author to write code.

// Intent is the prompt: a resolved request that starts a reasoning cycle.
type Intent struct {
	Text    string
	Context map[string]any
}

// NewIntent builds an Intent with an initialized context map.
func NewIntent(text string, context map[string]any) Intent {
	if context == nil {
		context = map[string]any{}
	}
	return Intent{Text: text, Context: context}
}

func (i Intent) String() string { return i.Text }

// IntentFromMarkdown reads an Intent from an intent document: the title and
// prose become the request text; a "## Context" section's bullets become
// the context facts; any other section's prose elaborates the request.
func IntentFromMarkdown(markdown string) Intent {
	doc := ParseDocument(markdown)
	var parts []string
	if doc.Title != "" {
		parts = append(parts, doc.Title)
	}
	if doc.Preamble != "" {
		parts = append(parts, doc.Preamble)
	}
	context := map[string]any{}
	for _, section := range doc.Sections {
		name := Normalize(section.Name)
		body := section.StructuredBody()
		if strings.Contains(name, "context") {
			for _, bullet := range body.Bullets {
				key, value, ok := strings.Cut(bullet, ": ")
				if !ok {
					key, value, ok = strings.Cut(bullet, ":")
				}
				if ok {
					context[strings.TrimSpace(key)] = Coerce(value)
				}
			}
		} else if body.Prose != "" {
			parts = append(parts, body.Prose)
		}
	}
	return Intent{Text: strings.TrimSpace(strings.Join(parts, "\n\n")), Context: context}
}

// ToMarkdown renders this Intent as an intent document.
func (i Intent) ToMarkdown() string {
	first, rest, _ := strings.Cut(i.Text, "\n")
	lines := []string{"# " + strings.TrimSpace(first)}
	if strings.TrimSpace(rest) != "" {
		lines = append(lines, "", strings.TrimSpace(rest))
	}
	if len(i.Context) > 0 {
		lines = append(lines, "", "## Context", "")
		for key, value := range i.Context {
			lines = append(lines, fmt.Sprintf("- %s: %v", key, value))
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// Skill is a single addressable capability a persona can invoke: a stacked
// prompt, not a code slot. The prompt is the natural-language instruction
// the runtime reasons over. Handler stays optional for the advanced,
// deterministic case.
type Skill struct {
	Name        string
	Prompt      string
	Description string
	Handler     func(args map[string]any) (any, error)
	Version     string
	Author      string
}

// Instruction is the prompt the runtime stacks into reasoning. It falls
// back to the description, then the name, so a skill always contributes
// some natural-language signal even if only loosely specified.
func (s Skill) Instruction() string {
	if s.Prompt != "" {
		return s.Prompt
	}
	if s.Description != "" {
		return s.Description
	}
	return s.Name
}

// ToMarkdown renders this skill the way skills.md stacks one.
func (s Skill) ToMarkdown() string {
	lines := []string{"## " + s.Name, ""}
	if s.Description != "" {
		lines = append(lines, "Description: "+s.Description, "")
	}
	if s.Prompt != "" {
		lines = append(lines, s.Prompt)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

// Persona is a stack of Skills plus standing instructions.
type Persona struct {
	Name         string
	Instructions string
	Skills       []*Skill
}

// AddSkill stacks a skill onto the persona and returns the persona for
// chaining.
func (p *Persona) AddSkill(s *Skill) *Persona {
	p.Skills = append(p.Skills, s)
	return p
}

// GetSkill returns the stacked skill with the given name, or nil.
func (p *Persona) GetSkill(name string) *Skill {
	for _, s := range p.Skills {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// ToMarkdown renders this persona the way persona.md stacks one.
func (p *Persona) ToMarkdown() string {
	lines := []string{"## " + p.Name, ""}
	if len(p.Skills) > 0 {
		names := make([]string, len(p.Skills))
		for i, s := range p.Skills {
			names[i] = s.Name
		}
		lines = append(lines, "Skills: "+strings.Join(names, ", "), "")
	}
	if p.Instructions != "" {
		lines = append(lines, p.Instructions)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

// Step is one ordered step in a Workflow, delegated to a Persona that the
// runtime reasons through. A Step with no persona is still valid.
type Step struct {
	Instruction string
	Persona     *Persona
	Name        string
}

// DelegateTo assigns a persona to the step and returns the step.
func (s *Step) DelegateTo(p *Persona) *Step {
	s.Persona = p
	return s
}

// Workflow is an ordered list of Steps delegated to Personas, plus the
// Policies that govern it and, optionally, the Contract its decision must
// deliver.
type Workflow struct {
	Name        string
	Personas    []*Persona
	Steps       []*Step
	Policies    []*Policy
	Contract    *Contract
	Pattern     string // a deliberation pattern in plain English
	Routes      string // routing prose judged after each leg
	RetryBudget *int   // retries for a failed leg; nil keeps crash-and-resume
}

// AddPersona stacks a persona directly on the workflow.
func (w *Workflow) AddPersona(p *Persona) *Workflow {
	w.Personas = append(w.Personas, p)
	return w
}

// AddStep adds one ordered step, narrated in plain English and delegated to
// a Persona the runtime reasons it through (persona may be nil).
func (w *Workflow) AddStep(instruction string, persona *Persona) *Workflow {
	w.Steps = append(w.Steps, &Step{Instruction: instruction, Persona: persona})
	return w
}

// AddPolicy attaches a Policy that governs this workflow.
func (w *Workflow) AddPolicy(p *Policy) *Workflow {
	w.Policies = append(w.Policies, p)
	return w
}

// DelegatedPersonas returns every Persona this workflow reasons through --
// those delegated to a step and any stacked directly -- in order,
// de-duplicated by identity.
func (w *Workflow) DelegatedPersonas() []*Persona {
	seen := map[*Persona]bool{}
	var ordered []*Persona
	for _, step := range w.Steps {
		if step.Persona != nil && !seen[step.Persona] {
			seen[step.Persona] = true
			ordered = append(ordered, step.Persona)
		}
	}
	for _, p := range w.Personas {
		if !seen[p] {
			seen[p] = true
			ordered = append(ordered, p)
		}
	}
	return ordered
}

// Process is a stack of Workflows that performs an action. Its description
// lets the Discoverer reason about relevance in natural language.
type Process struct {
	Name        string
	Description string
	Workflows   []*Workflow
}

// AddWorkflow stacks a workflow onto the process.
func (p *Process) AddWorkflow(w *Workflow) *Process {
	p.Workflows = append(p.Workflows, w)
	return p
}

// Tool is a capability declared to the runtime in plain English.
type Tool struct {
	Name        string
	Description string
	Command     string
	Origin      string // "authored" (default) or "acquired"
}

// Describe renders the tool as a single declaration line.
func (t Tool) Describe() string {
	line := t.Name
	if t.Description != "" {
		line += ": " + t.Description
	}
	if t.Command != "" {
		line += " (invoked via `" + t.Command + "`)"
	}
	return line
}

// ContractField is one declared deliverable field: its authored name and
// its meaning in plain English.
type ContractField struct {
	Name    string
	Meaning string
}

// Identifier is the field name as a signature-safe identifier
// ("risk grade" -> "risk_grade").
func (f ContractField) Identifier() string {
	var b strings.Builder
	for _, ch := range strings.ToLower(strings.TrimSpace(f.Name)) {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			b.WriteRune(ch)
		} else {
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "field"
	}
	return b.String()
}

// Contract is the structured promise a workflow's decision must honor:
// named fields with plain-English meanings.
type Contract struct {
	Name        string
	Description string
	Fields      []ContractField
}

// AddField declares one deliverable field on the contract.
func (c *Contract) AddField(name, meaning string) *Contract {
	c.Fields = append(c.Fields, ContractField{Name: name, Meaning: meaning})
	return c
}

// Judge judges whether filled data honors the fields' meanings and returns
// (conforms, rationale). With no model bound the check is structural only --
// every declared field present with a non-empty value -- and the rationale
// says so, matching the Python package's deterministic fallback. The
// meaning-level judgment (does each value actually mean what the field
// declares) requires a live model and is not performed here.
func (c *Contract) Judge(data map[string]any) (bool, string) {
	var missing []string
	for _, f := range c.Fields {
		v, ok := data[f.Name]
		if !ok || v == nil || strings.TrimSpace(fmt.Sprint(v)) == "" {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		return false, "structural check only (no model bound): missing or empty fields: " + strings.Join(missing, ", ")
	}
	return true, "structural check only (no model bound): every declared field is present and non-empty"
}

// RenderFields renders the contract's fields as declaration bullets.
func (c *Contract) RenderFields() string {
	lines := make([]string, len(c.Fields))
	for i, f := range c.Fields {
		meaning := f.Meaning
		if meaning == "" {
			meaning = "no meaning declared"
		}
		lines[i] = "- " + f.Name + ": " + meaning
	}
	return strings.Join(lines, "\n")
}

// ToMarkdown renders this workflow the way workflow.md stacks one.
func (w *Workflow) ToMarkdown() string {
	lines := []string{"## " + w.Name, ""}
	if w.Pattern != "" {
		lines = append(lines, "Pattern: "+w.Pattern)
	}
	if w.Routes != "" {
		lines = append(lines, "Routes: "+w.Routes)
	}
	if w.RetryBudget != nil {
		lines = append(lines, fmt.Sprintf("Retries: retry a failed leg %d times", *w.RetryBudget))
	}
	if len(w.Policies) > 0 {
		names := make([]string, len(w.Policies))
		for i, p := range w.Policies {
			names[i] = p.Name
		}
		lines = append(lines, "Policies: "+strings.Join(names, ", "))
	}
	lines = append(lines, "")
	for number, step := range w.Steps {
		suffix := ""
		if step.Persona != nil {
			suffix = " (" + step.Persona.Name + ")"
		}
		lines = append(lines, fmt.Sprintf("%d. %s%s", number+1, step.Instruction, suffix))
	}
	if w.Contract != nil {
		lines = append(lines, "", "### Deliverable", "")
		if w.Contract.Description != "" {
			lines = append(lines, w.Contract.Description, "")
		}
		for _, f := range w.Contract.Fields {
			lines = append(lines, "- "+f.Name+": "+f.Meaning)
		}
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

// ToMarkdown renders this process the way process.md stacks one.
func (p *Process) ToMarkdown() string {
	lines := []string{"## " + p.Name, ""}
	if len(p.Workflows) > 0 {
		names := make([]string, len(p.Workflows))
		for i, w := range p.Workflows {
			names[i] = w.Name
		}
		lines = append(lines, "Workflows: "+strings.Join(names, ", "), "")
	}
	if p.Description != "" {
		lines = append(lines, p.Description)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}
