package ear

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlugFoldsNamesTheSameWayCrossReferencesDo(t *testing.T) {
	// "Credit Risk Guru", "credit-risk-guru" and "credit_risk_guru" all
	// address the same file, exactly as they all resolve to the same object.
	want := slug("Credit Risk Guru")
	for _, variant := range []string{"credit-risk-guru", "credit_risk_guru", "CREDIT RISK GURU"} {
		if got := slug(variant); got != want {
			t.Errorf("slug(%q) = %q, want %q", variant, got, want)
		}
	}
	if got := slug("!!!"); got != "unnamed" {
		t.Errorf("a name with nothing addressable should fall back, got %q", got)
	}
}

func TestStoreListsNamesFromHeadingsNotFilenames(t *testing.T) {
	// The slug is an address, not the name: the catalogue reports what each
	// file's own heading says.
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write("Credit Risk Guru", "## Credit Risk Guru\n\nUnderwrite conservatively.\n"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(store.Directory, "credit-risk-guru.md")); err != nil {
		t.Errorf("the file should be addressed by slug: %v", err)
	}
	names, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "Credit Risk Guru" {
		t.Errorf("want the heading's name, got %v", names)
	}
}

func TestStoreMissingNameFailsLoudlyListingWhatIsThere(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.Write("Known Thing", "## Known Thing\n\nHere.\n")

	_, err = store.Read("Absent Thing")
	if err == nil {
		t.Fatal("reading an uncatalogued name must fail")
	}
	// Same discipline as an unresolved cross-reference in the loader: say what
	// was asked for, and what there was.
	for _, want := range []string{"Absent Thing", "Known Thing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q omits %q", err.Error(), want)
		}
	}
}

func TestStoreDeleteIsIdempotent(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	store.Write("Thing", "## Thing\n\nHere.\n")
	if err := store.Delete("Thing"); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	// The postcondition the caller wanted already holds.
	if err := store.Delete("Thing"); err != nil {
		t.Errorf("deleting an absent object should not error: %v", err)
	}
	exists, _ := store.Exists("Thing")
	if exists {
		t.Error("the object should be gone")
	}
}

// -- round trips -------------------------------------------------------------

func TestSkillRoundTrip(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	catalogue := SkillCatalogue(store)

	original := &Skill{Name: "Band Credit Profile", Prompt: "Combine the score tier and DTI band into a grade A-E."}
	if err := catalogue.Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := catalogue.Load("band credit profile")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Name != original.Name {
		t.Errorf("name = %q", loaded.Name)
	}
	if !strings.Contains(loaded.Prompt, "score tier") {
		t.Errorf("prompt did not survive: %q", loaded.Prompt)
	}
}

func TestPolicyRoundTripKeepsItsGovernance(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	catalogue := PolicyCatalogue(store)

	original := &Policy{
		Name:               "Large Loan Approval",
		Statement:          "Loans above $50,000 need a human approval.",
		FallbackExpression: "loan_amount <= 50000",
		ApprovalRequired:   true,
		Approvers:          []string{"riya", "sam"},
	}
	if err := catalogue.Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := catalogue.Load("Large Loan Approval")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// A policy that round-trips without its gate is a governance hole.
	if !loaded.ApprovalRequired {
		t.Error("the approval gate did not survive the round trip")
	}
	if loaded.FallbackExpression != original.FallbackExpression {
		t.Errorf("fallback = %q", loaded.FallbackExpression)
	}
	if len(loaded.Approvers) != 2 {
		t.Errorf("approvers = %v", loaded.Approvers)
	}
	// And it still judges the same way.
	if loaded.Evaluate(map[string]any{"loan_amount": 60000.0}) {
		t.Error("the round-tripped policy should still refuse an oversized loan")
	}
}

func TestPersonaResolvesCataloguedSkills(t *testing.T) {
	root := t.TempDir()
	skillStore, _ := NewStore(filepath.Join(root, "skills"))
	personaStore, _ := NewStore(filepath.Join(root, "personas"))

	skills := SkillCatalogue(skillStore)
	skills.Save(&Skill{Name: "Risk Grade", Prompt: "Assign a grade A-E."})
	all, err := skills.LoadAll()
	if err != nil {
		t.Fatal(err)
	}

	// Authored by hand, referencing a catalogued skill by name -- the same
	// cross-reference a stacked persona.md would make.
	personaStore.Write("Credit Risk Guru",
		"## Credit Risk Guru\n\nUnderwrite conservatively.\n\nSkills: Risk Grade\n")

	persona, err := PersonaCatalogue(personaStore, all).Load("Credit Risk Guru")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(persona.Skills) != 1 || persona.Skills[0].Name != "Risk Grade" {
		t.Errorf("the catalogued skill did not resolve: %+v", persona.Skills)
	}
}

