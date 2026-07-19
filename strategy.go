package ear

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Strategy is the runtime's operating strategy, stacked in memory.md. This
// port parses the sections whose effect is deterministic -- context-history
// capacity, audit-trail retention, declared tools, the working ontology,
// subagent-spawning limits and skills-discovery guidance -- and captures the
// model-selection prose. It also parses pricing, budget, model selection and
// knowledge sources (all wired). The sections whose effect needs an unported
// plane (MCP, sandbox, energy, evolution, toolsets, auxiliary model,
// cross-session store) are recognised but left inert.
type Strategy struct {
	Raw string

	ContextHistory  string
	HistoryCapacity int // 0 = unset

	AuditTrail    string
	AuditEnabled  bool
	AuditPath     string
	RetentionDays float64 // 0 = unset

	SubagentSpawning    string
	SubagentsConfigured bool
	SubagentsEnabled    bool
	MaxSubagents        int

	SkillsDiscovery string

	Tools           []Tool
	ToolAcquisition bool

	Ontology Ontology

	// Model selection, authored in memory.md's `## Model Selection` section
	// ("Reason with anthropic/claude-opus-4-8, reading the credential from
	// ANTHROPIC_API_KEY, at a temperature of 0.2."). The provider, model id
	// and params are parsed from prose; the API key is NEVER in markdown --
	// only the env var that holds it is named. ModelClient builds the client.
	ModelSelection  string // raw prose
	Provider        string
	Model           string // "provider/model"
	APIKeyEnvVar    string
	APIBase         string
	Temperature     *float64
	MaxOutputTokens int

	Pricing              string
	InputRatePerMillion  *float64 // nil = undeclared
	OutputRatePerMillion *float64

	// Budget and the alert thresholds it is measured against, both authored
	// in memory.md's `## Budget` section in plain English (e.g. "The budget
	// is $50. Alert at 25%, 50% and 90%."). Budget is 0 when none is
	// declared; the thresholds are fractions, sorted ascending.
	Budget          float64
	AlertThresholds []float64

	// KnowledgeSources declared in memory.md's `## Knowledge` section (name:
	// `path`); the loader reads and chunks them into a corpus.
	KnowledgeSources []KnowledgeSource
}

// Dollars returns the declared cost of a token spend and whether pricing was
// authored at all. A figure nobody declared is never invented (the bool is
// false). Cached input is priced off the input rate at the provider-standard
// multipliers (cache read ~0.1x, cache write ~1.25x); input is the uncached
// remainder, so the three input counts never double-bill.
func (s *Strategy) Dollars(input, output, cacheRead, cacheWrite int) (float64, bool) {
	if s.InputRatePerMillion == nil && s.OutputRatePerMillion == nil {
		return 0, false
	}
	cost := 0.0
	if s.InputRatePerMillion != nil {
		rate := *s.InputRatePerMillion / 1_000_000
		cost += float64(input) * rate
		cost += float64(cacheRead) * rate * 0.1
		cost += float64(cacheWrite) * rate * 1.25
	}
	if s.OutputRatePerMillion != nil {
		cost += float64(output) * *s.OutputRatePerMillion / 1_000_000
	}
	return cost, true
}

// Ontology is the working vocabulary the runtime reasons with.
type Ontology struct {
	Terms map[string]string
	Order []string
	Notes string
}

// Define records a term and its meaning, preserving first-seen order.
func (o *Ontology) Define(term, meaning string) {
	if o.Terms == nil {
		o.Terms = map[string]string{}
	}
	if _, seen := o.Terms[term]; !seen {
		o.Order = append(o.Order, term)
	}
	o.Terms[term] = meaning
}

// Render renders the vocabulary as a natural-language block.
func (o *Ontology) Render() string {
	var lines []string
	for _, term := range o.Order {
		lines = append(lines, "- "+term+": "+o.Terms[term])
	}
	if o.Notes != "" {
		lines = append(lines, strings.TrimSpace(o.Notes))
	}
	if len(lines) == 0 {
		return ""
	}
	return "Working vocabulary:\n" + strings.Join(lines, "\n")
}

