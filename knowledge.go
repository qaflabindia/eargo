package ear

import (
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Knowledge is the runtime's reference corpus: passages chunked from the
// declared sources, with BM25 narrowing for the Librarian. Declared in
// memory.md's `## Knowledge` section, so the corpus -- like everything else
// -- is authored, not coded.

// BM25's two standard constants: k1 sets how quickly repeated terms stop
// adding weight; b sets how much a passage's length discounts its score.
const (
	bm25K1 = 1.5
	bm25B  = 0.75
)

// wordsOf lowercases and splits on every non-alphanumeric character -- plain
// string mechanics, no stemming.
func wordsOf(text string) []string {
	var b strings.Builder
	for _, r := range text {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Fields(strings.ToLower(b.String()))
}

func contentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", sum[:])[:12]
}

// KnowledgeSource is one declared source from memory.md's Knowledge section:
// a name and a file path resolved against the stack directory.
type KnowledgeSource struct {
	Name string
	Path string
}

// Passage is one retrievable chunk of a knowledge source: where it came from,
// its text verbatim, and (once indexed) a one-line model-written gist that
// narrowing also scores against.
type Passage struct {
	Source string
	Text   string
	Gist   string
}

// Render renders the passage with its source attribution.
func (p Passage) Render() string { return "[" + p.Source + "]\n" + p.Text }

// Fingerprint is a short stable hash of the passage text.
func (p Passage) Fingerprint() string { return contentHash(p.Text) }

// searchable is what narrowing scores: the passage text, plus the gist when
// the index has one.
func (p Passage) searchable() string {
	if p.Gist != "" {
		return p.Text + "\n" + p.Gist
	}
	return p.Text
}

// Knowledge holds the chunked passages of a corpus.
type Knowledge struct {
	Passages []Passage
}

// Len is the number of passages.
func (k *Knowledge) Len() int { return len(k.Passages) }

// AddDocument chunks one document into passages: markdown by section through
// the shared parser, anything else by paragraph.
func (k *Knowledge) AddDocument(sourceName, filename, text string) *Knowledge {
	if strings.HasSuffix(filename, ".md") {
		doc := ParseDocument(text)
		if doc.Preamble != "" {
			k.Passages = append(k.Passages, Passage{Source: sourceName + " -- " + filename, Text: doc.Preamble})
		}
		for _, section := range doc.Sections {
			body := section.StructuredBody()
			parts := []string{}
			if body.Prose != "" {
				parts = append(parts, body.Prose)
			}
			for _, b := range body.Bullets {
				parts = append(parts, "- "+b)
			}
			parts = append(parts, body.Numbered...)
			if content := strings.Join(parts, "\n"); content != "" {
				k.Passages = append(k.Passages, Passage{
					Source: sourceName + " -- " + filename + " § " + section.Name,
					Text:   content,
				})
			}
		}
	} else {
		for _, paragraph := range strings.Split(text, "\n\n") {
			if cleaned := strings.Join(strings.Fields(paragraph), " "); cleaned != "" {
				k.Passages = append(k.Passages, Passage{Source: sourceName + " -- " + filename, Text: cleaned})
			}
		}
	}
	return k
}

// Candidates returns the structurally best-matching passages for a query by
// BM25 over text (and gist, when indexed). Only passages that actually match
// are returned; when nothing matches, the first passages stand in so the
// caller still sees the corpus rather than an empty room.
func (k *Knowledge) Candidates(query string, limit int) []Passage {
	terms := map[string]bool{}
	for _, w := range wordsOf(query) {
		terms[w] = true
	}
	if len(terms) == 0 || len(k.Passages) == 0 {
		return k.head(limit)
	}
	documents := make([][]string, len(k.Passages))
	counts := make([]map[string]int, len(k.Passages))
	totalLen := 0
	for i, p := range k.Passages {
		documents[i] = wordsOf(p.searchable())
		counts[i] = countWordsIn(documents[i])
		totalLen += len(documents[i])
	}
	total := float64(len(documents))
	avgLen := float64(totalLen) / total
	freq := map[string]float64{}
	for term := range terms {
		n := 0.0
		for _, c := range counts {
			if c[term] > 0 {
				n++
			}
		}
		freq[term] = n
	}

	type scored struct {
		score float64
		idx   int
	}
	var ranked []scored
	for i := range k.Passages {
		saturationFloor := bm25K1 * (1 - bm25B + bm25B*float64(len(documents[i]))/avgLen)
		score := 0.0
		for term := range terms {
			c := float64(counts[i][term])
			if c == 0 {
				continue
			}
			idf := math.Log(1 + (total-freq[term]+0.5)/(freq[term]+0.5))
			score += idf * (c * (bm25K1 + 1)) / (c + saturationFloor)
		}
		if score > 0 {
			ranked = append(ranked, scored{score, i})
		}
	}
	if len(ranked) == 0 {
		return k.head(limit)
	}
	sort.SliceStable(ranked, func(a, b int) bool { return ranked[a].score > ranked[b].score })
	out := make([]Passage, 0, limit)
	for _, r := range ranked {
		if len(out) >= limit {
			break
		}
		out = append(out, k.Passages[r.idx])
	}
	return out
}

// Narrowing reports what narrowing is scoring, for the retrieval record.
func (k *Knowledge) Narrowing() string {
	for _, p := range k.Passages {
		if p.Gist != "" {
			return "BM25 over passage text and index gists"
		}
	}
	return "BM25 over passage text alone (no gist index)"
}

func (k *Knowledge) head(limit int) []Passage {
	if limit > len(k.Passages) {
		limit = len(k.Passages)
	}
	return append([]Passage{}, k.Passages[:limit]...)
}

func countWordsIn(words []string) map[string]int {
	c := map[string]int{}
	for _, w := range words {
		c[w]++
	}
	return c
}
