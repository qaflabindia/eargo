package ear

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SessionStore is cross-session data: the runtime's Memory, Experience and
// Adaptations persisted to disk, so a new session picks up where the last one
// left off instead of starting cold.
//
// Declared in memory.md under the `## Cross-Session Data` strategy section
// (the loader creates the store and restores from it before the first cycle),
// and written back automatically after every Runtime.Reason cycle.
//
// The file's extension picks the codec. `.md` -- the system-native default --
// writes the session as a readable markdown document and restores it through
// the same Section parser the whole authoring stack uses: entries are
// sections, facts are bullets, and every free-text value (a decision, an
// insight) is blockquoted so it can never be mistaken for structure. A
// `.json` path keeps the plain-JSON codec for machine pipelines. Neither
// holds code, and Evidence's "why" travels as its basis sentence.
type SessionStore struct {
	Path string
}

// Save writes the runtime's memory layers to the store, creating the parent
// directory as needed, and returns the path written.
func (s *SessionStore) Save(rt *Runtime) (string, error) {
	if dir := filepath.Dir(s.Path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	var text string
	if strings.HasSuffix(s.Path, ".md") {
		text = s.renderMarkdown(rt)
	} else {
		data, err := json.MarshalIndent(s.payload(rt), "", "  ")
		if err != nil {
			return "", err
		}
		text = string(data)
	}
	if err := os.WriteFile(s.Path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return s.Path, nil
}

// Restore loads persisted layers back into the runtime. It returns false (and
// leaves the runtime untouched) when there is nothing usable to load, so a
// missing or corrupt store never blocks a session from starting.
func (s *SessionStore) Restore(rt *Runtime) bool {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return false
	}
	if strings.HasSuffix(s.Path, ".md") {
		s.restoreMarkdown(rt, string(data))
		return true
	}
	var payload sessionPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return false
	}
	s.restoreJSON(rt, payload)
	return true
}

// -- the markdown codec (system-native) -------------------------------------

func (s *SessionStore) renderMarkdown(rt *Runtime) string {
	var lines []string
	lines = append(lines, "# Session -- "+rt.Name, "")
	lines = append(lines, "Saved at: "+time.Now().UTC().Format(time.RFC3339Nano), "")

	for i, summary := range rt.Memory.Compressed {
		lines = append(lines, fmt.Sprintf("## Compressed %d", i+1), "", Quote(summary), "")
	}

	for i, entry := range rt.Memory.Working {
		lines = append(lines, fmt.Sprintf("## Entry %d", i+1), "")
		lines = append(lines, "Timestamp: "+entry.Timestamp.Format(time.RFC3339Nano))
		if entry.Evidence != nil {
			lines = append(lines,
				"Evidence basis: "+entry.Evidence.Basis,
				"Evidence confidence: "+strconv.FormatFloat(entry.Evidence.Confidence, 'g', -1, 64),
			)
		}
		lines = append(lines, "", "Intent:", Quote(entry.IntentText), "")
		lines = append(lines, "Decision:", Quote(fmt.Sprint(entry.Decision)), "")
		if len(entry.Context) > 0 {
			lines = append(lines, "Context:", "")
			for _, key := range sortedKeys(entry.Context) {
				lines = append(lines, fmt.Sprintf("- %s: %v", key, entry.Context[key]))
			}
			lines = append(lines, "")
		}
	}

	lines = append(lines, "## Experience", "", fmt.Sprintf("Observations: %d", rt.Experience.Observations), "")
	for _, decision := range sortedKeys(rt.Experience.DecisionCounts) {
		lines = append(lines,
			"### Observed decision", "",
			fmt.Sprintf("Count: %d", rt.Experience.DecisionCounts[decision]), "",
			Quote(decision), "",
		)
	}

	for _, a := range rt.Adaptations.Impressions {
		lines = append(lines,
			"## Adaptation -- "+a.Name, "",
			"Confidence: "+strconv.FormatFloat(a.Confidence, 'g', -1, 64),
			fmt.Sprintf("Evidence count: %d", a.EvidenceCount), "",
			Quote(a.Insight), "",
		)
	}
	return strings.Join(lines, "\n")
}

