package ear

import (
	"encoding/json"
	"io"
	"iter"
	"strings"
	"sync"
	"time"
)

// ReasoningLog is the audit trail of every reasoning step -- policy
// judgments with their rationale, discovery, the deliberation with the full
// stacked prompt material, and the explanation -- so governance and
// reasoning leave a reviewable record rather than a bare boolean.
//
// If Sink is set, each Record is also streamed to it as a JSON line
// (JSONL), the same append-only trail the Python package flushes to disk;
// point Sink at an *os.File to persist it, or a bytes.Buffer to capture it.
// The in-memory Cycles always accumulate regardless of Sink.
type ReasoningLog struct {
	mu      sync.Mutex
	Sink    io.Writer
	Cycles  []TrailCycle
	current *TrailCycle
	enc     *json.Encoder
}

// TrailCycle is one reasoning cycle's ordered records, stamped with the moment it
// opened so retention can rotate whole expired cycles out of the trail.
type TrailCycle struct {
	IntentText string    `json:"intent"`
	Started    time.Time `json:"started"`
	Records    []Record  `json:"records"`
}

// Record is one logged reasoning step.
type Record struct {
	Stage     string         `json:"stage"`
	Time      time.Time      `json:"time"`
	Inputs    map[string]any `json:"inputs,omitempty"`
	Output    string         `json:"output"`
	Rationale string         `json:"rationale,omitempty"`
	Model     string         `json:"model"`
}

// BeginCycle opens a new cycle keyed to the intent and stamped now.
func (l *ReasoningLog) BeginCycle(intent Intent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Cycles = append(l.Cycles, TrailCycle{IntentText: intent.Text, Started: time.Now()})
	l.current = &l.Cycles[len(l.Cycles)-1]
}

// Rotate drops whole cycles whose start is older than retentionDays measured
// from now, and returns how many were removed. Zero (or a non-positive
// window) is a no-op, so retention only takes effect when memory.md declares
// a "keep N days" window. The Python package rotates individual records by
// timestamp; rotating at the cycle boundary keeps a cycle's trail intact.
func (l *ReasoningLog) Rotate(retentionDays float64, now time.Time) int {
	if retentionDays <= 0 {
		return 0
	}
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.Add(-time.Duration(retentionDays * float64(24*time.Hour)))
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.Cycles[:0:0]
	removed := 0
	for _, c := range l.Cycles {
		if !c.Started.IsZero() && c.Started.Before(cutoff) {
			removed++
			continue
		}
		kept = append(kept, c)
	}
	l.Cycles = kept
	l.current = nil
	return removed
}

// Record appends one step to the current cycle (opening a headless cycle if
// none is active, so records are never lost) and streams it to Sink.
func (l *ReasoningLog) Record(r Record) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current == nil {
		l.Cycles = append(l.Cycles, TrailCycle{})
		l.current = &l.Cycles[len(l.Cycles)-1]
	}
	if r.Model == "" {
		r.Model = "deterministic-fallback"
	}
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	l.current.Records = append(l.current.Records, r)
	if l.Sink != nil {
		if l.enc == nil {
			l.enc = json.NewEncoder(l.Sink)
		}
		// Best-effort streaming: a sink write failure must never crash a
		// reasoning cycle, and the record is retained in memory regardless.
		_ = l.enc.Encode(r)
	}
}

// LastCycle returns the most recent cycle, or nil.
func (l *ReasoningLog) LastCycle() *TrailCycle {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.Cycles) == 0 {
		return nil
	}
	return &l.Cycles[len(l.Cycles)-1]
}

// Records returns a single-pass iterator over every record across every
// cycle, in order. It is a range-over-func iterator (Go 1.23+), so callers
// can `for rec := range log.Records()` without materializing a slice:
//
//	for rec := range log.Records() {
//	    if rec.Stage == "policy" { ... }
//	}
func (l *ReasoningLog) Records() iter.Seq[Record] {
	return func(yield func(Record) bool) {
		l.mu.Lock()
		cycles := l.Cycles
		l.mu.Unlock()
		for i := range cycles {
			for _, rec := range cycles[i].Records {
				if !yield(rec) {
					return
				}
			}
		}
	}
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
