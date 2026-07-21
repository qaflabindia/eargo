package ear

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// LoadRuntime stacks a directory of natural-language markdown files into a
// Runtime: prompts stacked in skills.md, skills in persona.md, steps in
// workflow.md, workflows in process.md, governance in policy.md, and the
// org in tenant.md. The author writes no code -- the whole stack is plain
// English, assembled here.
func LoadRuntime(directory, name string) (*Runtime, error) {
	return (&loader{directory: directory}).load(name)
}

type loader struct {
	directory string
}

var fileCandidates = map[string][]string{
	"skills":    {"skills.md", "skill.md"},
	"personas":  {"persona.md", "personas.md"},
	"workflows": {"workflow.md", "workflows.md"},
	"processes": {"process.md", "processes.md"},
	"policies":  {"policy.md", "policies.md"},
	"tenant":    {"tenant.md", "org.md"},
	"memory":    {"memory.md"},
}

var (
	runtimeScopes = map[string]bool{
		"runtime": true, "the runtime": true, "all": true, "everything": true,
		"global": true, "the whole runtime": true,
	}
	toolScopes = map[string]bool{
		"tools": true, "tool": true, "tool calls": true, "tool call": true,
		"tool invocations": true, "tool invocation": true, "any tool": true,
	}
	refSplit         = regexp.MustCompile(`[,;]`)
	delegationParen  = regexp.MustCompile(`\(([^()]+)\)\s*$`)
	delegationSquare = regexp.MustCompile(`\[([^\[\]]+)\]\s*$`)
	delegationDash   = regexp.MustCompile(`\s(?:--|—|–)\s*([^—–]+?)\s*$`)
	delegationPrefix = regexp.MustCompile(`(?i)^(?:delegated?\s+to|persona|by)\s*:?\s*`)
)

func (l *loader) read(kind string) string {
	for _, filename := range fileCandidates[kind] {
		path := filepath.Join(l.directory, filename)
		if data, err := os.ReadFile(path); err == nil {
			return string(data)
		}
	}
	return ""
}

func (l *loader) parse(kind string) Document {
	return ParseDocument(l.read(kind))
}

func (l *loader) load(name string) (*Runtime, error) {
	skills := loadSkills(l.parse("skills"))
	personas, err := loadPersonas(l.parse("personas"), skills)
	if err != nil {
		return nil, err
	}
	policies, scopes, err := loadPolicies(l.parse("policies"))
	if err != nil {
		return nil, err
	}
	workflows, workflowOrder, err := loadWorkflows(l.parse("workflows"), personas, policies)
	if err != nil {
		return nil, err
	}
	processesDoc := l.parse("processes")
	processes, referenced, err := loadProcesses(processesDoc, workflows)
	if err != nil {
		return nil, err
	}
	tenant, err := loadTenant(l.parse("tenant"))
	if err != nil {
		return nil, err
	}

	if name == "" {
		name = processesDoc.Title
	}
	if name == "" {
		name = filepath.Base(l.directory)
	}
	runtime := NewRuntime(name)
	runtime.Tenant = tenant
	for _, p := range processes {
		runtime.AddProcess(p)
	}
	// A workflow no process references is still the author's work: wrap it
	// in a process of its own rather than dropping it, in stable order.
	for _, key := range workflowOrder {
		if !referenced[key] {
			w := workflows[key]
			orphan := &Process{Name: w.Name, Description: "Runs the " + w.Name + " workflow."}
			orphan.AddWorkflow(w)
			runtime.AddProcess(orphan)
		}
	}

	if err := applyPolicyScopes(runtime, policies, scopes, workflows); err != nil {
		return nil, err
	}
	applyMemoryStrategy(runtime, l.read("memory"))
	l.loadKnowledge(runtime)
	l.loadSessionStore(runtime)
	if err := l.loadAuditTrail(runtime); err != nil {
		return nil, err
	}
	return runtime, nil
}

func loadSkills(doc Document) map[string]*Skill {
	skills := map[string]*Skill{}
	for _, section := range doc.Sections {
		body := section.StructuredBody("description")
		skills[Normalize(section.Name)] = &Skill{
			Name:        section.Name,
			Prompt:      joinProse(body),
			Description: body.Field("description"),
		}
	}
	return skills
}