var (
	disabledRe  = regexp.MustCompile(`(?i)\b(?:do not|don't|never|disabled?|forbidden|prohibited)\b|\bno\s+sub-?agents?\b|\bswitched?\s+off\b`)
	integerRe   = regexp.MustCompile(`\b(\d+)\b`)
	backtickRe  = regexp.MustCompile("`([^`]+)`")
	urlRe       = regexp.MustCompile(`https?://[^\s]+`)
	trailReach  = regexp.MustCompile(`(?i)[,;]?\s*(?:via|using|through|over|at)\s*$`)
	cadenceDays = map[string]float64{
		"hourly": 1.0 / 24, "daily": 1.0, "nightly": 1.0, "weekly": 7.0,
		"fortnightly": 14.0, "monthly": 30.0, "quarterly": 91.0,
		"yearly": 365.0, "annually": 365.0,
	}
	unitDays = map[string]float64{
		"hour": 1.0 / 24, "day": 1.0, "week": 7.0, "month": 30.0, "year": 365.0,
	}
	countWords = map[string]int{
		"no": 0, "never": 0, "zero": 0, "once": 1, "one": 1, "twice": 2,
		"two": 2, "thrice": 3, "three": 3, "four": 4, "five": 5, "six": 6,
		"seven": 7, "eight": 8, "nine": 9, "ten": 10,
	}
)

// daysInProse returns the period a sentence declares, in days -- a cadence
// word ("weekly"), or a count with a unit ("after 2 weeks"). Returns
// (0,false) when no period was authored. Used for escalation deadlines and
// retention windows alike.
func daysInProse(text string) (float64, bool) {
	words := proseWords(text)
	for _, w := range words {
		if d, ok := cadenceDays[w]; ok {
			return d, true
		}
	}
	for i := 0; i+1 < len(words); i++ {
		count, err := strconv.ParseFloat(words[i], 64)
		if err != nil {
			continue
		}
		unit := strings.TrimSuffix(words[i+1], "s")
		if d, ok := unitDays[unit]; ok {
			return count * d, true
		}
	}
	return 0, false
}

// countInProse returns the first count a sentence declares -- a digit or a
// spoken number ("retry a failed leg twice" -> 2, "no retries" -> 0).
// Returns (0,false) when no count was authored.
func countInProse(text string) (int, bool) {
	for _, w := range proseWords(text) {
		if n, err := strconv.Atoi(w); err == nil {
			return n, true
		}
		if n, ok := countWords[w]; ok {
			return n, true
		}
	}
	return 0, false
}

func proseWords(text string) []string {
	fields := strings.Fields(text)
	out := make([]string, len(fields))
	for i, w := range fields {
		out[i] = strings.ToLower(strings.Trim(w, ".,;:()"))
	}
	return out
}

// StrategyFromMarkdown parses a memory.md document into a Strategy. Section
// dispatch mirrors the Python package's heading-substring rules, including
// the ordering that keeps "toolset" from being read as "tool" and
// "auxiliary model" from overwriting "model".
func StrategyFromMarkdown(text string) *Strategy {
	s := &Strategy{Raw: text, AuditEnabled: true, ToolAcquisition: true}
	doc := ParseDocument(text)
	for _, section := range doc.Sections {
		heading := Normalize(section.Name)
		body := section.StructuredBody()
		prose := body.Prose
		switch {
		case strings.Contains(heading, "ontolog") || strings.Contains(heading, "vocabular"):
			s.readOntology(body)
		case strings.Contains(heading, "audit"):
			s.readAudit(prose)
		case strings.Contains(heading, "budget") || strings.Contains(heading, "spend"):
			s.readBudget(prose)
		case strings.Contains(heading, "pricing") || strings.Contains(heading, "price") ||
			strings.Contains(heading, "cost"):
			s.readPricing(prose)
		case strings.Contains(heading, "knowledge"):
			s.readKnowledge(body)
		case strings.Contains(heading, "discover"):
			s.SkillsDiscovery = prose
		case strings.Contains(heading, "toolset"):
			// recognised; toolsets are an unported mechanical plane
		case strings.Contains(heading, "tool"):
			s.readTools(body)
		case strings.Contains(heading, "auxiliary") || strings.Contains(heading, "summar"):
			// recognised; auxiliary model needs the LM
		case strings.Contains(heading, "model"):
			s.readModel(prose)
		case strings.Contains(heading, "spawn") || strings.Contains(heading, "subagent") ||
			strings.Contains(heading, "sub-agent") || strings.Contains(heading, "agent"):
			s.readSubagents(prose)
		case strings.Contains(heading, "history") || strings.Contains(heading, "context") ||
			strings.Contains(heading, "memory"):
			s.readContextHistory(prose)
		}
	}
	return s
}

