package ear

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// TrailFile is the persisted reasoning trail: every record appended to one
// file, declared in memory.md's `## Reasoning Audit Trail` section and wired
// by the loader as the ReasoningLog's Trail.
//
// The path's extension picks the codec. `.md` -- the system-native default --
// appends each record as a readable markdown block under a `## Cycle N`
// heading, with the tamper-evident chain hash riding an HTML comment
// (invisible in a rendered view, never colliding with content). Any other
// extension appends JSON lines -- the machine record, losslessly readable
// back with ReadTrail. Both are self-verifying: the file carries its own
// hash chain, seeded at genesis and continued across sessions by Resume, so
// VerifyTrail proves the file unbroken from its own bytes alone.
//
// The file is append-only: retention (`Rotate`) bounds the in-memory window
// the runtime reasons over, while the file stays the complete archive.
type TrailFile struct {
	Path string

	file      *os.File
	asMd      bool
	tip       string // chain tip over what THIS FILE persists
	lastCycle int    // last cycle a markdown heading was written for
	maxCycle  int    // highest cycle number seen in the file (for numbering resume)
}

var (
	chainMarkerRe  = regexp.MustCompile(`<!-- chain: ([0-9a-f]+) -->`)
	cycleHeadingRe = regexp.MustCompile(`(?m)^## Cycle (\d+)`)
)

// OpenTrailFile opens (creating parents as needed) the trail file for
// appending, resuming the hash chain and cycle numbering from whatever the
// file already holds -- so a new session links its first record to the last
// persisted one and never repeats a cycle number.
func OpenTrailFile(path string) (*TrailFile, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating the trail directory: %w", err)
		}
	}
	t := &TrailFile{Path: path, asMd: strings.HasSuffix(path, ".md"), tip: genesis}
	t.resume()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening the trail file: %w", err)
	}
	t.file = file
	return t, nil
}

// MaxCycle is the highest cycle number the existing file held when opened;
// the loader seeds the ReasoningLog's numbering from it.
func (t *TrailFile) MaxCycle() int { return t.maxCycle }

// Close releases the underlying file. Safe on a nil receiver.
func (t *TrailFile) Close() error {
	if t == nil || t.file == nil {
		return nil
	}
	err := t.file.Close()
	t.file = nil
	return err
}

// resume reads the existing file (if any) for the last chain marker and the
// highest cycle number. A missing or unreadable file leaves the defaults --
// a fresh chain from genesis, numbering from zero.
func (t *TrailFile) resume() {
	data, err := os.ReadFile(t.Path)
	if err != nil {
		return
	}
	text := string(data)
	if t.asMd {
		for _, m := range cycleHeadingRe.FindAllStringSubmatch(text, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil && n > t.maxCycle {
				t.maxCycle = n
			}
		}
		if markers := chainMarkerRe.FindAllStringSubmatch(text, -1); len(markers) > 0 {
			t.tip = markers[len(markers)-1][1]
		}
		t.lastCycle = t.maxCycle
		return
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if n := anyInt(record["cycle"]); n > t.maxCycle {
			t.maxCycle = n
		}
		if chain, _ := record["chain"].(string); chain != "" {
			t.tip = chain
		}
	}
}

// WriteRecord appends one record in the file's codec, linking it into the
// file's own chain. This implements RecordWriter.
func (t *TrailFile) WriteRecord(r Record) error {
	if t.file == nil {
		return fmt.Errorf("trail file %q is closed", t.Path)
	}
	if t.asMd {
		return t.writeMarkdown(r)
	}
	return t.writeJSONL(r)
}

// writeJSONL appends the record as one JSON line: the canonical payload (the
// record's fields minus any chain, keys sorted by Go's map marshaling) is
// hashed into the file chain, then written with the resulting link -- so
// verification reproduces the payload from the line itself.
func (t *TrailFile) writeJSONL(r Record) error {
	fields, err := recordFields(r)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	t.tip = chainLink(t.tip, string(payload))
	fields["chain"] = t.tip
	line, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = t.file.Write(append(line, '\n'))
	return err
}