func loadPersonas(doc Document, skills map[string]*Skill) (map[string]*Persona, error) {
	personas := map[string]*Persona{}
	for _, section := range doc.Sections {
		body := section.StructuredBody("skills", "skill")
		persona := &Persona{Name: section.Name, Instructions: body.Prose}
		for _, ref := range splitReferences(body.Field("skills", "skill")) {
			skill, err := resolveSkill(skills, ref)
			if err != nil {
				return nil, err
			}
			persona.AddSkill(skill)
		}
		for _, bullet := range body.Bullets {
			key := Normalize(bullet)
			if s, ok := skills[key]; ok {
				persona.AddSkill(s)
			} else if strings.Contains(bullet, ":") {
				inlineName, inlinePrompt, _ := strings.Cut(bullet, ":")
				persona.AddSkill(&Skill{Name: strings.TrimSpace(inlineName), Prompt: strings.TrimSpace(inlinePrompt)})
			} else if _, err := resolveSkill(skills, bullet); err != nil {
				return nil, err
			}
		}
		personas[Normalize(section.Name)] = persona
	}
	return personas, nil
}

func loadPolicies(doc Document) (map[string]*Policy, map[string]string, error) {
	policies := map[string]*Policy{}
	scopes := map[string]string{}
	for _, section := range doc.Sections {
		body := section.StructuredBody(
			"fallback", "fallback expression", "applies to", "applies", "scope",
			"approval", "approvers", "approver", "escalate", "escalation",
		)
		approval, err := readApprovalField(section.Name, body.Field("approval"))
		if err != nil {
			return nil, nil, err
		}
		key := Normalize(section.Name)
		policy := &Policy{
			Name:               section.Name,
			Statement:          joinProse(body),
			FallbackExpression: body.Field("fallback", "fallback expression"),
			ApprovalRequired:   approval,
			Approvers:          splitReferences(body.Field("approvers", "approver")),
			Escalation:         body.Field("escalate", "escalation"),
		}
		// A declared escalation must name a readable period; a deadline the
		// runner silently can't read is a governance hole, not a default.
		if policy.Escalation != "" {
			days, ok := daysInProse(policy.Escalation)
			if !ok {
				return nil, nil, fmt.Errorf("policy '%s' declares Escalate '%s' but no readable period -- write 'Escalate: after 3 days'", section.Name, policy.Escalation)
			}
			policy.EscalationDays = &days
		}
		policies[key] = policy
		scopes[key] = body.Field("applies to", "applies", "scope")
	}
	return policies, scopes, nil
}

func readApprovalField(policyName, value string) (bool, error) {
	if value == "" {
		return false, nil
	}
	words := map[string]bool{}
	for _, w := range strings.Fields(Normalize(value)) {
		words[w] = true
	}
	for _, no := range []string{"no", "not", "none", "never", "false"} {
		if words[no] {
			return false, nil
		}
	}
	for _, yes := range []string{"required", "needed", "mandatory", "human", "yes", "true"} {
		if words[yes] {
			return true, nil
		}
	}
	return false, fmt.Errorf("policy '%s' has an unreadable Approval field '%s' -- write 'Approval: required' or 'Approval: not required'", policyName, value)
}

