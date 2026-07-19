package ear

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// EAR keeps four memory layers deliberately distinct -- a separation AI
// systems often blur. Evidence is *why* one decision was made; Memory is
// *what happened*; Experience is the *pattern* aggregated from repeated
// memory; Adaptation is the *behaviour change* distilled from experience.
// This file holds all four.

// Evidence is the evidentiary basis for a single decision: which policies
// it cleared, which reasoning path produced it, and whatever else justifies
// "why", as opposed to merely recording "what".
type Evidence struct {
	Basis      string
	Sources    map[string]any
	Confidence float64
}

// NewEvidence builds an Evidence with an initialized sources map and full
// confidence.
func NewEvidence(basis string) *Evidence {
	return &Evidence{Basis: basis, Sources: map[string]any{}, Confidence: 1.0}
}

func (e *Evidence) String() string { return e.Basis }

// MemoryEntry is one remembered cycle: an intent, what the Reasoner
// decided, the intent's own input context, and -- separately -- the
// Evidence for why that decision was made.
type MemoryEntry struct {
	IntentText string
	Decision   any
	Context    map[string]any
	Evidence   *Evidence
	Timestamp  time.Time
}

// Render renders the entry as one history line.
func (m MemoryEntry) Render() string {
	line := fmt.Sprintf("- (%s) '%s' -> %v", m.Timestamp.Format("2006-01-02 15:04"), m.IntentText, m.Decision)
	if m.Evidence != nil {
		line += " [" + m.Evidence.Basis + "]"
	}
	return line
}

// Memory is the runtime's persistent memory. Recent cycles are kept
// verbatim in the working layer; once that grows past Capacity, the oldest
// entries are rolled into the compressed layer as a single summary string,
// keeping the context the Reasoner sees bounded.
type Memory struct {
	Capacity   int
	Working    []MemoryEntry
	Compressed []string

	// Summarizer, when set, compresses overflowed history with a model
	// instead of the deterministic digest. It takes the rendered overflow and
	// returns a summary; on error or an empty result, Compress falls back to
	// the digest, so compression never depends on a model being present.
	// Kept as a plain func so Memory stays decoupled from the LLM layer;
	// WithLM wires it.
	Summarizer func(history string) (string, error)
}

// NewMemory builds a Memory with the default capacity of 20.
func NewMemory() *Memory { return &Memory{Capacity: 20} }

// Record records one cycle, compressing overflow past capacity.
func (m *Memory) Record(intentText string, decision any, context map[string]any, evidence *Evidence) MemoryEntry {
	if context == nil {
		context = map[string]any{}
	}
	entry := MemoryEntry{
		IntentText: intentText,
		Decision:   decision,
		Context:    context,
		Evidence:   evidence,
		Timestamp:  time.Now().UTC(),
	}
	m.Working = append(m.Working, entry)
	if len(m.Working) > m.Capacity {
		m.Compress(m.Capacity)
	}
	return entry
}

// Compress rolls the oldest entries past keep out of working into one new
// summary string in compressed -- model-written when a Summarizer is set,
// the deterministic digest otherwise.
func (m *Memory) Compress(keep int) string {
	if len(m.Working) <= keep {
		return ""
	}
	var overflow []MemoryEntry
	if keep <= 0 {
		overflow, m.Working = m.Working, nil
	} else {
		cut := len(m.Working) - keep
		overflow, m.Working = m.Working[:cut], m.Working[cut:]
	}
	summary := m.summarize(overflow)
	m.Compressed = append(m.Compressed, summary)
	return summary
}

// summarize produces the compression summary: the model's, if a Summarizer is
// set and succeeds; the deterministic digest otherwise.
func (m *Memory) summarize(overflow []MemoryEntry) string {
	if m.Summarizer != nil {
		rendered := make([]string, len(overflow))
		for i, e := range overflow {
			rendered[i] = e.Render()
		}
		if s, err := m.Summarizer(strings.Join(rendered, "\n")); err == nil && strings.TrimSpace(s) != "" {
			return s
		}
	}
	var decisions []string
	for _, e := range overflow {
		d := fmt.Sprint(e.Decision)
		if len(d) > 60 {
			d = d[:60]
		}
		decisions = append(decisions, d)
	}
	return fmt.Sprintf("%d earlier cycles (e.g. %s)", len(overflow), strings.Join(decisions, ", "))
}

