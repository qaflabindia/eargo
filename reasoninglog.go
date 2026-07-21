package ear

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"strings"
	"sync"
	"time"
)

// genesis is the chain's seed: the previous-hash the first record links
// against, so a trail with even one record has a fixed, verifiable start.
const genesis = "ear-genesis"

// chainLink is one link of the tamper-evident chain: the SHA-256 of the
// previous link and this record's payload. Editing any byte of any record
// breaks its own link and every link after it.
func chainLink(previous, payload string) string {
	sum := sha256.Sum256([]byte(previous + "\n" + payload))
	return fmt.Sprintf("%x", sum[:])
}

// recordPayload is the stable bytes a record's chain links over: its JSON
// with the chain field itself excluded, so verification can reproduce it.
func recordPayload(r Record) string {
	r.Chain = ""
	b, _ := json.Marshal(r)
	return string(b)
}

// RecordWriter is the seam a persisted trail implements: it receives each
// finished record as it is logged. TrailFile is the file-backed
// implementation; anything else (an exporter to an external system) is a few
// lines of the caller's own code, never a dependency of EAR's.
type RecordWriter interface {
	WriteRecord(Record) error
}

// ReasoningLog is the audit trail of every reasoning step -- policy
// judgments with their rationale, discovery, the deliberation with the full
// stacked prompt material, and the explanation -- so governance and
// reasoning leave a reviewable record rather than a bare boolean.
//
// If Sink is set, each Record is also streamed to it as a JSON line
// (JSONL); point Sink at an *os.File to persist it, or a bytes.Buffer to
// capture it. Trail, when set (the loader wires a TrailFile from memory.md's
// `## Reasoning Audit Trail`), receives each record for the append-only
// persisted trail. Both are best-effort: a write failure never breaks a
// reasoning cycle, and the in-memory Cycles always accumulate regardless.
type ReasoningLog struct {
	mu       sync.Mutex
	Sink     io.Writer
	Trail    RecordWriter
	Cycles   []TrailCycle
	current  *TrailCycle
	enc      *json.Encoder
	chainTip string // tip of the tamper-evident hash chain
	cycleNo  int    // current cycle number, monotonic across the log's life
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
	Cycle     int            `json:"cycle"`
	Stage     string         `json:"stage"`
	Time      time.Time      `json:"time"`
	Inputs    map[string]any `json:"inputs,omitempty"`
	Output    string         `json:"output"`
	Rationale string         `json:"rationale,omitempty"`
	Model     string         `json:"model"`
	Chain     string         `json:"chain,omitempty"` // tamper-evident hash-chain link
}

// SeedCycleNumbering continues cycle numbering from n, so a session resumed
// against an existing trail file never repeats cycle numbers inside the same
// audit trail. The loader calls this with the trail file's highest cycle.
func (l *ReasoningLog) SeedCycleNumbering(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > l.cycleNo {
		l.cycleNo = n
	}
}

// BeginCycle opens a new numbered cycle keyed to the intent and stamped now,
// and records the intent itself as the cycle's first record -- so the
// persisted trail carries what was asked, not just how it was handled.
func (l *ReasoningLog) BeginCycle(intent Intent) {
	l.mu.Lock()
	l.cycleNo++
	l.Cycles = append(l.Cycles, TrailCycle{IntentText: intent.Text, Started: time.Now()})
	l.current = &l.Cycles[len(l.Cycles)-1]
	l.mu.Unlock()
	l.Record(Record{
		Stage:  "intent",
		Inputs: map[string]any{"context": intent.Context},
		Output: intent.Text,
	})
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
// none is active, so records are never lost), streams it to Sink and hands it
// to the persisted Trail.
func (l *ReasoningLog) Record(r Record) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current == nil {
		l.cycleNo++
		l.Cycles = append(l.Cycles, TrailCycle{})
		l.current = &l.Cycles[len(l.Cycles)-1]
	}
	if r.Cycle == 0 {
		r.Cycle = l.cycleNo
	}
	if r.Model == "" {
		r.Model = "deterministic-fallback"
	}
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	// Link this record into the tamper-evident chain before it is stored or
	// streamed, so the persisted trail carries its own proof.
	if l.chainTip == "" {
		l.chainTip = genesis
	}
	l.chainTip = chainLink(l.chainTip, recordPayload(r))
	r.Chain = l.chainTip
	l.current.Records = append(l.current.Records, r)
	if l.Sink != nil {
		if l.enc == nil {
			l.enc = json.NewEncoder(l.Sink)
		}
		// Best-effort streaming: a sink write failure must never crash a
		// reasoning cycle, and the record is retained in memory regardless.
		_ = l.enc.Encode(r)
	}
	if l.Trail != nil {
		// Equally best-effort; the TrailFile keeps its own chain over what it
		// persists, continued across sessions.
		_ = l.Trail.WriteRecord(r)
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

// Verify proves the in-memory trail unbroken, or names the first record
// whose link fails to reproduce -- so any edit, insertion or deletion of a
// record surfaces as the exact point the chain first breaks. Returns
// (true, "<n> records verified") when intact.
func (l *ReasoningLog) Verify() (bool, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	tip := genesis
	n := 0
	for i := range l.Cycles {
		for _, rec := range l.Cycles[i].Records {
			n++
			expected := chainLink(tip, recordPayload(rec))
			if rec.Chain != expected {
				return false, fmt.Sprintf("chain broken at record %d (stage %q)", n, rec.Stage)
			}
			tip = expected
		}
	}
	return true, fmt.Sprintf("%d records verified", n)
}

// UsageReport renders the operational ledger from the trail: one row per
// cycle -- model calls, in+out tokens, dollar cost (when Pricing is declared)
// and latency -- with a totals row. A markdown document, like every other
// EAR artifact. Cost shows "—" for a cycle (or the total) that had no
// declared pricing.
func (l *ReasoningLog) UsageReport() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	var b strings.Builder
	b.WriteString("# Usage Report\n\n")
	b.WriteString("| Cycle | Model calls | In+Out tokens | Cost | Latency (ms) |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")

	var totalCalls, totalIn, totalOut int
	var totalLatency int64
	var totalCost float64
	priced := false
	row := 0
	for i := range l.Cycles {
		rec, ok := usageRecord(l.Cycles[i].Records)
		if !ok {
			continue
		}
		row++
		calls := anyInt(rec.Inputs["model_calls"])
		in := anyInt(rec.Inputs["input_tokens"])
		out := anyInt(rec.Inputs["output_tokens"])
		latency := anyInt64(rec.Inputs["latency_ms"])
		costCell := "—"
		if c, has := rec.Inputs["cost"]; has {
			cost := anyFloat(c)
			costCell = fmt.Sprintf("$%.6f", cost)
			totalCost += cost
			priced = true
		}
		totalCalls += calls
		totalIn += in
		totalOut += out
		totalLatency += latency
		b.WriteString(fmt.Sprintf("| %d | %d | %d+%d | %s | %d |\n", row, calls, in, out, costCell, latency))
	}
	totalCostCell := "—"
	if priced {
		totalCostCell = fmt.Sprintf("$%.6f", totalCost)
	}
	b.WriteString(fmt.Sprintf("| **total** | **%d** | **%d+%d** | **%s** | **%d** |\n",
		totalCalls, totalIn, totalOut, totalCostCell, totalLatency))
	return b.String()
}

func usageRecord(records []Record) (Record, bool) {
	for _, r := range records {
		if r.Stage == "usage" {
			return r, true
		}
	}
	return Record{}, false
}

func anyInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func anyInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func anyFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
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