// writeMarkdown appends the record as a readable block -- a `## Cycle N`
// heading when the cycle changes, then the block, then the chain comment.
// The chain covers the block's exact bytes (the heading sits outside it), so
// the rendered view stays honest without the hash cluttering it.
func (t *TrailFile) writeMarkdown(r Record) error {
	var b strings.Builder
	if r.Cycle != t.lastCycle {
		t.lastCycle = r.Cycle
		b.WriteString(fmt.Sprintf("\n## Cycle %d -- %s UTC\n\n", r.Cycle, r.Time.UTC().Format("2006-01-02 15:04:05")))
	}
	payload := recordMarkdown(r)
	t.tip = chainLink(t.tip, payload)
	b.WriteString(payload)
	b.WriteString(fmt.Sprintf("\n<!-- chain: %s -->\n", t.tip))
	_, err := t.file.WriteString(b.String())
	return err
}

// recordFields renders a record as the flat JSON object the JSONL codec
// persists -- via the struct's own marshaling so types and timestamps are
// canonical, minus the in-memory chain link (the file keeps its own).
func recordFields(r Record) (map[string]any, error) {
	r.Chain = ""
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	delete(fields, "chain")
	return fields, nil
}

// recordMarkdown renders one record as its markdown block: one-line inputs
// as bullets (keys sorted for a stable payload), multi-line values as
// blockquotes under their own label, the rationale under "Why:", and a long
// or multi-line output quoted in full under "Output:".
func recordMarkdown(r Record) string {
	header := "### " + r.Stage
	if r.Output != "" {
		header += " -- " + clip(r.Output, 80)
	}
	if r.Model != "" {
		header += "  (" + r.Model + ")"
	}
	lines := []string{header, ""}

	var simple, multiline []string
	for _, key := range sortedKeys(r.Inputs) {
		if strings.Contains(inputValue(r.Inputs[key]), "\n") {
			multiline = append(multiline, key)
		} else {
			simple = append(simple, key)
		}
	}
	if len(simple) > 0 {
		for _, key := range simple {
			lines = append(lines, "- "+key+": "+inputValue(r.Inputs[key]))
		}
		lines = append(lines, "")
	}
	for _, key := range multiline {
		if value := inputValue(r.Inputs[key]); strings.TrimSpace(value) != "" {
			lines = append(lines, capitalize(key)+":", Quote(value), "")
		}
	}
	if r.Rationale != "" {
		lines = append(lines, "Why:", Quote(r.Rationale), "")
	}
	if strings.Contains(r.Output, "\n") || len(r.Output) > 80 {
		lines = append(lines, "Output:", Quote(r.Output), "")
	}
	return strings.Join(lines, "\n")
}

// VerifyTrail proves a persisted trail file unbroken, or names the first
// record whose link fails to reproduce -- recomputing the hash chain over the
// file's own bytes, so any edit, insertion or deletion surfaces as the exact
// point the chain first breaks. Handles both codecs by extension.
func VerifyTrail(path string) (bool, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Sprintf("no trail file at %q", path)
	}
	var links []trailLink
	if strings.HasSuffix(path, ".md") {
		links = chainLinksMd(string(data))
	} else {
		links = chainLinksJSONL(string(data))
	}
	if len(links) == 0 {
		return false, "no chain records found -- this trail carries no integrity hashes"
	}
	tip := genesis
	for i, link := range links {
		expected := chainLink(tip, link.payload)
		if link.stored != expected {
			return false, fmt.Sprintf("broken chain at record %d (%s) -- the trail was altered here or earlier", i+1, link.label)
		}
		tip = expected
	}
	return true, fmt.Sprintf("chain intact over %d records", len(links))
}

