package ear

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Monitor -- one runtime's health and progress, distilled from the reasoning
// trail, and a fleet view over many.
//
// The Dashboard is the board you open in a browser; the Monitor is the wall of
// screens in the control room. Both read the same thing, because health has to
// have one definition: this file computes it, and the two views only render it.
// A number that appears in one and not the other is a number nobody can act on.
//
// Nothing here judges. The Monitor reports what the trail already recorded --
// it is an instrument, not another opinion about whether the fleet is well.

// Health classifies a runtime in one word.
type Health string

const (
	// Healthy -- nothing needs a human. Policy blocks live here: governance
	// refusing something is the system working, not a fault.
	Healthy Health = "healthy"
	// Attention -- something is waiting on a person, or a cycle failed.
	Attention Health = "attention"
	// Broken -- the audit chain does not verify. The only hard fault, because
	// it is the only one that means the record itself cannot be trusted.
	Broken Health = "broken"
)

// Rank orders health for sorting a fleet worst-first: an operator should not
// have to hunt for the instance that needs them.
func (h Health) Rank() int {
	switch h {
	case Broken:
		return 2
	case Attention:
		return 1
	default:
		return 0
	}
}

// Freshness is the heartbeat that tells a live runtime from a quiet or dead
// one.
type Freshness string

const (
	Active Freshness = "active"
	Idle   Freshness = "idle"
	Stale  Freshness = "stale"
)

// Freshness thresholds, from the age of the last recorded activity.
const (
	FreshActiveWithin = 90 * time.Second
	FreshStaleAfter   = 30 * time.Minute
)

// InstanceHealth is one runtime's health and progress.
type InstanceHealth struct {
	Name   string
	Status Health
	Reason string

	Cycles  int
	Calls   int
	Tokens  int
	Dollars float64
	Latency time.Duration

	// Blocked counts governance stops -- surfaced as a count, never a fault.
	Blocked int
	// Pending counts cycles waiting on a human.
	Pending int
	// Failed counts cycles that ended in a fault. The trail alone cannot show
	// this -- the Python package reads it from a `retry` stage that belongs to
	// the unported journey plane -- so it is folded in from the Kernel's own
	// dispatch history, which is where this port actually records a failed
	// run. Zero for a runtime inspected outside a kernel.
	Failed int

	// ChainIntact reports whether the hash chain verifies, and ChainDetail
	// says where it first breaks if it does not.
	ChainIntact bool
	ChainDetail string

	Last      time.Time
	Freshness Freshness

	// Spark is per-cycle token counts, for a sparkline.
	Spark []int
}

// FleetHealth is the whole control room in one value.
type FleetHealth struct {
	Instances []InstanceHealth
	Cycles    int
	Tokens    int
	Dollars   float64
	Blocked   int
	Pending   int
	Failed    int
	Broken    int
	At        time.Time
}

// Status is the worst status across the fleet: the fleet is only as well as
// its least well instance.
func (f FleetHealth) Status() Health {
	worst := Healthy
	for _, instance := range f.Instances {
		if instance.Status.Rank() > worst.Rank() {
			worst = instance.Status
		}
	}
	return worst
}

