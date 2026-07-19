package ear

import (
	"fmt"
	"strings"
)

// The concrete pipeline stages. Each is a tiny value implementing Stage.
// Mechanical stages run one deterministic step; judged stages branch on
// whether an LM is bound, taking the ported signature when it is and the
// deterministic fallback when it is not -- so binding a model lights up
// discovery, selection, scheduling, delegation, recall, explanation and
// audit without changing the pipeline.

func modelLabel(r *Runtime) string {
	if r.LM == nil {
		return ""
	}
	return "llm"
}

// -- governance -------------------------------------------------------------

type governStage struct {
	scope    string
	workflow bool
}

func (governStage) Name() string { return "govern" }

func (s governStage) Run(c *Cycle) error {
	policies := c.Runtime.Policies
	if s.workflow {
		policies = workflowPolicies(c.Plan)
	}
	violations, err := c.Runtime.govern(c.Ctx, policies, c.Intent, c.Approval)
	if err != nil {
		return err
	}
	return c.Runtime.enforce(violations, c.Approval, s.scope)
}

// -- discover ---------------------------------------------------------------

type discoverStage struct{}

func (discoverStage) Name() string { return "discover" }

func (discoverStage) Run(c *Cycle) error {
	if c.Runtime.LM == nil {
		c.Candidates = c.Runtime.discover(c.Intent)
		return nil
	}
	found, err := lmDiscover(c)
	if err != nil {
		return err
	}
	c.Candidates = found
	return nil
}

func lmDiscover(c *Cycle) ([]*Process, error) {
	r := c.Runtime
	if len(r.Processes) == 0 {
		return nil, nil
	}
	out, err := DiscoverRelevantProcesses.Run(c.Ctx, r.LM, DiscoverIn{
		IntentText:         c.Intent.Text,
		AvailableProcesses: processCatalogue(r.Processes),
	})
	if err != nil {
		return nil, err
	}
	found := resolveProcesses(out.RelevantProcessNames, r.Processes)
	if len(found) == 0 {
		found = append([]*Process{}, r.Processes...)
	}
	r.ReasoningLog.Record(Record{
		Stage:  "discovery",
		Inputs: map[string]any{"intent": c.Intent.Text, "available_processes": processNames(r.Processes)},
		Output: orNone(processNames(found)),
		Model:  modelLabel(r),
	})
	return found, nil
}

// -- select -----------------------------------------------------------------

type selectStage struct{}

func (selectStage) Name() string { return "select" }

func (selectStage) Run(c *Cycle) error {
	deduped := selectProcesses(c.Candidates)
	if c.Runtime.LM == nil || len(deduped) <= 1 {
		c.Selected = deduped
		return nil
	}
	out, err := SelectProcesses.Run(c.Ctx, c.Runtime.LM, SelectIn{
		IntentText:         c.Intent.Text,
		CandidateProcesses: processCatalogue(deduped),
	})
	if err != nil {
		return err
	}
	chosen := resolveProcesses(out.SelectedProcessNames, deduped)
	if len(chosen) == 0 {
		chosen = deduped
	}
	c.Selected = chosen
	c.Runtime.ReasoningLog.Record(Record{
		Stage:  "selection",
		Inputs: map[string]any{"intent": c.Intent.Text, "candidates": processNames(deduped)},
		Output: orNone(processNames(chosen)),
		Model:  modelLabel(c.Runtime),
	})
	return nil
}

// -- compose ----------------------------------------------------------------

type composeStage struct{}

func (composeStage) Name() string { return "compose" }

func (composeStage) Run(c *Cycle) error {
	c.Plan = compose(c.Selected)
	return nil
}

// -- schedule ---------------------------------------------------------------

type scheduleStage struct{}

func (scheduleStage) Name() string { return "schedule" }

