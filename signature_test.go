package ear

import (
	"context"
	"strings"
	"testing"
)

// Exercises the typed generic signature layer: Go types drive parsing, the
// result is a real typed struct (no string keys), and a description may
// contain commas.

type demoIn struct {
	Question string         `ear:"question,The thing to answer"`
	Facts    map[string]any `ear:"facts"`
}

type demoOut struct {
	// Description deliberately contains a comma -- it must not be mistaken
	// for a kind field (the regression that broke bool parsing).
	Approved bool              `ear:"approved,True if allowed, False if denied"`
	Reason   string            `ear:"reason,One sentence"`
	Tags     []string          `ear:"tags,Zero or more labels"`
	Fields   map[string]string `ear:"fields"`
	Grade    string            // untagged -> defaults to snake-cased "grade"
}

var demoSig = Signature[demoIn, demoOut]{Instruction: "Decide."}

func TestSignatureTypedRoundTrip(t *testing.T) {
	reply := Reply(
		"approved", "yes",
		"reason", "within all limits",
		"tags", "- alpha\n- beta",
		"fields", "- a: 1\n- b: two",
		"grade", "B",
	)
	lm := &ScriptedLM{Default: reply}
	out, err := demoSig.Run(context.Background(), lm, demoIn{Question: "ok?", Facts: map[string]any{"x": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Approved { // bool inferred from the Go type, comma in desc survived
		t.Error("Approved should be true")
	}
	if out.Reason != "within all limits" {
		t.Errorf("Reason = %q", out.Reason)
	}
	if len(out.Tags) != 2 || out.Tags[0] != "alpha" {
		t.Errorf("Tags = %v", out.Tags)
	}
	if out.Fields["a"] != "1" || out.Fields["b"] != "two" {
		t.Errorf("Fields = %v", out.Fields)
	}
	if out.Grade != "B" {
		t.Errorf("Grade = %q", out.Grade)
	}
}

func TestSignatureRenderReflectsTypes(t *testing.T) {
	prompt := demoSig.Render(demoIn{Question: "ok?", Facts: map[string]any{"x": 1}})
	for _, want := range []string{
		"## question", "## facts", // input headings (descriptions are metadata, not prompted)
		"## approved", "yes or no", // bool -> yes/no guidance
		"## tags", "each line beginning with '- '", // []string -> list guidance
		"True if allowed, False if denied", // output desc with a comma survives
		"## grade",                         // untagged field, snake-cased default name
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, prompt)
		}
	}
}

func TestSignatureFieldCacheIsStable(t *testing.T) {
	a := cachedFields(typeOf[demoOut](), true)
	b := cachedFields(typeOf[demoOut](), true)
	if len(a) != 5 || len(b) != 5 {
		t.Fatalf("expected 5 output fields, got %d/%d", len(a), len(b))
	}
	// Bool field's kind is inferred, not tag-driven.
	if a[0].Name != "approved" || a[0].Kind != KindBool {
		t.Errorf("field[0] = %+v", a[0])
	}
}