func loadWorkflows(doc Document, personas map[string]*Persona, policies map[string]*Policy) (map[string]*Workflow, []string, error) {
	workflows := map[string]*Workflow{}
	var order []string
	var last *Workflow
	for _, section := range doc.Sections {
		if strings.Contains(Normalize(section.Name), "deliverable") {
			if last == nil {
				return nil, nil, fmt.Errorf("deliverable section '%s' has no workflow above it to attach to", section.Name)
			}
			contract, err := loadContract(section, last)
			if err != nil {
				return nil, nil, err
			}
			last.Contract = contract
			continue
		}
		body := section.StructuredBody(
			"persona", "delegate to", "delegate", "policies", "policy",
			"pattern", "routes", "route", "retries", "retry",
		)
		workflow := &Workflow{
			Name:    section.Name,
			Pattern: body.Field("pattern"),
			Routes:  body.Field("routes", "route"),
		}
		// A declared retry budget must name a readable count.
		if retries := body.Field("retries", "retry"); retries != "" {
			count, ok := countInProse(retries)
			if !ok {
				return nil, nil, fmt.Errorf("workflow '%s' declares Retries '%s' but no readable count -- write 'Retries: retry a failed leg twice'", section.Name, retries)
			}
			workflow.RetryBudget = &count
		}
		var defaultPersona *Persona
		if ref := body.Field("persona", "delegate to", "delegate"); ref != "" {
			p, err := resolvePersona(personas, ref)
			if err != nil {
				return nil, nil, err
			}
			defaultPersona = p
		}
		items := body.Numbered
		if len(items) == 0 {
			items = body.Bullets
		}
		for _, item := range items {
			instruction, persona := splitDelegation(item, personas)
			if persona == nil {
				persona = defaultPersona
			}
			workflow.AddStep(instruction, persona)
		}
		for _, ref := range splitReferences(body.Field("policies", "policy")) {
			p, err := resolvePolicy(policies, ref)
			if err != nil {
				return nil, nil, err
			}
			workflow.AddPolicy(p)
		}
		key := Normalize(section.Name)
		workflows[key] = workflow
		order = append(order, key)
		last = workflow
	}
	return workflows, order, nil
}

func loadContract(section Section, workflow *Workflow) (*Contract, error) {
	body := section.StructuredBody()
	contract := &Contract{Name: workflow.Name + " Deliverable", Description: body.Prose}
	for _, bullet := range body.Bullets {
		name, meaning, ok := strings.Cut(bullet, ": ")
		if !ok {
			name, meaning, ok = strings.Cut(bullet, ":")
		}
		if ok && strings.TrimSpace(name) != "" {
			contract.AddField(strings.TrimSpace(name), strings.TrimSpace(meaning))
		} else {
			return nil, fmt.Errorf("deliverable field '%s' in '%s' must be written as 'name: meaning'", bullet, workflow.Name)
		}
	}
	if len(contract.Fields) == 0 {
		return nil, fmt.Errorf("deliverable of '%s' declares no fields -- add '- name: meaning' bullets", workflow.Name)
	}
	return contract, nil
}

func loadProcesses(doc Document, workflows map[string]*Workflow) ([]*Process, map[string]bool, error) {
	var processes []*Process
	referenced := map[string]bool{}
	for _, section := range doc.Sections {
		body := section.StructuredBody("workflows", "workflow")
		process := &Process{Name: section.Name, Description: body.Prose}
		for _, ref := range splitReferences(body.Field("workflows", "workflow")) {
			w, err := resolveWorkflow(workflows, ref)
			if err != nil {
				return nil, nil, err
			}
			process.AddWorkflow(w)
			referenced[Normalize(ref)] = true
		}
		for _, bullet := range body.Bullets {
			key := Normalize(bullet)
			if w, ok := workflows[key]; ok {
				process.AddWorkflow(w)
				referenced[key] = true
			} else if bullet != "" {
				process.Description = strings.TrimSpace(process.Description + "\n- " + bullet)
			}
		}
		processes = append(processes, process)
	}
	return processes, referenced, nil
}

func loadTenant(doc Document) (Tenant, error) {
	if len(doc.Sections) == 0 {
		return NewTenant(), nil
	}
	section := doc.Sections[0]
	body := section.StructuredBody("org id", "org", "fiscal year start", "fiscal year end", "timezone", "secret env var", "secret")
	orgID := body.Field("org id", "org")
	if orgID == "" {
		return Tenant{}, fmt.Errorf("tenant '%s' declares no 'Org id:' -- every tenant.md needs one", section.Name)
	}
	tenant := Tenant{
		OrgID:        orgID,
		Name:         section.Name,
		Timezone:     body.Field("timezone"),
		SecretEnvVar: body.Field("secret env var", "secret"),
	}
	if start := body.Field("fiscal year start"); start != "" {
		if t, err := time.Parse("2006-01-02", start); err == nil {
			tenant.FiscalYearStart = &t
		}
	}
	if end := body.Field("fiscal year end"); end != "" {
		if t, err := time.Parse("2006-01-02", end); err == nil {
			tenant.FiscalYearEnd = &t
		}
	}
	return tenant, nil
}