func TestUnresolvedCrossReferenceFailsLoudly(t *testing.T) {
	// Composing out of objects nobody catalogued is exactly the silent drift a
	// library exists to prevent.
	store, _ := NewStore(t.TempDir())
	store.Write("Guru", "## Guru\n\nDo the work.\n\nSkills: No Such Skill\n")

	_, err := PersonaCatalogue(store, map[string]*Skill{}).Load("Guru")
	if err == nil {
		t.Fatal("a persona referencing an uncatalogued skill must not load silently")
	}
}

// -- the whole library -------------------------------------------------------

// writeLibrary lays down a small catalogue across all five kinds.
func writeLibrary(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(kind, name, body string) {
		dir := filepath.Join(root, kind)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, slug(name)+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("skills", "Risk Grade", "## Risk Grade\n\nAssign a grade A-E from the score tier and DTI band.\n")
	write("policies", "Loan Amount Cap",
		"## Loan Amount Cap\n\nThe loan must not exceed $75,000.\n\nFallback: loan_amount <= 75000\n")
	write("personas", "Credit Risk Guru",
		"## Credit Risk Guru\n\nUnderwrite conservatively.\n\nSkills: Risk Grade\n")
	write("workflows", "Underwriting",
		"## Underwriting\n\n1. Band the credit profile (Credit Risk Guru)\n2. Decide approve or decline (Credit Risk Guru)\n\nPolicies: Loan Amount Cap\n")
	write("processes", "Underwrite Consumer Loan",
		"## Underwrite Consumer Loan\n\nUnderwrite a consumer loan application.\n\nWorkflows: Underwriting\n")
	return root
}

func TestOpenLibraryResolvesEveryKindInDependencyOrder(t *testing.T) {
	library, err := OpenLibrary(writeLibrary(t))
	if err != nil {
		t.Fatalf("OpenLibrary: %v", err)
	}

	process, err := library.Processes.Load("Underwrite Consumer Loan")
	if err != nil {
		t.Fatalf("Load process: %v", err)
	}
	if len(process.Workflows) != 1 {
		t.Fatalf("the catalogued workflow did not resolve: %+v", process.Workflows)
	}
	workflow := process.Workflows[0]
	if len(workflow.Policies) != 1 || workflow.Policies[0].Name != "Loan Amount Cap" {
		t.Errorf("the catalogued policy did not resolve: %+v", workflow.Policies)
	}

	// Delegation reached all the way down to the catalogued skill.
	var delegated *Persona
	for _, step := range workflow.Steps {
		if step.Persona != nil {
			delegated = step.Persona
		}
	}
	if delegated == nil {
		t.Fatal("no step delegated to a catalogued persona")
	}
	if len(delegated.Skills) != 1 || delegated.Skills[0].Name != "Risk Grade" {
		t.Errorf("the persona's catalogued skill did not resolve: %+v", delegated.Skills)
	}
}

func TestComposeBuildsARuntimeThatReasons(t *testing.T) {
	library, err := OpenLibrary(writeLibrary(t))
	if err != nil {
		t.Fatal(err)
	}

	runtime, err := library.Compose("Lending Desk", "Underwrite Consumer Loan")
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if len(runtime.Processes) != 1 {
		t.Fatalf("want 1 process, got %d", len(runtime.Processes))
	}

	decision, err := runtime.Reason(context.Background(),
		NewIntent("Underwrite a $20,000 loan", map[string]any{"loan_amount": 20000.0}), nil)
	if err != nil {
		t.Fatalf("a composed runtime should reason: %v", err)
	}
	if !strings.Contains(decision.(string), "Underwrite Consumer Loan") {
		t.Errorf("the composed process should appear in the decision: %v", decision)
	}

	// And the catalogued policy still governs it.
	_, err = runtime.Reason(context.Background(),
		NewIntent("Underwrite a $90,000 loan", map[string]any{"loan_amount": 90000.0}), nil)
	if err == nil {
		t.Error("the catalogued policy should still block an oversized loan")
	}
}

func TestComposeRefusesUncataloguedAndEmpty(t *testing.T) {
	library, err := OpenLibrary(writeLibrary(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := library.Compose("Desk"); err == nil {
		t.Error("composing nothing should be refused")
	}
	if _, err := library.Compose("Desk", "No Such Process"); err == nil {
		t.Error("composing an uncatalogued process should be refused")
	}
}

func TestOneProcessComposesIntoManyStacks(t *testing.T) {
	// The point of a catalogue: the same object composed into as many stacks
	// as the enterprise needs, rather than copy-pasted into each.
	library, err := OpenLibrary(writeLibrary(t))
	if err != nil {
		t.Fatal(err)
	}
	first, err := library.Compose("Retail Desk", "Underwrite Consumer Loan")
	if err != nil {
		t.Fatal(err)
	}
	second, err := library.Compose("Broker Desk", "Underwrite Consumer Loan")
	if err != nil {
		t.Fatal(err)
	}

	if first.Name == second.Name {
		t.Error("the two runtimes should be distinct")
	}
	// Each has its own memory: composing shares the authored objects, not the
	// runtime state built on top of them.
	first.Reason(context.Background(), NewIntent("Underwrite", map[string]any{"loan_amount": 1000.0}), nil)
	if second.Experience.Observations != 0 {
		t.Errorf("one stack's cycles must not appear in another's, got %d", second.Experience.Observations)
	}
}

// -- no drift from the loader -------------------------------------------------

func TestCatalogueDoesNotDriftFromTheStackedLoader(t *testing.T) {
	// A store file is simply a one-section stacked-markdown document, so the
	// same authored text must mean the same thing through either path. If
	// these ever diverge, the catalogue has grown its own dialect.
	library, err := OpenLibrary(writeLibrary(t))
	if err != nil {
		t.Fatal(err)
	}
	composed, err := library.Compose("Desk", "Underwrite Consumer Loan")
	if err != nil {
		t.Fatal(err)
	}

	// The same objects, authored as one stacked directory instead.
	stackDir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(stackDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("skills.md", "# Skills\n\n## Risk Grade\n\nAssign a grade A-E from the score tier and DTI band.\n")
	write("policy.md", "# Policies\n\n## Loan Amount Cap\n\nThe loan must not exceed $75,000.\n\nFallback: loan_amount <= 75000\n")
	write("persona.md", "# Personas\n\n## Credit Risk Guru\n\nUnderwrite conservatively.\n\nSkills: Risk Grade\n")
	write("workflow.md", "# Workflows\n\n## Underwriting\n\n1. Band the credit profile (Credit Risk Guru)\n2. Decide approve or decline (Credit Risk Guru)\n\nPolicies: Loan Amount Cap\n")
	write("process.md", "# Desk\n\n## Underwrite Consumer Loan\n\nUnderwrite a consumer loan application.\n\nWorkflows: Underwriting\n")

	stacked, err := LoadRuntime(stackDir, "Desk")
	if err != nil {
		t.Fatalf("LoadRuntime: %v", err)
	}

	if len(composed.Processes) != len(stacked.Processes) {
		t.Fatalf("process count differs: composed %d, stacked %d",
			len(composed.Processes), len(stacked.Processes))
	}
	cw, sw := composed.Processes[0].Workflows[0], stacked.Processes[0].Workflows[0]
	if len(cw.Steps) != len(sw.Steps) {
		t.Errorf("step count differs: composed %d, stacked %d", len(cw.Steps), len(sw.Steps))
	}
	if len(cw.Policies) != len(sw.Policies) {
		t.Errorf("policy count differs: composed %d, stacked %d", len(cw.Policies), len(sw.Policies))
	}

	// The decisive check: both reason to the same governed outcomes.
	for _, tc := range []struct {
		amount  float64
		blocked bool
	}{{20000, false}, {90000, true}} {
		intent := NewIntent("Underwrite", map[string]any{"loan_amount": tc.amount})
		_, composedErr := composed.Reason(context.Background(), intent, nil)
		_, stackedErr := stacked.Reason(context.Background(), intent, nil)
		if (composedErr != nil) != (stackedErr != nil) {
			t.Errorf("$%.0f: composed err = %v, stacked err = %v", tc.amount, composedErr, stackedErr)
		}
		if (composedErr != nil) != tc.blocked {
			t.Errorf("$%.0f: want blocked=%v, got %v", tc.amount, tc.blocked, composedErr)
		}
	}
}

func TestSaveRefusesAnUnnamedObject(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	if err := SkillCatalogue(store).Save(&Skill{Prompt: "no name"}); err == nil {
		t.Error("cataloguing an unnamed object should be refused")
	}
}

func TestLoadRejectsAFileWithNoHeading(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	// Written past the catalogue so it lands with no '## Heading' -- the same
	// authoring mistake tenant.md can make.
	os.WriteFile(filepath.Join(store.Directory, "broken.md"), []byte("# Title only\n\nProse.\n"), 0o644)

	if _, err := SkillCatalogue(store).Load("broken"); err == nil {
		t.Error("a catalogue file with no section heading must fail loudly")
	}
}