// InspectRuntime distils one runtime's trail into its health.
func InspectRuntime(name string, r *Runtime, now time.Time) InstanceHealth {
	if now.IsZero() {
		now = time.Now()
	}
	health := InstanceHealth{Name: name, ChainIntact: true}

	perCycle := map[int]int{}
	var cycles []int
	seen := map[int]bool{}

	for record := range r.ReasoningLog.Records() {
		if !seen[record.Cycle] {
			seen[record.Cycle] = true
			cycles = append(cycles, record.Cycle)
		}
		if record.Time.After(health.Last) {
			health.Last = record.Time
		}

		// The governance stages write a controlled vocabulary, not prose, so
		// these are exact markers rather than keyword guesses. Matching on
		// prose would let a deliberation that happened to say "violated"
		// register as a policy block.
		switch record.Stage {
		case "usage":
			health.Calls += intField(record.Inputs, "model_calls")
			tokens := intField(record.Inputs, "input_tokens") + intField(record.Inputs, "output_tokens")
			health.Tokens += tokens
			perCycle[record.Cycle] += tokens
			health.Latency += time.Duration(intField(record.Inputs, "latency_ms")) * time.Millisecond
			if cost, ok := record.Inputs["cost"].(float64); ok {
				health.Dollars += cost
			}
		case "policy":
			// govern() writes exactly one of: complies / VIOLATED /
			// PENDING APPROVAL.
			switch {
			case strings.HasPrefix(record.Output, "VIOLATED"):
				health.Blocked++
			case strings.HasPrefix(record.Output, "PENDING"):
				health.Pending++
			}
		case "approval":
			// govern() writes REJECTED/REFUSED/approved once a verdict is in.
			// enforce() also writes an "approval" PENDING summary, but a parked
			// gate already registered "PENDING APPROVAL" on its `policy`
			// record above -- counting the summary too would double every
			// pending gate, so pending is not counted here.
			switch {
			case strings.HasPrefix(record.Output, "REJECTED"), strings.HasPrefix(record.Output, "REFUSED"):
				health.Blocked++
			}
		case "tenant":
			if strings.HasPrefix(record.Output, "REFUSED") {
				health.Blocked++
			}
		}
	}

	health.Cycles = len(cycles)
	sort.Ints(cycles)
	for _, cycle := range cycles {
		health.Spark = append(health.Spark, perCycle[cycle])
	}
	if len(health.Spark) == 0 {
		health.Spark = []int{0}
	}

	health.ChainIntact, health.ChainDetail = r.ReasoningLog.Verify()
	health.Freshness = freshnessOf(health.Last, now)
	health.Status, health.Reason = classifyHealth(health)
	return health
}

// classifyHealth is a runtime's health in one word.
//
// A broken chain is the only hard fault, because it is the only condition
// under which the record itself cannot be trusted. Failures and pending
// approvals need a human. Policy blocks stay healthy: governance refusing
// something is the system working, and reporting it as a fault would train
// operators to ignore the one signal that means "a human must act".
func classifyHealth(h InstanceHealth) (Health, string) {
	switch {
	case !h.ChainIntact:
		detail := h.ChainDetail
		if detail == "" {
			detail = "audit trail chain is broken"
		}
		return Broken, detail
	case h.Failed > 0:
		return Attention, fmt.Sprintf("%d failed cycle(s)", h.Failed)
	case h.Pending > 0:
		return Attention, fmt.Sprintf("%d awaiting approval / escalated", h.Pending)
	case h.Blocked > 0:
		return Healthy, fmt.Sprintf("%d policy block(s) -- governance working", h.Blocked)
	}
	return Healthy, "all clear"
}

func freshnessOf(last, now time.Time) Freshness {
	if last.IsZero() {
		return Idle
	}
	age := now.Sub(last)
	switch {
	case age <= FreshActiveWithin:
		return Active
	case age >= FreshStaleAfter:
		return Stale
	}
	return Idle
}

func intField(inputs map[string]any, key string) int {
	switch v := inputs[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64: // JSON numbers decode as float64
		return int(v)
	}
	return 0
}

// InspectTrailFile distils one persisted JSONL trail into its health.
//
// A trail read back from disk cannot be integrity-checked with the in-memory
// Verify: that recomputes each link from a re-marshalled struct, whose JSON
// key order need not match the bytes the file actually stored. VerifyTrail is
// the file-canonical verifier -- it reproduces the stored bytes -- so the
// chain result comes from there, and the trail-read InstanceHealth is
// re-classified against it. Using the wrong verifier reported every read-back
// trail as broken; this is what makes the file monitor trustworthy.
func InspectTrailFile(name, path string, now time.Time) (InstanceHealth, error) {
	log, err := ReadTrail(path)
	if err != nil {
		return InstanceHealth{}, err
	}
	runtime := NewRuntime(name)
	runtime.ReasoningLog = log
	health := InspectRuntime(name, runtime, now)

	// Override the (unreliable, for a read-back log) in-memory chain result
	// with the file-canonical one, then re-classify so a genuinely broken
	// file still reads broken.
	health.ChainIntact, health.ChainDetail = VerifyTrail(path)
	health.Status, health.Reason = classifyHealth(health)
	return health, nil
}

