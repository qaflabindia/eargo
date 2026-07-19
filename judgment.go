package ear

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// judgment is EAR's native structured-prompting engine -- its dependency-free
// replacement for DSPy (the Python package refuses to depend on DSPy,
// LiteLLM or any provider SDK). A Judgment is a declared reasoning task: an
// instruction, the inputs the model is given, and the outputs it must
// return. It renders those into a prompt and parses the reply back into
// typed values using the very same markdown Section codec the whole package
// is built on -- so the model answers in `## Heading` sections, exactly the
// format EAR authors, persists and audits in, and ParseDocument reads back.
//
// A missing or unparseable field degrades to a safe empty value rather than
// erroring, so a stage always gets a well-formed Prediction; the caller's own
// deterministic fallback handles a genuinely unusable answer.

// Kind is an output field's type: how its section is read back.
type Kind string

const (
	KindText Kind = "text" // the section's prose, verbatim
	KindStr  Kind = "str"  // a short one-line value
	KindBool Kind = "bool" // yes/no via the shared coerce codec
	KindList Kind = "list" // the section's bullets (or numbered items)
	KindMap  Kind = "map"  // name->value argument blocks
)

var kindGuidance = map[Kind]string{
	KindText: "the full text of the answer, as prose",
	KindStr:  "a short one-line value",
	KindBool: "answer with a single word: yes or no",
	KindList: "one item per line, each line beginning with '- '",
	KindMap: "one '- name: value' bullet per short argument; for a value that needs more than one line, " +
		"write 'name:' alone on its line followed by the value as a blockquote -- every line, including a " +
		"blank one, starting with '> ' (a lone '>' for a blank line)",
}

// Field is one declared input or output of a Judgment.
type Field struct {
	Name string
	Desc string
	Kind Kind // outputs only; defaults to text
}

// NewField builds a text-kind field.
func NewField(name, desc string) Field { return Field{Name: name, Desc: desc, Kind: KindText} }

func (f Field) heading() string { return strings.ReplaceAll(f.Name, "_", " ") }

// Judgment is a declared reasoning task, rendered to a prompt and parsed from
// a markdown-section reply. Nothing here hardcodes an answer: the instruction
// and field descriptions frame the task; the model decides.
type Judgment struct {
	Instruction string
	Inputs      []Field
	Outputs     []Field
	Demos       []map[string]any
	// CacheBoundary names the one input whose value is volatile across
	// otherwise-identical calls; it renders last so everything before it is a
	// stable, cacheable prefix passed to the LM as a provider-neutral hint.
	CacheBoundary string
}

// Run renders the prompt, calls the LM, and parses the reply into a
// Prediction. ctx is threaded to the LM for cancellation/deadlines.
func (j Judgment) Run(ctx context.Context, lm LM, values map[string]any) (Prediction, error) {
	prompt, cachePrefix := j.render(values)
	reply, err := lm.Complete(ctx, prompt, j.Instruction, cachePrefix)
	if err != nil {
		return nil, err
	}
	return j.ParseReply(reply), nil
}

// RenderPrompt renders the prompt for the given input values (no cache hint).
func (j Judgment) RenderPrompt(values map[string]any) string {
	prompt, _ := j.render(values)
	return prompt
}