// trailLink is one persisted record's verification triple: a label for error
// messages, the exact payload its hash was taken over, and the stored hash.
type trailLink struct {
	label   string
	payload string
	stored  string
}

// chainLinksMd splits a markdown trail into links: each payload is the block
// from its `### ` heading to the line before its chain comment, exactly as
// persisted, so verification reproduces the bytes without reparsing.
func chainLinksMd(text string) []trailLink {
	var links []trailLink
	var block []string
	inBlock := false
	for _, line := range strings.Split(text, "\n") {
		marker := chainMarkerRe.FindStringSubmatch(line)
		switch {
		case strings.HasPrefix(line, "### "):
			block, inBlock = []string{line}, true
		case marker != nil && inBlock:
			label, _, _ := strings.Cut(strings.TrimPrefix(block[0], "### "), " -- ")
			links = append(links, trailLink{
				label:   strings.TrimSpace(label),
				payload: strings.Join(block, "\n"),
				stored:  marker[1],
			})
			block, inBlock = nil, false
		case inBlock:
			block = append(block, line)
		}
	}
	return links
}

// chainLinksJSONL builds the links from a JSONL trail: each payload is the
// line's object minus its chain field, re-marshaled to the same sorted-key
// canonical form the writer hashed.
func chainLinksJSONL(text string) []trailLink {
	var links []trailLink
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := map[string]any{}
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			continue
		}
		stored, _ := fields["chain"].(string)
		if stored == "" {
			continue
		}
		delete(fields, "chain")
		payload, err := json.Marshal(fields)
		if err != nil {
			continue
		}
		label, _ := fields["stage"].(string)
		if label == "" {
			label = "?"
		}
		links = append(links, trailLink{label: label, payload: string(payload), stored: stored})
	}
	return links
}

// ReadTrail reconstructs a ReasoningLog from a persisted JSONL trail --
// lossless, so the trail view and the usage ledger can be built from a
// finished run on disk. The markdown codec is a human view and not fully
// reconstructable; JSONL is the machine record, and this reads it back
// exactly. Records keep the chain values the file stored (verify a file with
// VerifyTrail, which recomputes over the file's own canonical form).
func ReadTrail(path string) (*ReasoningLog, error) {
	if strings.HasSuffix(path, ".md") {
		return nil, fmt.Errorf("%q is a markdown trail -- a human view; only a JSONL trail reads back losslessly", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	byCycle := map[int][]Record{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		byCycle[r.Cycle] = append(byCycle[r.Cycle], r)
	}
	numbers := make([]int, 0, len(byCycle))
	for n := range byCycle {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)

	log := &ReasoningLog{}
	for _, n := range numbers {
		records := byCycle[n]
		cycle := TrailCycle{Records: records}
		for _, r := range records {
			if r.Stage == "intent" {
				cycle.IntentText, cycle.Started = r.Output, r.Time
				break
			}
		}
		if cycle.Started.IsZero() && len(records) > 0 {
			cycle.Started = records[0].Time
		}
		log.Cycles = append(log.Cycles, cycle)
		if n > log.cycleNo {
			log.cycleNo = n
		}
	}
	return log, nil
}

// inputValue renders one input value for the markdown view: maps as sorted
// "key: value" pairs and slices comma-joined, so the readable trail never
// shows Go's raw map[...] formatting.
func inputValue(v any) string {
	switch value := v.(type) {
	case map[string]any:
		pairs := make([]string, 0, len(value))
		for _, key := range sortedKeys(value) {
			pairs = append(pairs, key+": "+inputValue(value[key]))
		}
		return "{" + strings.Join(pairs, ", ") + "}"
	case []string:
		return strings.Join(value, ", ")
	case []any:
		parts := make([]string, len(value))
		for i, item := range value {
			parts[i] = inputValue(item)
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(v)
	}
}

// clip collapses whitespace and truncates to width, ellipsized.
func clip(text string, width int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= width {
		return text
	}
	return text[:width-3] + "..."
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