func (scheduleStage) Run(c *Cycle) error {
	plan := schedule(c.Plan)
	if c.Runtime.LM == nil || len(plan) <= 1 {
		c.Plan = plan
		return nil
	}
	out, err := ScheduleWorkflows.Run(c.Ctx, c.Runtime.LM, ScheduleIn{
		IntentText: c.Intent.Text,
		Workflows:  workflowSummaries(plan),
	})
	if err != nil {
		return err
	}
	c.Plan = reorderWorkflows(out.OrderedWorkflowNames, plan)
	c.Runtime.ReasoningLog.Record(Record{
		Stage:  "scheduling",
		Inputs: map[string]any{"intent": c.Intent.Text, "composed_order": workflowNames(plan)},
		Output: strings.Join(workflowNames(c.Plan), ", "),
		Model:  modelLabel(c.Runtime),
	})
	return nil
}

// -- delegate ---------------------------------------------------------------

type delegateStage struct{}

func (delegateStage) Name() string { return "delegate" }

func (delegateStage) Run(c *Cycle) error {
	if c.Runtime.LM == nil {
		return nil // deterministic: steps stay as authored
	}
	for _, w := range c.Plan {
		var undelegated []*Step
		for _, step := range w.Steps {
			if step.Persona == nil {
				undelegated = append(undelegated, step)
			}
		}
		pool := w.DelegatedPersonas()
		if len(undelegated) == 0 || len(pool) == 0 {
			continue
		}
		out, err := DelegateSteps.Run(c.Ctx, c.Runtime.LM, DelegateIn{
			Steps:    numberedSteps(undelegated),
			Personas: personaPool(pool),
		})
		if err != nil {
			return err
		}
		applyDelegations(out.Assignments, undelegated, pool)
	}
	return nil
}

// -- recall -----------------------------------------------------------------

type recallStage struct{}

func (recallStage) Name() string { return "recall" }

func (recallStage) Run(c *Cycle) error {
	window := c.Runtime.recall()
	if c.Runtime.LM == nil || window == "" {
		c.Recalled = window
		return nil
	}
	out, err := RecallRelevantMemory.Run(c.Ctx, c.Runtime.LM, RecallIn{
		IntentText: c.Intent.Text,
		History:    window,
	})
	if err != nil {
		return err
	}
	// An empty recall falls back to the full window: forgetting everything is
	// never the right reading of "nothing was relevant".
	if strings.TrimSpace(out.RelevantContext) == "" {
		c.Recalled = window
	} else {
		c.Recalled = out.RelevantContext
	}
	return nil
}

// -- reason -----------------------------------------------------------------

type reasonStage struct{}

func (reasonStage) Name() string { return "reason" }

func (reasonStage) Run(c *Cycle) error {
	decision, err := c.Runtime.Reasoner.Reason(c.Ctx, c.Runtime, c.Intent, c.Plan)
	if err != nil {
		return err
	}
	decision, err = validate(decision)
	if err != nil {
		return err
	}
	c.Decision = decision
	return nil
}

// -- formalize --------------------------------------------------------------

type formalizeStage struct{}

func (formalizeStage) Name() string { return "formalize" }

func (formalizeStage) Run(c *Cycle) error {
	r := c.Runtime
	if r.LM == nil {
		// No model to extract with: record the skip per contract rather than
		// fabricating deliverable values.
		r.formalize(c.Plan)
		return nil
	}
	data := map[string]any{}
	for _, w := range c.Plan {
		if w.Contract == nil {
			continue
		}
		conforms, rationale, extracted, err := extractAndJudge(c, w.Contract)
		if err != nil {
			return err
		}
		output := "conformant"
		if !conforms {
			output = "NONCONFORMING -- data withheld from the decision"
		}
		r.ReasoningLog.Record(Record{
			Stage:     "contract",
			Inputs:    map[string]any{"contract": w.Contract.Name, "fields": w.Contract.RenderFields(), "data": extracted},
			Output:    output,
			Rationale: rationale,
			Model:     modelLabel(r),
		})
		if conforms {
			for k, v := range extracted {
				data[k] = v
			}
		}
	}
	if len(data) > 0 {
		c.Data = data
	}
	return nil
}

