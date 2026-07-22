package ear

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Store -- named catalogues of Skills, Personas, Workflows, Processes and
// Policies: one markdown file per object, on disk.
//
// Loader stacks *one directory's* skills.md / persona.md / workflow.md /
// process.md / policy.md into *one* Runtime -- the authoring model for a
// single stack. A Store is the complementary shape: a reusable *library*, one
// named object per file (skills/band-credit-profile.md,
// personas/credit-risk-guru.md, …), addressable by name and composable into
// any number of stacks, the way a shared code library is imported by many
// programs instead of copy-pasted into each.
//
// Every kind reads and writes through the same Section/Document codec and
// parses through Loader's own per-kind parsing -- a store file is simply a
// one-section stacked-markdown document, so nothing here duplicates or drifts
// from what skills.md already means. Cross-references between kinds (a
// Persona's Skills: line, a Workflow's delegated Personas and Policies, a
// Process's Workflows: line) resolve the same way Loader resolves them within
// one file: load the referenced kind first, pass its catalogue in.
//
// Nothing here decides anything. Like Loader, a Store is structural: it does
// not judge, retrieve by relevance, or version. It is a filing cabinet, not a
// search engine.

// CatalogueBackend is the minimal set of operations any catalogue backend must
// support. The file-based Store satisfies it; so could a database-backed one,
// without any kind-specific catalogue changing -- swapping backends is a
// constructor argument, not a different code path.
type CatalogueBackend interface {
	List() ([]string, error)
	Exists(name string) (bool, error)
	Read(name string) (string, error)
	Write(name, text string) error
	Delete(name string) error
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// slug is the filesystem-safe file stem for a catalogued name, folded the same
// way every cross-reference in the stack already is -- "Credit Risk Guru",
// "credit-risk-guru" and "credit_risk_guru" all address the same file.
func slug(name string) string {
	s := strings.Trim(slugUnsafe.ReplaceAllString(Normalize(name), "-"), "-")
	if s == "" {
		return "unnamed"
	}
	return s
}

// Store is the file-based catalogue backend: one directory of <slug>.md
// objects. The catalogues build named domain objects on top of these
// primitives; nothing else here touches the filesystem.
type Store struct {
	Directory string
}

// NewStore opens (creating if needed) a catalogue directory.
func NewStore(directory string) (*Store, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("opening the catalogue at %q: %w", directory, err)
	}
	return &Store{Directory: directory}, nil
}

// PathFor is the file a catalogued name addresses.
func (s *Store) PathFor(name string) string {
	return filepath.Join(s.Directory, slug(name)+".md")
}

// List returns the catalogued names, read back from each file's own heading
// rather than its filename -- the slug is an address, not the name.
func (s *Store) List() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(s.Directory, "*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)

	var names []string
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		doc := ParseDocument(string(data))
		if len(doc.Sections) > 0 {
			names = append(names, doc.Sections[0].Name)
		}
	}
	return names, nil
}

