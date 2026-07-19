package ear

import (
	"context"
	"reflect"
	"strings"
	"sync"
)

// Signature is a typed, generic reasoning task -- EAR's DSPy replacement in
// native Go. In and Out are structs whose exported fields, tagged
// `ear:"name,description"`, declare the model's inputs and outputs. A field's
// Go type picks how its answer is parsed: bool -> yes/no, []string -> a
// bulleted list, map[string]string -> name/value blocks, string -> prose. So
// there are no stringly-typed keys and no map[string]any: Run returns a real,
// statically-typed Out value.
//
//	type policyIn struct {
//		Statement string         `ear:"policy_statement,The policy in plain English"`
//		Context   map[string]any `ear:"context"`
//	}
//	type policyOut struct {
//		Complies  bool   `ear:"complies,True if the context satisfies the policy"`
//		Rationale string `ear:"rationale,One sentence explaining the judgment"`
//	}
//	var JudgePolicy = Signature[policyIn, policyOut]{Instruction: "..."}
//	out, err := JudgePolicy.Run(ctx, lm, policyIn{Statement: p.Statement, Context: c})
//	// out.Complies is a bool; out.Rationale is a string. No casts.
//
// The reflection that builds the prompt and reads the reply is the same
// pattern encoding/json uses; field layouts are cached per type. Dynamic
// tasks whose fields are only known at runtime (e.g. contract extraction)
// use the underlying Judgment directly instead.
type Signature[In, Out any] struct {
	Instruction string
	// CacheBoundary is the ear-name of the one volatile input, rendered last
	// so the prefix before it is a stable, cacheable span. Optional.
	CacheBoundary string
}

// Run renders the prompt from in, calls the model, and parses the reply into
// a typed Out. ctx is threaded to the LM for cancellation/deadlines.
func (s Signature[In, Out]) Run(ctx context.Context, lm LM, in In) (Out, error) {
	var out Out
	pred, err := s.judgment().Run(ctx, lm, structToValues(in))
	if err != nil {
		return out, err
	}
	fillStruct(&out, pred)
	return out, nil
}

// Render returns the prompt this signature would send for in -- useful for
// tests and inspection.
func (s Signature[In, Out]) Render(in In) string {
	return s.judgment().RenderPrompt(structToValues(in))
}

func (s Signature[In, Out]) judgment() Judgment {
	return Judgment{
		Instruction:   s.Instruction,
		Inputs:        cachedFields(typeOf[In](), false),
		Outputs:       cachedFields(typeOf[Out](), true),
		CacheBoundary: s.CacheBoundary,
	}
}

// -- reflection (cached per struct type, like encoding/json) ----------------

var fieldCache sync.Map // reflect.Type -> [2][]Field indexed by output bool

func typeOf[T any]() reflect.Type { return reflect.TypeOf((*T)(nil)).Elem() }

func cachedFields(t reflect.Type, output bool) []Field {
	if cached, ok := fieldCache.Load(t); ok {
		return cached.([2][]Field)[boolIndex(output)]
	}
	pair := [2][]Field{reflectFields(t, false), reflectFields(t, true)}
	fieldCache.Store(t, pair)
	return pair[boolIndex(output)]
}

func boolIndex(b bool) int {
	if b {
		return 1
	}
	return 0
}

func reflectFields(t reflect.Type, output bool) []Field {
	if t.Kind() != reflect.Struct {
		return nil
	}
	var fields []Field
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		name, desc := parseEarTag(sf)
		if name == "-" {
			continue
		}
		f := Field{Name: name, Desc: desc}
		if output {
			f.Kind = inferKind(sf.Type)
		}
		fields = append(fields, f)
	}
	return fields
}

// parseEarTag reads the `ear:"name,description"` tag. Only the first comma
// separates name from description, so a description may itself contain
// commas. An empty/absent name falls back to the field's snake-cased name.
// The output kind is never in the tag -- it is inferred from the field's Go
// type -- so the tag stays purely descriptive.
func parseEarTag(sf reflect.StructField) (name, desc string) {
	tag := sf.Tag.Get("ear")
	rawName, rawDesc, _ := strings.Cut(tag, ",")
	name = strings.TrimSpace(rawName)
	if name == "" {
		name = snakeCase(sf.Name)
	}
	return name, strings.TrimSpace(rawDesc)
}

// inferKind picks a Judgment output Kind from a field's Go type:
// string -> text, bool -> bool, []string -> list, map[string]string -> map.
func inferKind(t reflect.Type) Kind {
	switch t.Kind() {
	case reflect.Bool:
		return KindBool
	case reflect.Slice:
		if t.Elem().Kind() == reflect.String {
			return KindList
		}
	case reflect.Map:
		return KindMap
	}
	return KindText
}

func structToValues[In any](in In) map[string]any {
	v := reflect.ValueOf(in)
	if v.Kind() != reflect.Struct {
		return map[string]any{}
	}
	t := v.Type()
	values := make(map[string]any, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		name, _ := parseEarTag(sf)
		if name == "-" {
			continue
		}
		values[name] = v.Field(i).Interface()
	}
	return values
}

func fillStruct[Out any](out *Out, pred Prediction) {
	v := reflect.ValueOf(out).Elem()
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		name, _ := parseEarTag(sf)
		if name == "-" {
			continue
		}
		fv := v.Field(i)
		switch {
		case fv.Kind() == reflect.Bool:
			fv.SetBool(pred.Bool(name))
		case fv.Kind() == reflect.String:
			fv.SetString(pred.Text(name))
		case fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() == reflect.String:
			fv.Set(reflect.ValueOf(pred.List(name)))
		case fv.Kind() == reflect.Map:
			fv.Set(reflect.ValueOf(pred.Map(name)))
		}
	}
}

// snakeCase converts an exported Go field name to a spaced-lowercase ear
// name ("RelevantProcessNames" -> "relevant process names"), so a field can
// go untagged when its name already matches the signature's heading.
func snakeCase(name string) string {
	var b strings.Builder
	for i, r := range name {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r + 32)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
