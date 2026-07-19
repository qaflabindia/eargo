package ear

import (
	"strings"
	"sync"
)

// ReasoningLog is the audit trail of every reasoning step -- policy
// judgments with their rationale, discovery, the deliberation with the full
// stacked prompt material, and the explanation -- so governance and
// reasoning leave a reviewable record rather than a bare boolean. In the
// Python package it also flushes to JSONL; here it is held in memory and
// grouped into cycles, which is enough for the deterministic spine.
type ReasoningLog struct {
	mu      sync.Mutex
	Cycles  []Cycle
	current *Cycle
}

// Cycle is one reasoning cycle's ordered records.
type Cycle struct {
	IntentText string
	Records    []Record
}

// Record is one logged reasoning step.
type Record struct {
	Stage     string
	Inputs    map[string]any
	Output    string
	Rationale string
	Model     string
}

// BeginCycle opens a new cycle keyed to the intent.
func (l *ReasoningLog) BeginCycle(intent Intent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Cycles = append(l.Cycles, Cycle{IntentText: intent.Text})
	l.current = &l.Cycles[len(l.Cycles)-1]
}

// Record appends one step to the current cycle (or a headless cycle if none
// is open, so records are never lost).
func (l *ReasoningLog) Record(r Record) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current == nil {
		l.Cycles = append(l.Cycles, Cycle{})
		l.current = &l.Cycles[len(l.Cycles)-1]
	}
	if r.Model == "" {
		r.Model = "deterministic-fallback"
	}
	l.current.Records = append(l.current.Records, r)
}

// LastCycle returns the most recent cycle, or nil.
func (l *ReasoningLog) LastCycle() *Cycle {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.Cycles) == 0 {
		return nil
	}
	return &l.Cycles[len(l.Cycles)-1]
}

// Render renders the whole trail as readable markdown, one section per
// cycle.
func (l *ReasoningLog) Render() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	for i := range l.Cycles {
		c := &l.Cycles[i]
		b.WriteString("## Cycle: " + c.IntentText + "\n\n")
		for _, r := range c.Records {
			b.WriteString("- **" + r.Stage + "** [" + r.Model + "]: " + r.Output + "\n")
			if r.Rationale != "" {
				b.WriteString("  - rationale: " + r.Rationale + "\n")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