func (s *SessionStore) restoreMarkdown(rt *Runtime, text string) {
	doc := ParseDocument(text)
	var working []MemoryEntry
	var compressed []string
	observations := 0
	decisionCounts := map[string]int{}
	var adaptations []*Adaptation

	for _, section := range doc.Sections {
		key := Normalize(section.Name)
		switch {
		case strings.HasPrefix(key, "compressed"):
			compressed = append(compressed, Unquote(section.Lines))
		case strings.HasPrefix(key, "entry"):
			working = append(working, entryFromSection(section))
		case key == "experience":
			observations = atoiOr(section.StructuredBody("observations").Field("observations"), 0)
		case strings.HasPrefix(key, "observed"):
			count := atoiOr(section.StructuredBody("count").Field("count"), 1)
			decisionCounts[Unquote(section.Lines)] = count
		case strings.HasPrefix(key, "adaptation"):
			body := section.StructuredBody("confidence", "evidence count")
			name := section.Name
			if _, after, ok := strings.Cut(section.Name, "--"); ok {
				if trimmed := strings.TrimSpace(after); trimmed != "" {
					name = trimmed
				}
			}
			adaptations = append(adaptations, &Adaptation{
				Name:          name,
				Insight:       Unquote(section.Lines),
				Confidence:    atofOr(body.Field("confidence"), 1.0),
				EvidenceCount: atoiOr(body.Field("evidence count"), 0),
			})
		}
	}

	rt.Memory.Compressed = compressed
	rt.Memory.Working = working
	rt.Experience.Observations = observations
	rt.Experience.DecisionCounts = decisionCounts
	rt.Adaptations.Impressions = adaptations
}

// entryFromSection reads one `## Entry N` section back into a MemoryEntry:
// its timestamp and evidence from recognized fields, its intent and decision
// from the blockquoted labels, and its context from the `- key: value`
// bullets (coerced back to typed values).
func entryFromSection(section Section) MemoryEntry {
	body := section.StructuredBody("timestamp", "evidence basis", "evidence confidence")
	var evidence *Evidence
	if basis := body.Field("evidence basis"); basis != "" {
		evidence = &Evidence{
			Basis:      basis,
			Sources:    map[string]any{},
			Confidence: atofOr(body.Field("evidence confidence"), 1.0),
		}
	}
	timestamp, err := parseTimestamp(body.Field("timestamp"))
	if err != nil {
		timestamp = time.Now().UTC()
	}
	blocks := labelledBlocks(section.Lines)
	context := map[string]any{}
	for _, bullet := range body.Bullets {
		key, value, ok := strings.Cut(bullet, ": ")
		if !ok {
			key, value, ok = strings.Cut(bullet, ":")
		}
		if ok {
			context[strings.TrimSpace(key)] = Coerce(value)
		}
	}
	return MemoryEntry{
		IntentText: blocks["intent"],
		Decision:   blocks["decision"],
		Context:    context,
		Evidence:   evidence,
		Timestamp:  timestamp,
	}
}

// -- the JSON codec (for machine pipelines) ---------------------------------

type sessionPayload struct {
	Runtime string `json:"runtime"`
	SavedAt string `json:"saved_at"`
	Memory  struct {
		Capacity   int      `json:"capacity"`
		Compressed []string `json:"compressed"`
		Working    []struct {
			IntentText         string         `json:"intent_text"`
			Decision           string         `json:"decision"`
			Context            map[string]any `json:"context"`
			Timestamp          string         `json:"timestamp"`
			EvidenceBasis      string         `json:"evidence_basis"`
			EvidenceConfidence float64        `json:"evidence_confidence"`
		} `json:"working"`
	} `json:"memory"`
	Experience struct {
		Observations   int            `json:"observations"`
		DecisionCounts map[string]int `json:"decision_counts"`
	} `json:"experience"`
	Adaptations []struct {
		Name          string  `json:"name"`
		Insight       string  `json:"insight"`
		Confidence    float64 `json:"confidence"`
		EvidenceCount int     `json:"evidence_count"`
	} `json:"adaptations"`
}