func (j Judgment) render(values map[string]any) (string, string) {
	var lines []string
	lines = append(lines, j.Instruction, "")
	for n, demo := range j.Demos {
		lines = append(lines, fmt.Sprintf("Worked example %d:", n+1), "")
		for _, spec := range append(append([]Field{}, j.Inputs...), j.Outputs...) {
			if v, ok := demo[spec.Name]; ok {
				lines = append(lines, "## "+spec.heading(), "", strings.TrimSpace(renderValue(v)), "")
			}
		}
	}
	if len(j.Demos) > 0 {
		lines = append(lines, "Now the task at hand:", "")
	}

	ordered := j.Inputs
	if j.CacheBoundary != "" {
		ordered = nil
		for _, s := range j.Inputs {
			if s.Name != j.CacheBoundary {
				ordered = append(ordered, s)
			}
		}
		for _, s := range j.Inputs {
			if s.Name == j.CacheBoundary {
				ordered = append(ordered, s)
			}
		}
	}
	cachePrefix := ""
	for _, spec := range ordered {
		lines = append(lines, "## "+spec.heading(), "")
		if j.CacheBoundary != "" && spec.Name == j.CacheBoundary && cachePrefix == "" {
			cachePrefix = strings.Join(lines, "\n")
		}
		lines = append(lines, strings.TrimSpace(renderValue(values[spec.Name])), "")
	}

	lines = append(lines,
		"Respond using exactly the following markdown sections, each a level-2 heading (`## Name`) "+
			"followed by its value. Add nothing outside these sections:", "")
	for _, spec := range j.Outputs {
		guidance := kindGuidance[spec.Kind]
		if guidance == "" {
			guidance = kindGuidance[KindText]
		}
		detail := guidance
		if spec.Desc != "" {
			detail = spec.Desc + " -- " + guidance
		}
		lines = append(lines, "## "+spec.heading(), "("+detail+")", "")
	}
	return strings.Join(lines, "\n"), cachePrefix
}

// ParseReply parses a markdown reply into a Prediction keyed by output field
// name, each read according to its kind. A missing section yields the kind's
// zero value.
func (j Judgment) ParseReply(reply string) Prediction {
	sections := map[string]Section{}
	for _, s := range ParseDocument(reply).Sections {
		sections[Normalize(s.Name)] = s
	}
	result := Prediction{}
	for _, spec := range j.Outputs {
		section, ok := sections[Normalize(spec.heading())]
		if !ok {
			section, ok = sections[Normalize(spec.Name)]
		}
		result[spec.Name] = readField(spec, section, ok)
	}
	return result
}

func readField(spec Field, section Section, present bool) any {
	if !present {
		switch spec.Kind {
		case KindList:
			return []string{}
		case KindMap:
			return map[string]string{}
		case KindBool:
			return false
		default:
			return ""
		}
	}
	if spec.Kind == KindMap {
		return argumentBlocks(section.Lines)
	}
	body := section.StructuredBody()
	if spec.Kind == KindList {
		if len(body.Bullets) > 0 {
			return append([]string{}, body.Bullets...)
		}
		return append([]string{}, body.Numbered...)
	}
	parts := []string{}
	if body.Prose != "" {
		parts = append(parts, body.Prose)
	}
	parts = append(parts, body.Bullets...)
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	switch spec.Kind {
	case KindBool:
		fields := strings.Fields(text)
		if len(fields) == 0 {
			return false
		}
		return Coerce(fields[0]) == true
	case KindStr:
		if text == "" {
			return ""
		}
		return strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	default:
		return text
	}
}

// renderValue renders an input value for the prompt. Strings pass through;
// maps render as deterministic sorted "key: value" lines (readable, unlike a
// raw Go map print); everything else uses fmt.Sprint.
func renderValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var lines []string
		for _, k := range keys {
			lines = append(lines, fmt.Sprintf("- %s: %v", k, t[k]))
		}
		return strings.Join(lines, "\n")
	default:
		return fmt.Sprint(v)
	}
}

// Prediction is a Judgment's parsed result, keyed by output field name. The
// typed getters read a field per its kind with a safe zero-value fallback,
// so a call site reads pred.Bool("complies"), pred.Text("rationale"), etc.
type Prediction map[string]any

// Text returns a text/str field.
func (p Prediction) Text(name string) string {
	s, _ := p[name].(string)
	return s
}

// Str is an alias for Text for str-kind fields.
func (p Prediction) Str(name string) string { return p.Text(name) }

// Bool returns a bool field.
func (p Prediction) Bool(name string) bool {
	b, _ := p[name].(bool)
	return b
}

// List returns a list field.
func (p Prediction) List(name string) []string {
	l, _ := p[name].([]string)
	return l
}

// Map returns a map field.
func (p Prediction) Map(name string) map[string]string {
	m, _ := p[name].(map[string]string)
	return m
}