func (s *Strategy) readContextHistory(prose string) {
	s.ContextHistory = prose
	if m := integerRe.FindStringSubmatch(prose); m != nil {
		s.HistoryCapacity, _ = strconv.Atoi(m[1])
	}
}

func (s *Strategy) readAudit(prose string) {
	s.AuditTrail = prose
	s.AuditEnabled = !disabledRe.MatchString(prose)
	s.AuditPath = declaredPath(prose)
	// A retention window declared in a keep/retain/purge/rotate sentence.
	for _, sentence := range strings.FieldsFunc(prose, func(r rune) bool { return r == '.' || r == ';' }) {
		lowered := strings.ToLower(sentence)
		if strings.Contains(lowered, "keep") || strings.Contains(lowered, "retain") ||
			strings.Contains(lowered, "retention") || strings.Contains(lowered, "purge") ||
			strings.Contains(lowered, "rotate") {
			if days, ok := daysInProse(sentence); ok {
				s.RetentionDays = days
				break
			}
		}
	}
}

var (
	dollarAmountRe = regexp.MustCompile(`\$\s?[0-9][0-9,]*(?:\.[0-9]+)?`)
	percentRe      = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*%`)
)

// readBudget reads the spend cap and alert thresholds from prose, both
// authored in plain English: "The monthly budget is $500. Send progressive
// alerts at 25%, 50%, 75%, 90% and 100%." The first $ amount is the cap;
// every N% becomes a fraction threshold. Nothing is hardcoded -- an author
// who declares no percentages gets a monitor that tracks spend but has no
// alert points, exactly as written.
func (s *Strategy) readBudget(prose string) {
	if m := dollarAmountRe.FindString(prose); m != "" {
		cleaned := strings.NewReplacer("$", "", ",", "", " ", "").Replace(m)
		if v, err := strconv.ParseFloat(cleaned, 64); err == nil {
			s.Budget = v
		}
	}
	seen := map[float64]bool{}
	for _, m := range percentRe.FindAllStringSubmatch(prose, -1) {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			if f := v / 100; f > 0 && !seen[f] {
				seen[f] = true
				s.AlertThresholds = append(s.AlertThresholds, f)
			}
		}
	}
	sort.Float64s(s.AlertThresholds)
}

// readPricing reads token rates from prose. The reliable form is per
// million: "Input tokens cost $3 per million; output tokens cost $15 per
// million." Each sentence names input or output, a $ amount, and the scale
// word (million / thousand / token).
func (s *Strategy) readPricing(prose string) {
	s.Pricing = prose
	for _, sentence := range strings.FieldsFunc(prose, func(r rune) bool { return r == '.' || r == ';' }) {
		words := strings.Fields(strings.ToLower(sentence))
		rate, found := 0.0, false
		for _, w := range words {
			if strings.HasPrefix(w, "$") {
				if v, err := strconv.ParseFloat(strings.Trim(w, "$,()"), 64); err == nil {
					rate, found = v, true
					break
				}
			}
		}
		if !found {
			continue
		}
		has := func(word string) bool {
			for _, w := range words {
				if w == word {
					return true
				}
			}
			return false
		}
		switch {
		case has("thousand") || has("1k"):
			rate *= 1000
		case has("token") && !has("million") && !has("1m"):
			rate *= 1_000_000
		}
		if has("input") {
			v := rate
			s.InputRatePerMillion = &v
		}
		if has("output") {
			v := rate
			s.OutputRatePerMillion = &v
		}
	}
}

func (s *Strategy) readSubagents(prose string) {
	s.SubagentSpawning = prose
	s.SubagentsConfigured = true
	s.SubagentsEnabled = !disabledRe.MatchString(prose)
	if s.SubagentsEnabled {
		if m := integerRe.FindStringSubmatch(prose); m != nil {
			s.MaxSubagents, _ = strconv.Atoi(m[1])
		}
	}
}

func (s *Strategy) readTools(body Body) {
	for _, bullet := range body.Bullets {
		name, description := splitDeclaration(bullet)
		command, _, cleaned := extractReach(description)
		s.Tools = append(s.Tools, Tool{Name: name, Description: cleaned, Command: command})
	}
	prose := strings.ToLower(body.Prose)
	if prose != "" && (disabledRe.MatchString(prose) ||
		strings.Contains(prose, "fixed toolset") || strings.Contains(prose, "no new tools")) {
		s.ToolAcquisition = false
	}
}

// readKnowledge reads declared corpus sources: one `- name: `path“ bullet
// per source, the path backticked in the bullet's text.
func (s *Strategy) readKnowledge(body Body) {
	for _, bullet := range body.Bullets {
		name, description := splitDeclaration(bullet)
		command, _, _ := extractReach(description)
		path := command
		if path == "" {
			path = strings.TrimSpace(description)
		}
		if name != "" && path != "" {
			s.KnowledgeSources = append(s.KnowledgeSources, KnowledgeSource{Name: name, Path: path})
		}
	}
}

func (s *Strategy) readOntology(body Body) {
	for _, bullet := range body.Bullets {
		term, meaning := splitDeclaration(bullet)
		if meaning != "" {
			s.Ontology.Define(term, meaning)
		} else {
			s.Ontology.Notes = strings.TrimSpace(s.Ontology.Notes + "\n" + term)
		}
	}
	if body.Prose != "" {
		s.Ontology.Notes = strings.TrimSpace(s.Ontology.Notes + "\n" + body.Prose)
	}
}

// splitDeclaration splits a declaration bullet into (name, description) on
// the first ':' or dash separator; a bullet with no separator is all name.
func splitDeclaration(bullet string) (string, string) {
	for _, sep := range []string{":", "—", "–", " -- ", " - "} {
		if name, description, ok := strings.Cut(bullet, sep); ok {
			if n := strings.TrimSpace(name); n != "" && len(n) <= 60 {
				return n, strings.TrimSpace(description)
			}
		}
	}
	return strings.TrimSpace(bullet), ""
}

// extractReach pulls a backticked command and/or a URL out of a
// declaration's description, returning (command, url, cleaned description).
func extractReach(description string) (string, string, string) {
	command := ""
	if m := backtickRe.FindStringSubmatch(description); m != nil {
		command = strings.TrimSpace(m[1])
	}
	url := ""
	if m := urlRe.FindString(description); m != "" {
		url = strings.TrimRight(m, ".,;")
	}
	cleaned := backtickRe.ReplaceAllString(description, "")
	cleaned = urlRe.ReplaceAllString(cleaned, "")
	cleaned = trailReach.ReplaceAllString(strings.TrimSpace(cleaned), "")
	return command, url, strings.Trim(cleaned, " ,;")
}

// declaredPath returns the first file-like path mentioned in prose, or "".
func declaredPath(prose string) string {
	for _, w := range strings.Fields(prose) {
		w = strings.Trim(w, "`.,;()")
		if strings.Contains(w, "/") || strings.HasSuffix(w, ".md") ||
			strings.HasSuffix(w, ".jsonl") || strings.HasSuffix(w, ".json") ||
			strings.HasSuffix(w, ".log") {
			if strings.ContainsAny(w, "./") {
				return w
			}
		}
	}
	return ""
}