func (s *SessionStore) payload(rt *Runtime) sessionPayload {
	var p sessionPayload
	p.Runtime = rt.Name
	p.SavedAt = time.Now().UTC().Format(time.RFC3339Nano)
	p.Memory.Capacity = rt.Memory.Capacity
	p.Memory.Compressed = append([]string{}, rt.Memory.Compressed...)
	for _, entry := range rt.Memory.Working {
		basis, confidence := "", 1.0
		if entry.Evidence != nil {
			basis, confidence = entry.Evidence.Basis, entry.Evidence.Confidence
		}
		p.Memory.Working = append(p.Memory.Working, struct {
			IntentText         string         `json:"intent_text"`
			Decision           string         `json:"decision"`
			Context            map[string]any `json:"context"`
			Timestamp          string         `json:"timestamp"`
			EvidenceBasis      string         `json:"evidence_basis"`
			EvidenceConfidence float64        `json:"evidence_confidence"`
		}{
			IntentText:         entry.IntentText,
			Decision:           fmt.Sprint(entry.Decision),
			Context:            entry.Context,
			Timestamp:          entry.Timestamp.Format(time.RFC3339Nano),
			EvidenceBasis:      basis,
			EvidenceConfidence: confidence,
		})
	}
	p.Experience.Observations = rt.Experience.Observations
	p.Experience.DecisionCounts = map[string]int{}
	for k, v := range rt.Experience.DecisionCounts {
		p.Experience.DecisionCounts[k] = v
	}
	for _, a := range rt.Adaptations.Impressions {
		p.Adaptations = append(p.Adaptations, struct {
			Name          string  `json:"name"`
			Insight       string  `json:"insight"`
			Confidence    float64 `json:"confidence"`
			EvidenceCount int     `json:"evidence_count"`
		}{a.Name, a.Insight, a.Confidence, a.EvidenceCount})
	}
	return p
}

func (s *SessionStore) restoreJSON(rt *Runtime, p sessionPayload) {
	rt.Memory.Compressed = append([]string{}, p.Memory.Compressed...)
	working := make([]MemoryEntry, 0, len(p.Memory.Working))
	for _, record := range p.Memory.Working {
		var evidence *Evidence
		if record.EvidenceBasis != "" {
			confidence := record.EvidenceConfidence
			if confidence == 0 {
				confidence = 1.0
			}
			evidence = &Evidence{Basis: record.EvidenceBasis, Sources: map[string]any{}, Confidence: confidence}
		}
		timestamp, err := parseTimestamp(record.Timestamp)
		if err != nil {
			timestamp = time.Now().UTC()
		}
		context := record.Context
		if context == nil {
			context = map[string]any{}
		}
		working = append(working, MemoryEntry{
			IntentText: record.IntentText,
			Decision:   record.Decision,
			Context:    context,
			Evidence:   evidence,
			Timestamp:  timestamp,
		})
	}
	rt.Memory.Working = working

	rt.Experience.Observations = p.Experience.Observations
	rt.Experience.DecisionCounts = map[string]int{}
	for k, v := range p.Experience.DecisionCounts {
		rt.Experience.DecisionCounts[k] = v
	}

	rt.Adaptations.Impressions = nil
	for _, record := range p.Adaptations {
		confidence := record.Confidence
		if confidence == 0 {
			confidence = 1.0
		}
		rt.Adaptations.Impressions = append(rt.Adaptations.Impressions, &Adaptation{
			Name:          record.Name,
			Insight:       record.Insight,
			Confidence:    confidence,
			EvidenceCount: record.EvidenceCount,
		})
	}
}

// -- small parsing helpers --------------------------------------------------

// parseTimestamp reads a stored timestamp, tolerating the RFC3339 variants the
// codecs emit across machines.
func parseTimestamp(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable timestamp %q", value)
}

func atoiOr(value string, fallback int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
		return n
	}
	return fallback
}

func atofOr(value string, fallback float64) float64 {
	if f, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
		return f
	}
	return fallback
}
