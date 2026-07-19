package ear

import (
	"context"
	"fmt"
	"strings"
)

// The model-facing half of a Contract: filling its declared fields from a
// prose decision, and judging whether the filling honours the fields'
// meanings. The extraction Judgment is built dynamically from the contract's
// own fields at runtime -- one output per field, the authored meaning as its
// description -- which is exactly the case the reflective Judgment engine
// exists for (a typed Signature can't express a schema known only at runtime).

// Extract fills the contract's fields from the prose decision using the
// model, returning authored-name -> coerced value. hint, when non-empty, is
// fed back into the instruction after a nonconforming attempt.
func (c *Contract) Extract(ctx context.Context, lm LM, decision any, intent, hint string) (map[string]any, error) {
	instruction := "Fill the deliverable's fields from the decision, exactly as the decision states them -- " +
		"never invent a value the decision does not support. Each field's description is its authored meaning."
	if hint != "" {
		instruction += "\nA prior attempt was judged nonconforming: " + hint
	}
	outputs := make([]Field, len(c.Fields))
	for i, f := range c.Fields {
		meaning := f.Meaning
		if meaning == "" {
			meaning = f.Name
		}
		outputs[i] = Field{Name: f.Identifier(), Desc: meaning, Kind: KindStr}
	}
	j := Judgment{
		Instruction: instruction,
		Inputs: []Field{
			NewField("intent", "The intent the decision resolves"),
			NewField("decision", "The prose decision to fill the fields from"),
		},
		Outputs: outputs,
	}
	pred, err := j.Run(ctx, lm, map[string]any{"intent": intent, "decision": fmt.Sprint(decision)})
	if err != nil {
		return nil, err
	}
	data := make(map[string]any, len(c.Fields))
	for _, f := range c.Fields {
		// One line per value: deliverable fields are facts, and the bullet
		// they render as must round-trip through the Section parser.
		raw := strings.Join(strings.Fields(pred.Str(f.Identifier())), " ")
		data[f.Name] = Coerce(raw)
	}
	return data, nil
}

// JudgeWithModel judges whether the filled data honours the fields' meanings
// using the model (the JudgeContractConformance signature), returning
// (conforms, rationale). The structural, no-model check is Contract.Judge.
func (c *Contract) JudgeWithModel(ctx context.Context, lm LM, data map[string]any) (bool, string, error) {
	out, err := JudgeContractConformance.Run(ctx, lm, ContractConformanceIn{
		Contract: c.RenderFields(),
		Data:     c.renderData(data),
	})
	if err != nil {
		return false, "", err
	}
	return out.Conforms, out.Rationale, nil
}

// renderData renders filled data as "- name: value" bullets in field order,
// so the conformance judge reads the values in the contract's declared shape.
func (c *Contract) renderData(data map[string]any) string {
	lines := make([]string, 0, len(c.Fields))
	for _, f := range c.Fields {
		lines = append(lines, fmt.Sprintf("- %s: %v", f.Name, data[f.Name]))
	}
	return strings.Join(lines, "\n")
}