// extractAndJudge fills a contract's fields from the decision and judges the
// filling against the authored meanings, with one hinted retry -- the
// Python package's _formalize loop.
func extractAndJudge(c *Cycle, contract *Contract) (bool, string, map[string]any, error) {
	lm := c.Runtime.LM
	extracted, err := contract.Extract(c.Ctx, lm, c.Decision, c.Intent.Text, "")
	if err != nil {
		return false, "", nil, err
	}
	conforms, rationale, err := contract.JudgeWithModel(c.Ctx, lm, extracted)
	if err != nil {
		return false, "", nil, err
	}
	if !conforms {
		extracted, err = contract.Extract(c.Ctx, lm, c.Decision, c.Intent.Text, rationale)
		if err != nil {
			return false, "", nil, err
		}
		conforms, rationale, err = contract.JudgeWithModel(c.Ctx, lm, extracted)
		if err != nil {
			return false, "", nil, err
		}
	}
	return conforms, rationale, extracted, nil
}

// -- evidence ---------------------------------------------------------------

type evidenceStage struct{}

func (evidenceStage) Name() string { return "evidence" }

func (evidenceStage) Run(c *Cycle) error {
	c.Evidence = c.Runtime.buildEvidence(c.Intent, c.Plan, c.Recalled)
	if len(c.Data) > 0 {
		c.Evidence.Sources["data"] = c.Data
	}
	return nil
}

// -- explain ----------------------------------------------------------------

type explainStage struct{}

func (explainStage) Name() string { return "explain" }

func (explainStage) Run(c *Cycle) error {
	var explanation string
	if c.Runtime.LM == nil {
		explanation = explain(c.Evidence, c.Decision)
	} else {
		out, err := ExplainDecision.Run(c.Ctx, c.Runtime.LM, ExplainIn{
			Basis:    c.Evidence.Basis,
			Decision: fmt.Sprint(c.Decision),
		})
		if err != nil {
			return err
		}
		explanation = out.Explanation
	}
	c.Evidence.Sources["explanation"] = explanation
	c.Runtime.ReasoningLog.Record(Record{
		Stage:  "explanation",
		Inputs: map[string]any{"basis": c.Evidence.Basis, "decision": fmt.Sprint(c.Decision)},
		Output: explanation,
		Model:  modelLabel(c.Runtime),
	})
	return nil
}

// -- audit ------------------------------------------------------------------

type auditStage struct{}

func (auditStage) Name() string { return "audit" }

func (auditStage) Run(c *Cycle) error {
	if c.Runtime.LM != nil {
		out, err := AuditEvidence.Run(c.Ctx, c.Runtime.LM, AuditIn{
			Decision: fmt.Sprint(c.Decision),
			Evidence: evidenceSummary(c.Evidence),
		})
		if err != nil {
			return err
		}
		c.Evidence.Sources["audit_assessment"] = out.Assessment
		c.Runtime.ReasoningLog.Record(Record{
			Stage:  "audit",
			Inputs: map[string]any{"basis": c.Evidence.Basis, "decision": fmt.Sprint(c.Decision)},
			Output: out.Assessment,
			Model:  modelLabel(c.Runtime),
		})
	}
	audit(c.Evidence) // always records the control fact (audited flag)
	return nil
}

// -- memory -----------------------------------------------------------------

type memoryStage struct{}

func (memoryStage) Name() string { return "memory" }

func (memoryStage) Run(c *Cycle) error {
	entry := c.Runtime.Memory.Record(c.Intent.Text, c.Decision, c.Intent.Context, c.Evidence)
	c.Runtime.Experience.ObserveEntry(entry)
	if learned := c.Runtime.adapt(); learned != nil {
		c.Runtime.ReasoningLog.Record(Record{
			Stage:  "adaptation",
			Inputs: map[string]any{"experience": c.Runtime.Experience.Summary()},
			Output: learned.Insight,
		})
	}
	return nil
}

// -- rendering & resolution helpers -----------------------------------------

func processCatalogue(processes []*Process) string {
	lines := make([]string, len(processes))
	for i, p := range processes {
		desc := p.Description
		if desc == "" {
			desc = "no description"
		}
		lines[i] = p.Name + ": " + desc
	}
	return strings.Join(lines, "\n")
}