func (s *Store) Exists(name string) (bool, error) {
	_, err := os.Stat(s.PathFor(name))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// Read returns a catalogued object's markdown. A name that is not there fails
// loudly, listing what is -- the same discipline the loader applies to an
// unresolved cross-reference.
func (s *Store) Read(name string) (string, error) {
	data, err := os.ReadFile(s.PathFor(name))
	if err == nil {
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	known, listErr := s.List()
	if listErr != nil {
		return "", err
	}
	catalogued := strings.Join(known, ", ")
	if catalogued == "" {
		catalogued = "none"
	}
	return "", fmt.Errorf("%q is not in the catalogue at %s -- catalogued: %s", name, s.Directory, catalogued)
}

func (s *Store) Write(name, text string) error {
	return os.WriteFile(s.PathFor(name), []byte(text), 0o644)
}

// Delete removes a catalogued object. Deleting one that is not there is not an
// error -- the postcondition the caller wanted already holds.
func (s *Store) Delete(name string) error {
	err := os.Remove(s.PathFor(name))
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// -- the catalogue ------------------------------------------------------------

// Catalogue is a named, typed view over a backend: load an object by name,
// save one back, list what is there.
//
// Python needs a class per kind. Here one generic type serves all five, with
// the per-kind parsing and rendering supplied at construction -- the same
// collapse the loader's generic resolve[T] makes for cross-references.
type Catalogue[T any] struct {
	Backend CatalogueBackend
	Kind    string

	parse  func(Document) (T, error)
	render func(T) string
	nameOf func(T) string
}

// List returns the catalogued names.
func (c *Catalogue[T]) List() ([]string, error) { return c.Backend.List() }

// Exists reports whether a name is catalogued.
func (c *Catalogue[T]) Exists(name string) (bool, error) { return c.Backend.Exists(name) }

// Delete removes a catalogued object.
func (c *Catalogue[T]) Delete(name string) error { return c.Backend.Delete(name) }

// Load reads one object by name.
func (c *Catalogue[T]) Load(name string) (T, error) {
	var zero T
	text, err := c.Backend.Read(name)
	if err != nil {
		return zero, err
	}
	doc := ParseDocument(text)
	if len(doc.Sections) == 0 {
		return zero, fmt.Errorf("catalogued %s %q has no '## Heading' to read an object from", c.Kind, name)
	}
	return c.parse(doc)
}

// LoadAll reads every catalogued object, keyed the same normalized way
// cross-references resolve.
func (c *Catalogue[T]) LoadAll() (map[string]T, error) {
	names, err := c.List()
	if err != nil {
		return nil, err
	}
	out := make(map[string]T, len(names))
	for _, name := range names {
		item, err := c.Load(name)
		if err != nil {
			return nil, err
		}
		out[Normalize(name)] = item
	}
	return out, nil
}

// Save writes an object to the catalogue under its own name, through the same
// markdown codec the loader reads -- so anything saved here loads back as an
// ordinary stacked-markdown section.
func (c *Catalogue[T]) Save(item T) error {
	name := c.nameOf(item)
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("cannot catalogue an unnamed %s", c.Kind)
	}
	return c.Backend.Write(name, c.render(item))
}

// -- the kinds ----------------------------------------------------------------

// firstOf returns the single object a one-section catalogue file parsed to.
func firstOf[T any](items map[string]T, kind, name string) (T, error) {
	for _, item := range items {
		return item, nil
	}
	var zero T
	return zero, fmt.Errorf("catalogued %s %q parsed to nothing", kind, name)
}

// SkillCatalogue is a library of Skills.
func SkillCatalogue(backend CatalogueBackend) *Catalogue[*Skill] {
	return &Catalogue[*Skill]{
		Backend: backend, Kind: "skill",
		parse: func(doc Document) (*Skill, error) {
			return firstOf(loadSkills(doc), "skill", doc.Sections[0].Name)
		},
		render: func(s *Skill) string { return s.ToMarkdown() },
		nameOf: func(s *Skill) string { return s.Name },
	}
}

// PolicyCatalogue is a library of Policies.
func PolicyCatalogue(backend CatalogueBackend) *Catalogue[*Policy] {
	return &Catalogue[*Policy]{
		Backend: backend, Kind: "policy",
		parse: func(doc Document) (*Policy, error) {
			policies, _, err := loadPolicies(doc)
			if err != nil {
				return nil, err
			}
			return firstOf(policies, "policy", doc.Sections[0].Name)
		},
		render: func(p *Policy) string { return p.ToMarkdown() },
		nameOf: func(p *Policy) string { return p.Name },
	}
}

// PersonaCatalogue is a library of Personas. Its Skills: lines resolve against
// the skills catalogue passed in -- the same way Loader resolves them within a
// single file.
func PersonaCatalogue(backend CatalogueBackend, skills map[string]*Skill) *Catalogue[*Persona] {
	return &Catalogue[*Persona]{
		Backend: backend, Kind: "persona",
		parse: func(doc Document) (*Persona, error) {
			personas, err := loadPersonas(doc, skills)
			if err != nil {
				return nil, err
			}
			return firstOf(personas, "persona", doc.Sections[0].Name)
		},
		render: func(p *Persona) string { return p.ToMarkdown() },
		nameOf: func(p *Persona) string { return p.Name },
	}
}

// WorkflowCatalogue is a library of Workflows, resolving delegated personas
// and attached policies against the catalogues passed in.
func WorkflowCatalogue(backend CatalogueBackend, personas map[string]*Persona, policies map[string]*Policy) *Catalogue[*Workflow] {
	return &Catalogue[*Workflow]{
		Backend: backend, Kind: "workflow",
		parse: func(doc Document) (*Workflow, error) {
			workflows, _, err := loadWorkflows(doc, personas, policies)
			if err != nil {
				return nil, err
			}
			return firstOf(workflows, "workflow", doc.Sections[0].Name)
		},
		render: func(w *Workflow) string { return w.ToMarkdown() },
		nameOf: func(w *Workflow) string { return w.Name },
	}
}

// ProcessCatalogue is a library of Processes, resolving their Workflows: lines
// against the workflow catalogue passed in.
func ProcessCatalogue(backend CatalogueBackend, workflows map[string]*Workflow) *Catalogue[*Process] {
	return &Catalogue[*Process]{
		Backend: backend, Kind: "process",
		parse: func(doc Document) (*Process, error) {
			processes, _, err := loadProcesses(doc, workflows)
			if err != nil {
				return nil, err
			}
			if len(processes) == 0 {
				return nil, fmt.Errorf("catalogued process %q parsed to nothing", doc.Sections[0].Name)
			}
			return processes[0], nil
		},
		render: func(p *Process) string { return p.ToMarkdown() },
		nameOf: func(p *Process) string { return p.Name },
	}
}

// -- composing a runtime out of the library ------------------------------------

// Library is a whole catalogue: one backend per kind, resolved in dependency
// order so cross-references between kinds work exactly as they do inside a
// single stacked file.
type Library struct {
	Skills    *Catalogue[*Skill]
	Policies  *Catalogue[*Policy]
	Personas  *Catalogue[*Persona]
	Workflows *Catalogue[*Workflow]
	Processes *Catalogue[*Process]
}

// LibraryBackends is one CatalogueBackend per kind. Any implementation of
// CatalogueBackend works in any slot -- a file Store, a SQLBackend over any
// database/sql driver, or something a deployment writes itself -- so the whole
// library is backend-agnostic, not just each catalogue.
type LibraryBackends struct {
	Skills    CatalogueBackend
	Policies  CatalogueBackend
	Personas  CatalogueBackend
	Workflows CatalogueBackend
	Processes CatalogueBackend
}

// NewLibrary assembles a library over five backends, resolving the kinds in
// dependency order -- skills, then policies, then the personas that reference
// skills, then the workflows that delegate to personas and attach policies,
// then the processes that compose workflows -- because a cross-reference can
// only resolve against a catalogue already read.
//
// This is the backend-agnostic core: OpenLibrary is the file convenience over
// it, and a SQL deployment calls this directly with SQLBackends. The
// resolution order and cross-reference wiring are identical whatever backs
// each kind, so a catalogue cannot mean something different because it lives
// in Postgres rather than on disk.
func NewLibrary(backends LibraryBackends) (*Library, error) {
	if backends.Skills == nil || backends.Policies == nil || backends.Personas == nil ||
		backends.Workflows == nil || backends.Processes == nil {
		return nil, fmt.Errorf("a library needs a backend for every kind (skills, policies, personas, workflows, processes)")
	}

	library := &Library{
		Skills:   SkillCatalogue(backends.Skills),
		Policies: PolicyCatalogue(backends.Policies),
	}

	skills, err := library.Skills.LoadAll()
	if err != nil {
		return nil, err
	}
	policies, err := library.Policies.LoadAll()
	if err != nil {
		return nil, err
	}

	library.Personas = PersonaCatalogue(backends.Personas, skills)
	personas, err := library.Personas.LoadAll()
	if err != nil {
		return nil, err
	}

	library.Workflows = WorkflowCatalogue(backends.Workflows, personas, policies)
	workflows, err := library.Workflows.LoadAll()
	if err != nil {
		return nil, err
	}

	library.Processes = ProcessCatalogue(backends.Processes, workflows)
	return library, nil
}

// OpenLibrary opens a file-backed catalogue rooted at a directory, with one
// subdirectory per kind (skills/, policies/, personas/, workflows/,
// processes/). It is the file convenience over NewLibrary.
func OpenLibrary(root string) (*Library, error) {
	stores := LibraryBackends{}
	for _, spec := range []struct {
		kind string
		into *CatalogueBackend
	}{
		{"skills", &stores.Skills},
		{"policies", &stores.Policies},
		{"personas", &stores.Personas},
		{"workflows", &stores.Workflows},
		{"processes", &stores.Processes},
	} {
		store, err := NewStore(filepath.Join(root, spec.kind))
		if err != nil {
			return nil, err
		}
		*spec.into = store
	}
	return NewLibrary(stores)
}

// Compose builds a Runtime from named processes in the library -- the point of
// a catalogue: the same underwriting process composed into as many stacks as
// the enterprise needs, rather than copy-pasted into each.
//
// A name that is not catalogued fails loudly, listing what is. Composing a
// runtime out of objects nobody catalogued is exactly the silent drift a
// library is meant to prevent.
func (l *Library) Compose(name string, processNames ...string) (*Runtime, error) {
	if len(processNames) == 0 {
		return nil, fmt.Errorf("composing %q needs at least one process name", name)
	}
	runtime := NewRuntime(name)
	for _, processName := range processNames {
		process, err := l.Processes.Load(processName)
		if err != nil {
			return nil, err
		}
		runtime.AddProcess(process)
	}
	return runtime, nil
}
