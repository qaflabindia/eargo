// Package ear is a Go port of EAR -- the Enterprise Agentic Runtime.
//
// Prompts are stacked inside skills, skills inside a persona, a persona
// into a workflow, workflows into processes, policies map onto processes,
// processes are orchestrated by the runtime, and the runtime reasons. The
// whole stack can be authored in plain-English markdown and assembled by
// LoadRuntime.
//
// This port covers EAR's deterministic spine: the shared markdown parser,
// the safe expression evaluator, the full data model, every pipeline stage
// with its deterministic (no-LLM) fallback, the per-cycle Runtime pipeline,
// and the markdown loader. Judgment stages that reason against a live LLM
// in the Python package fall back here to the same deterministic behaviour
// the Python package uses when no model is bound, so the runtime stays
// fully usable and testable with no provider configured.
package ear

import (
	"regexp"
	"strconv"
	"strings"
)

// This file is the one structural markdown parser every stacked document
// shares. It splits a document into a title, a preamble and named
// Sections, and lets each loader pull out only the field lines it knows
// about -- every other line stays natural-language prose, never swallowed
// by accident. Parsing here is structural, not judgmental.

var (
	headingRe  = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	bulletRe   = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)
	numberedRe = regexp.MustCompile(`^\s*\d+[.)]\s+(.*)$`)
	foldRe     = regexp.MustCompile(`[\s_-]+`)
)

// Normalize folds case, whitespace, hyphens and underscores so authors can
// refer to "Credit Risk Guru", "credit-risk-guru" or "credit_risk_guru"
// interchangeably.
func Normalize(text string) string {
	return foldRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(text)), " ")
}

// Coerce reads a markdown value back as the plain type it looks like --
// numbers as numbers, yes/no as booleans, everything else verbatim -- so
// facts written as "- loan_amount: 18500" reach policies and reasoning as
// values, not strings. Numbers are returned as float64 (like JSON), which
// keeps arithmetic in the safe evaluator uniform.
func Coerce(text string) any {
	value := strings.TrimSpace(text)
	switch strings.ToLower(value) {
	case "true", "yes":
		return true
	case "false", "no":
		return false
	}
	if i, err := strconv.ParseInt(value, 10, 64); err == nil {
		return float64(i)
	}
	if f, err := strconv.ParseFloat(value, 64); err == nil {
		return f
	}
	return value
}

// Body is a Section's content, structured: recognized "Key: value" fields,
// bullets, numbered items, and everything else kept verbatim as prose.
type Body struct {
	Fields   map[string]string
	Bullets  []string
	Numbered []string
	Prose    string
}

// Field returns the first recognized field value among the given names.
func (b Body) Field(names ...string) string {
	for _, name := range names {
		if value := b.Fields[Normalize(name)]; value != "" {
			return value
		}
	}
	return ""
}

// Section is one heading and the lines beneath it, up to the next heading.
type Section struct {
	Name  string
	Lines []string
}

// StructuredBody structures a section's lines. Only lines whose key appears
// in fieldKeys become fields -- a colon inside ordinary prose (or an
// unknown key) stays part of the prose, so nothing an author writes is
// silently dropped. Wrapped bullets and numbered items fold their indented
// continuations into the item above, exactly as markdown reads.
func (s Section) StructuredBody(fieldKeys ...string) Body {
	keys := map[string]bool{}
	for _, k := range fieldKeys {
		keys[Normalize(k)] = true
	}
	body := Body{Fields: map[string]string{}}
	var proseLines []string
	// openItem points at the list a wrapped line continues (bullets or
	// numbered); nil once a blank/flush-left line ends the item.
	var openItem *[]string
	for _, line := range s.Lines {
		if strings.TrimSpace(line) == "" {
			proseLines = append(proseLines, "")
			openItem = nil
			continue
		}
		if m := bulletRe.FindStringSubmatch(line); m != nil {
			body.Bullets = append(body.Bullets, strings.TrimSpace(m[1]))
			openItem = &body.Bullets
			continue
		}
		if m := numberedRe.FindStringSubmatch(line); m != nil {
			body.Numbered = append(body.Numbered, strings.TrimSpace(m[1]))
			openItem = &body.Numbered
			continue
		}
		if openItem != nil && len(line) > 0 && isSpace(line[0]) {
			last := len(*openItem) - 1
			(*openItem)[last] = (*openItem)[last] + " " + strings.TrimSpace(line)
			continue
		}
		openItem = nil
		if strings.Contains(line, ":") {
			key, value, _ := strings.Cut(line, ":")
			if keys[Normalize(key)] {
				body.Fields[Normalize(key)] = strings.TrimSpace(value)
				continue
			}
		}
		proseLines = append(proseLines, strings.TrimSpace(line))
	}
	body.Prose = paragraphs(proseLines)
	return body
}

// Document is a parsed markdown file: its "# Title", any prose before the
// first section heading, and the named Sections that follow.
type Document struct {
	Title    string
	Preamble string
	Sections []Section
}

// SectionNamed returns the first section whose name normalizes to name.
func (d Document) SectionNamed(name string) *Section {
	key := Normalize(name)
	for i := range d.Sections {
		if Normalize(d.Sections[i].Name) == key {
			return &d.Sections[i]
		}
	}
	return nil
}