func applyPolicyScopes(runtime *Runtime, policies map[string]*Policy, scopes map[string]string, workflows map[string]*Workflow) error {
	for _, key := range sortedKeys(policies) {
		policy := policies[key]
		targets := splitReferences(scopes[key])
		if len(targets) == 0 {
			targets = []string{"runtime"}
		}
		for _, target := range targets {
			lowered := strings.TrimSpace(strings.ToLower(target))
			switch {
			case toolScopes[Normalize(target)]:
				// Tool-scoped policies are judged per tool call, not per
				// cycle -- so they get their own set (see Runtime.InvokeTool).
				runtime.ToolPolicies = append(runtime.ToolPolicies, policy)
			case runtimeScopes[lowered] || strings.Contains(lowered, "runtime"):
				runtime.AddPolicy(policy)
			default:
				w, err := resolveWorkflow(workflows, target)
				if err != nil {
					return err
				}
				if !hasPolicy(w.Policies, policy) {
					w.AddPolicy(policy)
				}
			}
		}
	}
	return nil
}

// loadKnowledge reads the declared `## Knowledge` sources (resolved against
// the stack directory), chunks them into a corpus, and attaches a Librarian.
// A missing source file is skipped gracefully so a stack loads without it.
func (l *loader) loadKnowledge(runtime *Runtime) {
	if runtime.Strategy == nil || len(runtime.Strategy.KnowledgeSources) == 0 {
		return
	}
	corpus := &Knowledge{}
	for _, src := range runtime.Strategy.KnowledgeSources {
		path := src.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(l.directory, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		corpus.AddDocument(src.Name, filepath.Base(path), string(data))
	}
	if corpus.Len() > 0 {
		runtime.Librarian = &Librarian{Knowledge: corpus}
	}
}

// loadSessionStore wires the cross-session store declared in memory.md's
// `## Cross-Session Data` section: it resolves the path against the stack
// directory, restores the memory layers from it before the first cycle, and
// leaves it on the runtime so every later cycle saves back. A missing or
// corrupt store restores nothing and never blocks the load.
func (l *loader) loadSessionStore(runtime *Runtime) {
	if runtime.Strategy == nil || runtime.Strategy.CrossSessionPath == "" {
		return
	}
	path := runtime.Strategy.CrossSessionPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(l.directory, path)
	}
	store := &SessionStore{Path: path}
	store.Restore(runtime)
	runtime.SessionStore = store
}

// loadAuditTrail wires the persisted reasoning trail declared in memory.md's
// `## Reasoning Audit Trail` section: the path (resolved against the stack
// directory) opens as an append-only TrailFile whose hash chain and cycle
// numbering continue from whatever the file already holds. An authored path
// that cannot be opened fails the load -- a trail the author declared but the
// runner silently can't write is a governance hole, not a default. No path
// declared (or auditing written as disabled) leaves the trail in memory only.
func (l *loader) loadAuditTrail(runtime *Runtime) error {
	s := runtime.Strategy
	if s == nil || !s.AuditEnabled || s.AuditPath == "" {
		return nil
	}
	path := s.AuditPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(l.directory, path)
	}
	trail, err := OpenTrailFile(path)
	if err != nil {
		return fmt.Errorf("audit trail declared in memory.md: %w", err)
	}
	runtime.ReasoningLog.Trail = trail
	runtime.ReasoningLog.SeedCycleNumbering(trail.MaxCycle())
	runtime.trailFile = trail
	return nil
}