func skillCatalogue(skills []*Skill) string {
	lines := make([]string, len(skills))
	for i, s := range skills {
		lines[i] = s.Name + ": " + s.Instruction()
	}
	return strings.Join(lines, "\n")
}

func resolveSkills(names []string, pool []*Skill) []*Skill {
	byKey := map[string]*Skill{}
	for _, s := range pool {
		byKey[Normalize(s.Name)] = s
	}
	var found []*Skill
	seen := map[*Skill]bool{}
	for _, name := range names {
		if s := byKey[Normalize(name)]; s != nil && !seen[s] {
			seen[s] = true
			found = append(found, s)
		}
	}
	return found
}

func resolveProcesses(names []string, pool []*Process) []*Process {
	byKey := map[string]*Process{}
	for _, p := range pool {
		byKey[Normalize(p.Name)] = p
	}
	var found []*Process
	seen := map[*Process]bool{}
	for _, name := range names {
		if p := byKey[Normalize(name)]; p != nil && !seen[p] {
			seen[p] = true
			found = append(found, p)
		}
	}
	return found
}

func workflowNames(plan []*Workflow) []string {
	names := make([]string, len(plan))
	for i, w := range plan {
		names[i] = w.Name
	}
	return names
}

func workflowSummaries(plan []*Workflow) string {
	lines := make([]string, len(plan))
	for i, w := range plan {
		var steps []string
		for j, s := range w.Steps {
			if j >= 4 {
				break
			}
			steps = append(steps, s.Instruction)
		}
		summary := strings.Join(steps, "; ")
		if summary == "" {
			summary = "no steps"
		}
		lines[i] = w.Name + ": " + summary
	}
	return strings.Join(lines, "\n")
}

func reorderWorkflows(names []string, plan []*Workflow) []*Workflow {
	byKey := map[string]*Workflow{}
	for _, w := range plan {
		byKey[Normalize(w.Name)] = w
	}
	var ordered []*Workflow
	placed := map[*Workflow]bool{}
	for _, name := range names {
		if w := byKey[Normalize(name)]; w != nil && !placed[w] {
			placed[w] = true
			ordered = append(ordered, w)
		}
	}
	// Every composed workflow stays: any the model forgot keep their order.
	for _, w := range plan {
		if !placed[w] {
			ordered = append(ordered, w)
		}
	}
	return ordered
}

func numberedSteps(steps []*Step) string {
	lines := make([]string, len(steps))
	for i, s := range steps {
		lines[i] = fmt.Sprintf("%d: %s", i+1, s.Instruction)
	}
	return strings.Join(lines, "\n")
}

func personaPool(pool []*Persona) string {
	lines := make([]string, len(pool))
	for i, p := range pool {
		line := p.Name + ": " + orText(p.Instructions, "no standing instructions")
		if len(p.Skills) > 0 {
			var names []string
			for _, s := range p.Skills {
				names = append(names, s.Name)
			}
			line += " -- skills: " + strings.Join(names, ", ")
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func applyDelegations(assignments []string, undelegated []*Step, pool []*Persona) {
	byNumber := map[int]*Step{}
	for i, s := range undelegated {
		byNumber[i+1] = s
	}
	byName := map[string]*Persona{}
	for _, p := range pool {
		byName[Normalize(p.Name)] = p
	}
	for _, a := range assignments {
		numberText, personaName, ok := strings.Cut(a, ":")
		if !ok {
			continue
		}
		var number int
		if _, err := fmt.Sscanf(strings.TrimSpace(numberText), "%d", &number); err != nil {
			continue
		}
		step := byNumber[number]
		persona := byName[Normalize(personaName)]
		if step != nil && persona != nil && step.Persona == nil {
			step.Persona = persona
		}
	}
}

func evidenceSummary(e *Evidence) string {
	lines := []string{"Basis: " + e.Basis}
	for _, key := range []string{"policies_checked", "plan", "recalled_memory"} {
		if v, ok := e.Sources[key]; ok && v != nil {
			lines = append(lines, fmt.Sprintf("%s: %v", key, v))
		}
	}
	return strings.Join(lines, "\n")
}

func orText(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