// ContextWindow renders compressed history plus recent working entries as
// one string, ready to drop into a reasoning prompt.
func (m *Memory) ContextWindow() string {
	var parts []string
	if len(m.Compressed) > 0 {
		parts = append(parts, "Earlier history (compressed):\n"+strings.Join(m.Compressed, "\n"))
	}
	if len(m.Working) > 0 {
		var rendered []string
		for _, e := range m.Working {
			rendered = append(rendered, e.Render())
		}
		parts = append(parts, "Recent history:\n"+strings.Join(rendered, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

// Len is the total number of remembered items across both layers.
func (m *Memory) Len() int { return len(m.Working) + len(m.Compressed) }

// Experience aggregates repeated Memory entries into counts and the
// evidence seen along the way, without yet drawing a conclusion.
type Experience struct {
	Observations   int
	DecisionCounts map[string]int
	EvidenceSeen   []*Evidence
}

// NewExperience builds an Experience with an initialized counts map.
func NewExperience() *Experience {
	return &Experience{DecisionCounts: map[string]int{}}
}

// ObserveEntry folds one Memory entry into the experience.
func (x *Experience) ObserveEntry(entry MemoryEntry) *Experience {
	key := fmt.Sprint(entry.Decision)
	x.DecisionCounts[key]++
	x.Observations++
	if entry.Evidence != nil {
		x.EvidenceSeen = append(x.EvidenceSeen, entry.Evidence)
	}
	return x
}

// MostCommonDecision returns the most frequently repeated decision and its
// count, or ("", 0) if there are no observations.
func (x *Experience) MostCommonDecision() (string, int) {
	best, bestCount := "", 0
	// Iterate in sorted key order for a deterministic winner on ties.
	keys := make([]string, 0, len(x.DecisionCounts))
	for k := range x.DecisionCounts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if x.DecisionCounts[k] > bestCount {
			best, bestCount = k, x.DecisionCounts[k]
		}
	}
	return best, bestCount
}

// Summary renders the decision counts, most frequent first.
func (x *Experience) Summary() string {
	if len(x.DecisionCounts) == 0 {
		return "No observations yet."
	}
	type kv struct {
		decision string
		count    int
	}
	ranked := make([]kv, 0, len(x.DecisionCounts))
	for d, c := range x.DecisionCounts {
		ranked = append(ranked, kv{d, c})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count != ranked[j].count {
			return ranked[i].count > ranked[j].count
		}
		return ranked[i].decision < ranked[j].decision
	})
	var lines []string
	for _, r := range ranked {
		lines = append(lines, fmt.Sprintf("- '%s': %d/%d cycles", r.decision, r.count, x.Observations))
	}
	return strings.Join(lines, "\n")
}

// Adaptation is one standing impression: an insight distilled from
// Experience that the Runtime surfaces back to the Reasoner.
type Adaptation struct {
	Name          string
	Insight       string
	Confidence    float64
	EvidenceCount int
}

// AdaptationBank is the runtime's long-term, distilled memory.
type AdaptationBank struct {
	Impressions []*Adaptation

	// Distiller, when set, turns an Experience summary into an insight with a
	// model instead of reporting the most-frequent decision. On error or an
	// empty result, LearnFrom falls back to that deterministic report. A
	// plain func, so the bank stays decoupled from the LLM layer; WithLM
	// wires it.
	Distiller func(experienceSummary string) (string, error)
}

// NewAdaptationBank builds an empty bank.
func NewAdaptationBank() *AdaptationBank { return &AdaptationBank{} }

// Add appends an adaptation to the bank.
func (b *AdaptationBank) Add(a *Adaptation) *AdaptationBank {
	b.Impressions = append(b.Impressions, a)
	return b
}

// RelevantTo returns adaptations whose insight shares a keyword with the
// intent text.
func (b *AdaptationBank) RelevantTo(intentText string) []*Adaptation {
	words := keywords(intentText)
	if len(words) == 0 {
		return append([]*Adaptation{}, b.Impressions...)
	}
	var out []*Adaptation
	for _, a := range b.Impressions {
		lower := strings.ToLower(a.Insight)
		for w := range words {
			if strings.Contains(lower, w) {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

// LearnFrom distills the current Experience into one new Adaptation -- a
// model-written insight when a Distiller is set, the most-frequently-repeated
// decision otherwise.
func (b *AdaptationBank) LearnFrom(experience *Experience) *Adaptation {
	if len(experience.DecisionCounts) == 0 {
		return nil
	}
	insight := ""
	if b.Distiller != nil {
		if s, err := b.Distiller(experience.Summary()); err == nil && strings.TrimSpace(s) != "" {
			insight = s
		}
	}
	if insight == "" {
		decision, count := experience.MostCommonDecision()
		if decision == "" && count == 0 {
			return nil
		}
		trimmed := decision
		if len(trimmed) > 80 {
			trimmed = trimmed[:80]
		}
		insight = fmt.Sprintf("Most frequent outcome: '%s' (%d/%d cycles)", trimmed, count, experience.Observations)
	}
	adaptation := &Adaptation{
		Name:          fmt.Sprintf("Learned-%d", len(b.Impressions)+1),
		Insight:       insight,
		Confidence:    1.0,
		EvidenceCount: experience.Observations,
	}
	b.Add(adaptation)
	return adaptation
}

// keywords returns the lowercased words longer than three characters,
// matching the Python package's keyword-overlap retrieval.
func keywords(text string) map[string]bool {
	words := map[string]bool{}
	for _, w := range strings.Fields(text) {
		if len(w) > 3 {
			words[strings.ToLower(w)] = true
		}
	}
	return words
}