// applyMemoryStrategy parses memory.md into a Strategy and wires the
// settings whose effect is deterministic: the context-history capacity, the
// declared tools, and the audit-trail retention window. The parsed Strategy
// (ontology, subagent limits, skills-discovery guidance, model-selection
// prose) is attached to the runtime for callers and for the LLM/infra ports
// that will consume it.
func applyMemoryStrategy(runtime *Runtime, memoryText string) {
	if memoryText == "" {
		return
	}
	strategy := StrategyFromMarkdown(memoryText)
	runtime.Strategy = strategy
	runtime.Tools = strategy.Tools
	if strategy.HistoryCapacity > 0 {
		runtime.Memory.Capacity = strategy.HistoryCapacity
	}
	// A `## Budget` declared in memory.md wires the non-blocking alert
	// monitor -- cap and thresholds both authored, nothing coded. A host
	// that wants a callback sets runtime.Budget.OnAlert; alerts hit the
	// trail regardless.
	if strategy.Budget > 0 {
		monitor := NewBudgetMonitor(strategy.Budget, nil, strategy.AlertThresholds...)
		monitor.Log = runtime.ReasoningLog
		runtime.Budget = monitor
	}
	// A `## Model Selection` that names a model AND whose credential is
	// present in the environment (or that names a local api_base) binds the
	// model across every seam. Absent the credential, the runtime stays on
	// its deterministic fallback -- the stack loads on a machine with no keys
	// instead of crashing.
	if client, ok := strategy.ModelClient(); ok {
		attachLM(runtime, client)
	}
	// A `## Auxiliary Model` section binds a second, usually cheaper model to
	// the mechanical seams (memory compression, adaptation distillation),
	// overriding the primary there. Wired after the primary so it wins those
	// two seams; absent or credential-less, the primary (or deterministic
	// digest) stands.
	if aux, ok := strategy.AuxModelClient(); ok {
		attachAuxiliaryLM(runtime, aux)
	}
	// A `## Subagent Spawning` section wires the Spawner with the authored
	// enable/limit -- so an author's "up to 3 subagents" (or "no subagents")
	// is enforced when the runtime spawns. Left nil when unconfigured, keeping
	// Runtime.Spawn permissive for a hand-built runtime.
	if strategy.SubagentsConfigured {
		runtime.Spawner = &Spawner{Enabled: strategy.SubagentsEnabled, Limit: strategy.MaxSubagents}
	}
}

// -- helpers ---------------------------------------------------------------

func joinProse(body Body) string {
	parts := []string{}
	if body.Prose != "" {
		parts = append(parts, body.Prose)
	}
	for _, bullet := range body.Bullets {
		parts = append(parts, "- "+bullet)
	}
	return strings.Join(parts, "\n")
}

func splitReferences(value string) []string {
	if value == "" {
		return nil
	}
	var out []string
	for _, part := range refSplit.Split(value, -1) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitDelegation(item string, personas map[string]*Persona) (string, *Persona) {
	for _, pattern := range []*regexp.Regexp{delegationParen, delegationSquare, delegationDash} {
		m := pattern.FindStringSubmatchIndex(item)
		if m == nil {
			continue
		}
		who := delegationPrefix.ReplaceAllString(strings.TrimSpace(item[m[2]:m[3]]), "")
		if persona, ok := personas[Normalize(who)]; ok {
			return strings.TrimRight(item[:m[0]], " "), persona
		}
	}
	return item, nil
}

func resolve[T any](mapping map[string]*T, reference, kind string, nameOf func(*T) string) (*T, error) {
	key := Normalize(reference)
	if v, ok := mapping[key]; ok {
		return v, nil
	}
	var known []string
	for _, k := range sortedKeys(mapping) {
		known = append(known, nameOf(mapping[k]))
	}
	list := "none"
	if len(known) > 0 {
		list = strings.Join(known, ", ")
	}
	return nil, fmt.Errorf("unknown %s '%s' referenced in the stack -- known %ss: %s", kind, reference, kind, list)
}

func resolveSkill(m map[string]*Skill, ref string) (*Skill, error) {
	return resolve(m, ref, "skill", func(s *Skill) string { return s.Name })
}
func resolvePersona(m map[string]*Persona, ref string) (*Persona, error) {
	return resolve(m, ref, "persona", func(p *Persona) string { return p.Name })
}
func resolvePolicy(m map[string]*Policy, ref string) (*Policy, error) {
	return resolve(m, ref, "policy", func(p *Policy) string { return p.Name })
}
func resolveWorkflow(m map[string]*Workflow, ref string) (*Workflow, error) {
	return resolve(m, ref, "workflow", func(w *Workflow) string { return w.Name })
}

func hasPolicy(policies []*Policy, want *Policy) bool {
	for _, p := range policies {
		if p == want {
			return true
		}
	}
	return false
}
