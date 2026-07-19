package ear

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Research is one cycle's retrieved knowledge: the passages judged relevant,
// the sources they cite, and the rendered block deliberation reads.
type Research struct {
	Passages  []Passage
	Citations []string
	Rendered  string
}

// Librarian retrieves the Knowledge relevant to an intent: BM25 narrowing,
// then (Slice 2) the model's relevance judgment, always on the record. What
// was consulted is first-class evidence -- the citations travel into the
// decision's evidence, and the retrieved text reaches the Reasoner marked as
// reference material.
type Librarian struct {
	Knowledge      *Knowledge
	CandidateLimit int // BM25 narrowing width; default 6
	KeepLimit      int // deterministic pick from the candidates; default 3
}

// Research narrows the corpus to the intent and returns the passages to
// consult, recording the retrieval on the trail. Deterministically it keeps
// the best BM25 candidates; the model's relevance judgment is Slice 2.
// Returns nil when there is no corpus or nothing matches.
func (l *Librarian) Research(ctx context.Context, rt *Runtime, intent Intent) *Research {
	if l == nil || l.Knowledge == nil || l.Knowledge.Len() == 0 {
		return nil
	}
	candidateLimit := l.CandidateLimit
	if candidateLimit <= 0 {
		candidateLimit = 6
	}
	keep := l.KeepLimit
	if keep <= 0 {
		keep = 3
	}

	candidates := l.Knowledge.Candidates(intent.Text, candidateLimit)
	if len(candidates) == 0 {
		return nil
	}

	var chosen []Passage
	var rationale, model string
	if rt.LM != nil {
		// The model judges which of the narrowed candidates a careful analyst
		// would consult -- choosing none is valid. It can only pick from the
		// candidates, never invent a passage.
		chosen, rationale = judgePassagesWithLM(ctx, rt.LM, intent, candidates)
		model = "llm"
	} else {
		chosen = candidates
		if len(chosen) > keep {
			chosen = chosen[:keep]
		}
		rationale = "structural retrieval only (no model bound): best BM25 candidates included"
	}

	research := &Research{
		Passages:  chosen,
		Citations: passageSources(chosen),
		Rendered:  renderPassages(chosen),
	}
	if rt.ReasoningLog != nil {
		output := strings.Join(research.Citations, "; ")
		if output == "" {
			output = "nothing judged relevant"
		}
		rt.ReasoningLog.Record(Record{
			Stage: "retrieval",
			Inputs: map[string]any{
				"intent":     intent.Text,
				"candidates": passageSources(candidates),
				"narrowing":  l.Knowledge.Narrowing(),
			},
			Output:    output,
			Rationale: rationale,
			Model:     model,
		})
	}
	return research
}

// judgePassagesWithLM asks the model which candidates to consult. On an
// unusable answer or an LM error it falls back to the best three candidates
// -- retrieval is informative, never a gate, so it must not fail the cycle.
func judgePassagesWithLM(ctx context.Context, lm LM, intent Intent, candidates []Passage) ([]Passage, string) {
	numbered := make([]string, len(candidates))
	for i, p := range candidates {
		numbered[i] = fmt.Sprintf("%d. %s", i+1, p.Render())
	}
	out, err := SelectRelevantPassages.Run(ctx, lm, SelectPassagesIn{
		IntentText: intent.Text,
		Passages:   strings.Join(numbered, "\n\n"),
	})
	if err != nil {
		fallback := candidates
		if len(fallback) > 3 {
			fallback = fallback[:3]
		}
		return fallback, "retrieval judging failed; best BM25 candidates included"
	}
	var chosen []Passage
	seen := map[int]bool{}
	for _, num := range out.RelevantNumbers {
		n, err := strconv.Atoi(strings.TrimSpace(num))
		if err != nil {
			continue
		}
		idx := n - 1
		if idx >= 0 && idx < len(candidates) && !seen[idx] {
			seen[idx] = true
			chosen = append(chosen, candidates[idx])
		}
	}
	return chosen, out.Rationale
}

func passageSources(passages []Passage) []string {
	sources := make([]string, len(passages))
	for i, p := range passages {
		sources[i] = p.Source
	}
	return sources
}

func renderPassages(passages []Passage) string {
	rendered := make([]string, len(passages))
	for i, p := range passages {
		rendered[i] = p.Render()
	}
	return strings.Join(rendered, "\n\n")
}