// InspectFleet distils every registered instance, worst-first so the one
// needing attention is at the top.
//
// Dispatch failures come from the kernel rather than the trail: a cycle that
// panicked or errored never got to write a record saying so, which is exactly
// why the scheduler keeps its own history.
func InspectFleet(kernel *Kernel, now time.Time) FleetHealth {
	if now.IsZero() {
		now = time.Now()
	}
	failures := map[string]int{}
	for _, dispatch := range kernel.History() {
		if dispatch.Status == StatusFailed {
			failures[dispatch.Instance]++
		}
	}

	fleet := FleetHealth{At: now}
	for _, name := range kernel.Instances() {
		runtime, ok := kernel.Instance(name)
		if !ok {
			continue
		}
		instance := InspectRuntime(name, runtime, now)
		if failed := failures[name]; failed > 0 {
			// Re-classify: a failure outranks the healthy verdict the trail
			// alone would have produced.
			instance.Failed = failed
			instance.Status, instance.Reason = classifyHealth(instance)
		}
		fleet.Add(instance)
	}
	fleet.SortWorstFirst()
	return fleet
}

// Add folds one instance's health into the fleet totals.
func (f *FleetHealth) Add(instance InstanceHealth) {
	f.Instances = append(f.Instances, instance)
	f.Cycles += instance.Cycles
	f.Tokens += instance.Tokens
	f.Dollars += instance.Dollars
	f.Blocked += instance.Blocked
	f.Pending += instance.Pending
	f.Failed += instance.Failed
	if instance.Status == Broken {
		f.Broken++
	}
}

// SortWorstFirst orders the fleet so the instance needing attention is at the
// top -- an operator should not have to hunt for it.
func (f *FleetHealth) SortWorstFirst() {
	sort.SliceStable(f.Instances, func(i, j int) bool {
		a, b := f.Instances[i], f.Instances[j]
		if a.Status.Rank() != b.Status.Rank() {
			return a.Status.Rank() > b.Status.Rank()
		}
		return a.Name < b.Name
	})
}

// -- the terminal view --------------------------------------------------------

// sparkGlyphs are the eight block heights a sparkline is drawn from.
var sparkGlyphs = []rune("▁▂▃▄▅▆▇█")

// Sparkline renders a series as block glyphs, scaled to its own maximum. An
// all-zero series is drawn flat rather than full, so "no activity" and "steady
// activity" never look the same.
func Sparkline(values []int, width int) string {
	if len(values) == 0 || width <= 0 {
		return ""
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	peak := 0
	for _, v := range values {
		if v > peak {
			peak = v
		}
	}
	var out strings.Builder
	for _, v := range values {
		if peak <= 0 {
			out.WriteRune(sparkGlyphs[0])
			continue
		}
		index := (v * (len(sparkGlyphs) - 1)) / peak
		out.WriteRune(sparkGlyphs[index])
	}
	return out.String()
}

// RenderFleet draws one frame of the control room as a string, so it is
// testable without a terminal and pipeable into a file.
func RenderFleet(fleet FleetHealth) string {
	var out strings.Builder

	fmt.Fprintf(&out, "EAR FLEET  %s  %s\n",
		fleet.At.Format("2006-01-02 15:04:05"), strings.ToUpper(string(fleet.Status())))
	out.WriteString(strings.Repeat("─", 78) + "\n")
	fmt.Fprintf(&out, "%d instance(s)   %d cycles   %d tokens   $%.4f   %d blocked   %d pending   %d failed\n\n",
		len(fleet.Instances), fleet.Cycles, fleet.Tokens, fleet.Dollars,
		fleet.Blocked, fleet.Pending, fleet.Failed)

	if len(fleet.Instances) == 0 {
		out.WriteString("  (no instances registered)\n")
		return out.String()
	}

	fmt.Fprintf(&out, "  %-20s %-10s %-8s %7s %9s %-10s %s\n",
		"INSTANCE", "STATUS", "FRESH", "CYCLES", "TOKENS", "ACTIVITY", "REASON")
	for _, instance := range fleet.Instances {
		fmt.Fprintf(&out, "  %-20s %-10s %-8s %7d %9d %-10s %s\n",
			truncate(instance.Name, 20),
			instance.Status,
			instance.Freshness,
			instance.Cycles,
			instance.Tokens,
			Sparkline(instance.Spark, 10),
			truncate(instance.Reason, 30),
		)
	}
	return out.String()
}
