package ear

import (
	"context"
	"strings"
	"testing"
)

func TestKnowledgeChunksMarkdown(t *testing.T) {
	k := &Knowledge{}
	k.AddDocument("manual", "m.md", "Preamble here.\n\n## Alpha\n\nAlpha content.\n\n## Beta\n\nBeta content.\n")
	if k.Len() != 3 { // preamble + two sections
		t.Fatalf("passages = %d, want 3: %+v", k.Len(), k.Passages)
	}
	if !strings.Contains(k.Passages[1].Source, "§ Alpha") {
		t.Errorf("section source = %q", k.Passages[1].Source)
	}
}

func TestKnowledgeBM25Ranking(t *testing.T) {
	k := &Knowledge{}
	k.AddDocument("manual", "m.md",
		"## DTI\n\nThe DTI ratio must not exceed 0.43.\n\n## Fraud\n\nWatch for identity fraud signals.\n")
	got := k.Candidates("the DTI ratio", 5)
	if len(got) == 0 || !strings.Contains(got[0].Text, "0.43") {
		t.Fatalf("best candidate should be the DTI passage, got %+v", got)
	}
	// The unrelated Fraud passage scores zero and is excluded.
	for _, p := range got {
		if strings.Contains(p.Text, "fraud") {
			t.Errorf("non-matching passage should not be a candidate: %q", p.Text)
		}
	}
}

func TestLibrarianResearchRecords(t *testing.T) {
	k := &Knowledge{}
	k.AddDocument("manual", "m.md", "## DTI\n\nThe DTI ratio must not exceed 0.43.\n")
	rt := NewRuntime("R")
	rt.Librarian = &Librarian{Knowledge: k}
	research := rt.Librarian.Research(context.Background(), rt, NewIntent("dti ratio", nil))
	if research == nil || len(research.Citations) == 0 {
		t.Fatalf("expected research with citations, got %+v", research)
	}
	var sawRetrieval bool
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "retrieval" {
			sawRetrieval = true
		}
	}
	if !sawRetrieval {
		t.Error("expected a retrieval record on the trail")
	}
}

func TestCycleAugmentsReasoningWithKnowledge(t *testing.T) {
	k := &Knowledge{}
	k.AddDocument("manual", "m.md", "## DTI\n\nThe DTI ratio must not exceed 0.43.\n")
	lm := &ScriptedLM{Default: Reply(
		"complies", "yes", "decision", "APPROVED", "explanation", "x", "assessment", "y",
		"relevant numbers", "- 1", "rationale", "the DTI passage applies", // librarian selects passage 1
	)}
	proc := &Process{Name: "Underwriting", Description: "Underwrite."}
	proc.AddWorkflow((&Workflow{Name: "W"}).AddStep("Decide.", nil))
	rt := NewRuntime("R", WithLM(lm))
	rt.AddProcess(proc)
	rt.Librarian = &Librarian{Knowledge: k}

	var captured *Evidence
	rt.Pipeline = append(rt.Pipeline, evidenceCapture{&captured})

	if _, err := rt.Reason(context.Background(), NewIntent("check the DTI ratio", nil), nil); err != nil {
		t.Fatal(err)
	}

	// The retrieved passage reached the model's prompt (RAG augmentation).
	var augmented bool
	for _, call := range lm.Calls() {
		if strings.Contains(call.Prompt, "The DTI ratio must not exceed 0.43") {
			augmented = true
		}
	}
	if !augmented {
		t.Error("the retrieved knowledge should appear in the reasoning prompt")
	}
	// And the citation is on the decision's evidence.
	if captured == nil {
		t.Fatal("evidence not captured")
	}
	cites, _ := captured.Sources["citations"].([]string)
	if len(cites) == 0 || !strings.Contains(cites[0], "manual") {
		t.Errorf("evidence citations = %v", captured.Sources["citations"])
	}
}

func TestLibrarianLLMSelectsPassages(t *testing.T) {
	k := &Knowledge{}
	// Three passages, each matching the query equally -> BM25 keeps document
	// order, so passage numbers are stable.
	k.AddDocument("m", "m.md", "## A\n\nloan alpha rule.\n\n## B\n\nloan beta rule.\n\n## C\n\nloan gamma rule.\n")
	lm := &ScriptedLM{Default: Reply("relevant numbers", "- 1\n- 3", "rationale", "A and C apply")}
	rt := NewRuntime("R", WithLM(lm))
	rt.Librarian = &Librarian{Knowledge: k}

	research := rt.Librarian.Research(context.Background(), rt, NewIntent("loan rule", nil))
	if research == nil || len(research.Passages) != 2 {
		t.Fatalf("expected 2 model-selected passages, got %+v", research)
	}
	if !strings.Contains(research.Citations[0], "§ A") || !strings.Contains(research.Citations[1], "§ C") {
		t.Errorf("selected the wrong passages: %v", research.Citations)
	}
}

func TestKnowledgeGistIndex(t *testing.T) {
	k := &Knowledge{}
	k.AddDocument("m", "m.md", "## A\n\nDTI ceiling is 0.43.\n\n## B\n\nFraud signals list.\n")
	if strings.Contains(k.Narrowing(), "gists") {
		t.Fatal("no gists yet")
	}
	lm := &ScriptedLM{Default: Reply("gist", "debt-to-income limit and fraud clues in plain words")}
	n, err := k.Index(context.Background(), lm)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("gisted %d passages, want 2", n)
	}
	if !strings.Contains(k.Narrowing(), "gists") {
		t.Error("narrowing should now report the gist index")
	}
	if k.Passages[0].Gist == "" {
		t.Error("passage should carry its gist")
	}
}