// ParseDocument splits markdown text into a Document of named Sections. The
// first level-1 heading is the document title; every later heading (any
// level) starts a new section.
func ParseDocument(text string) Document {
	doc := Document{}
	var current *Section
	var preamble []string
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	for _, raw := range strings.Split(text, "\n") {
		if m := headingRe.FindStringSubmatch(raw); m != nil {
			level, name := len(m[1]), strings.TrimSpace(m[2])
			if level == 1 && doc.Title == "" && len(doc.Sections) == 0 {
				doc.Title = name
				continue
			}
			doc.Sections = append(doc.Sections, Section{Name: name})
			current = &doc.Sections[len(doc.Sections)-1]
			continue
		}
		if current == nil {
			preamble = append(preamble, strings.TrimSpace(raw))
		} else {
			current.Lines = append(current.Lines, raw)
		}
	}
	doc.Preamble = paragraphs(preamble)
	return doc
}

// Quote renders free text as a markdown blockquote, so multi-line values
// can never be mistaken for document structure.
func Quote(text string) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			out = append(out, ">")
		} else {
			out = append(out, "> "+line)
		}
	}
	return strings.Join(out, "\n")
}

// Unquote reassembles a blockquote written by Quote back into its original
// text: the reading half of the `> ...` idiom.
func Unquote(lines []string) string {
	var recovered []string
	for _, line := range lines {
		stripped := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(stripped, "> "):
			recovered = append(recovered, stripped[2:])
		case stripped == ">":
			recovered = append(recovered, "")
		}
	}
	return strings.Join(recovered, "\n")
}

// labelledBlocks collects `Label:` lines each followed by a blockquote into
// label -> text -- the reading half of the `Label:` then `> ...` idiom Quote
// writes (session entries, decision documents). Labels are normalized; a
// label with no quote beneath it yields nothing. Unlike argumentBlocks, a
// blank or unquoted line ends an open label, because this reads markdown EAR
// itself wrote, where Quote always emits a bare `>` for a blank line.
func labelledBlocks(lines []string) map[string]string {
	blocks := map[string]string{}
	label := ""
	var pending []string
	commit := func() {
		if label != "" && len(pending) > 0 {
			blocks[Normalize(label)] = Unquote(pending)
		}
		label, pending = "", nil
	}
	for _, line := range append(append([]string{}, lines...), "") {
		stripped := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(stripped, ">"):
			if label != "" {
				pending = append(pending, line)
			}
		case isLabel(stripped):
			commit()
			label = stripped[:len(stripped)-1]
		case stripped != "":
			commit()
		default: // a blank line ends an open, filled label
			if label != "" && len(pending) > 0 {
				commit()
			}
		}
	}
	commit()
	return blocks
}

// paragraphs joins wrapped lines back into paragraphs, preserving
// blank-line paragraph breaks.
func paragraphs(lines []string) string {
	var paras [][]string
	paras = append(paras, []string{})
	for _, line := range lines {
		if line == "" {
			if len(paras[len(paras)-1]) > 0 {
				paras = append(paras, []string{})
			}
		} else {
			paras[len(paras)-1] = append(paras[len(paras)-1], line)
		}
	}
	var joined []string
	for _, p := range paras {
		if len(p) > 0 {
			joined = append(joined, strings.Join(p, " "))
		}
	}
	return strings.Join(joined, "\n\n")
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' }

// argumentBlocks parses a tool call's / structured field's arguments in
// either of two freely-mixed forms: a short scalar as a one-line bullet
// (`- name: value`), or a value that needs more than one line as a label
// followed by a `>`-quoted block (`name:` then `> ...` lines). Names are
// kept verbatim (only trimmed), never folded, because a field name becomes a
// keyword-like identifier. This backs the Judgment "map" output kind.
func argumentBlocks(lines []string) map[string]string {
	blocks := map[string]string{}
	label := ""
	var pending []string
	commit := func() {
		if label != "" {
			blocks[label] = strings.Trim(strings.Join(pending, "\n"), "\n")
		}
		label, pending = "", nil
	}
	for _, line := range lines {
		if m := bulletRe.FindStringSubmatch(line); m != nil {
			commit()
			name, value, ok := strings.Cut(m[1], ":")
			if ok && strings.TrimSpace(name) != "" {
				blocks[strings.TrimSpace(name)] = strings.TrimSpace(value)
			}
			continue
		}
		stripped := strings.TrimSpace(line)
		if !strings.HasPrefix(stripped, ">") && isLabel(stripped) {
			commit()
			label = stripped[:len(stripped)-1]
			continue
		}
		if label == "" {
			continue
		}
		switch {
		case strings.HasPrefix(stripped, "> "):
			pending = append(pending, stripped[2:])
		case stripped == ">":
			pending = append(pending, "")
		default:
			pending = append(pending, line)
		}
	}
	commit()
	return blocks
}

// isLabel reports whether a line is a short label ending in a bare colon,
// e.g. "Decision:" or "Evidence basis:" -- never a sentence.
func isLabel(stripped string) bool {
	if !strings.HasSuffix(stripped, ":") || len(stripped) > 40 {
		return false
	}
	head := stripped[:len(stripped)-1]
	if head == "" || !isAlpha(rune(head[0])) {
		return false
	}
	for _, ch := range head {
		if !(isAlpha(ch) || (ch >= '0' && ch <= '9') || ch == ' ' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

func isAlpha(ch rune) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}
