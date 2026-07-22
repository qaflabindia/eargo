package ear

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// This file tests the SQL catalogue backend against the real database/sql
// package, driven by a compact in-memory driver defined below. Using the real
// database/sql machinery -- connection pool, prepared statements, rows,
// transactions -- means SQLBackend is exercised exactly as it would be against
// Postgres or SQLite, while the driver stays standard-library-only so the
// module keeps its zero third-party dependencies.

func init() { sql.Register("earmem", memDriver{}) }

var dsnCounter atomic.Int64

// openMemDB returns an isolated in-memory database.
func openMemDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("mem-%d", dsnCounter.Add(1))
	db, err := sql.Open("earmem", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); memStores.Delete(dsn) })
	return db
}

func sqlBackend(t *testing.T, opts ...SQLOption) *SQLBackend {
	t.Helper()
	b, err := NewSQLBackend(openMemDB(t), "skills", opts...)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// -- the CatalogueBackend contract, over real database/sql -------------------

func TestSQLBackendRoundTrip(t *testing.T) {
	b := sqlBackend(t)

	if err := b.Write("Credit Risk Guru", "## Credit Risk Guru\n\nUnderwrite conservatively.\n"); err != nil {
		t.Fatal(err)
	}
	// The slug is an address: the three ways of writing the name all reach the
	// same row, exactly as in the file backend.
	body, err := b.Read("credit_risk_guru")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(body, "Underwrite conservatively") {
		t.Errorf("body did not survive: %q", body)
	}

	names, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "Credit Risk Guru" {
		t.Errorf("List should report the object's name, got %v", names)
	}

	exists, _ := b.Exists("CREDIT RISK GURU")
	if !exists {
		t.Error("Exists should fold the name like every cross-reference")
	}
}

func TestSQLBackendUpsertReplacesInPlace(t *testing.T) {
	b := sqlBackend(t)
	b.Write("Guru", "## Guru\n\nFirst.\n")
	b.Write("Guru", "## Guru\n\nSecond.\n")

	body, err := b.Read("Guru")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "First") || !strings.Contains(body, "Second") {
		t.Errorf("upsert should replace, got %q", body)
	}
	names, _ := b.List()
	if len(names) != 1 {
		t.Errorf("upsert should not duplicate the row, got %v", names)
	}
}

func TestSQLBackendUpsertIsAtomic(t *testing.T) {
	// The upsert is delete-then-insert. If the insert fails, the transaction
	// must roll back the delete -- a mid-write failure cannot leave a hole
	// where the old object used to be.
	b := sqlBackend(t)
	if err := b.Write("Guru", "## Guru\n\nOriginal.\n"); err != nil {
		t.Fatal(err)
	}

	// "FAIL" trips the driver's insert failure after the delete has run.
	if err := b.Write("Guru", "FAIL"); err == nil {
		t.Fatal("a failing insert should surface as an error")
	}

	// The original survives: the delete was rolled back with the insert.
	body, err := b.Read("Guru")
	if err != nil {
		t.Fatalf("the object must survive a failed upsert: %v", err)
	}
	if !strings.Contains(body, "Original") {
		t.Errorf("rollback did not restore the original, got %q", body)
	}
}

func TestSQLBackendMissingNameFailsLoudly(t *testing.T) {
	b := sqlBackend(t)
	b.Write("Known", "## Known\n\nHere.\n")

	_, err := b.Read("Absent")
	if err == nil {
		t.Fatal("reading an uncatalogued name must fail")
	}
	for _, want := range []string{"Absent", "Known"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q omits %q", err.Error(), want)
		}
	}
}

func TestSQLBackendDeleteIsIdempotent(t *testing.T) {
	b := sqlBackend(t)
	b.Write("Thing", "## Thing\n\nHere.\n")
	if err := b.Delete("Thing"); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := b.Delete("Thing"); err != nil {
		t.Errorf("deleting an absent object should not error: %v", err)
	}
	if exists, _ := b.Exists("Thing"); exists {
		t.Error("the object should be gone")
	}
}

func TestSQLBackendRejectsUnsafeTableName(t *testing.T) {
	// The table name cannot be a bound parameter, so it is validated as a plain
	// identifier -- nothing off the wire ever reaches the query text.
	for _, bad := range []string{"skills; DROP TABLE users", "a b", "", "1abc", "sk-ills"} {
		if _, err := NewSQLBackend(openMemDB(t), bad); err == nil {
			t.Errorf("table name %q should be rejected", bad)
		}
	}
}

func TestSQLBackendNilDB(t *testing.T) {
	if _, err := NewSQLBackend(nil, "skills"); err == nil {
		t.Error("a nil *sql.DB should be refused")
	}
}

func TestSQLBackendPostgresPlaceholders(t *testing.T) {
	// The Postgres option renders $N placeholders; the round trip must still
	// work, proving the placeholder seam is wired through every statement.
	b := sqlBackend(t, Postgres)
	if b.ph(2) != "$2" {
		t.Errorf("Postgres placeholder = %q, want $2", b.ph(2))
	}
	if err := b.Write("Guru", "## Guru\n\nHere.\n"); err != nil {
		t.Fatalf("Write with $N placeholders: %v", err)
	}
	if _, err := b.Read("Guru"); err != nil {
		t.Fatalf("Read with $N placeholders: %v", err)
	}
}

func TestSQLBackendWithoutSchemaInit(t *testing.T) {
	db := openMemDB(t)
	// A deployment that manages its own schema provisions the table itself.
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS skills (slug VARCHAR(255) PRIMARY KEY, name TEXT NOT NULL, body TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	b, err := NewSQLBackend(db, "skills", WithoutSchemaInit())
	if err != nil {
		t.Fatalf("WithoutSchemaInit should not provision: %v", err)
	}
	if err := b.Write("Guru", "## Guru\n\nHere.\n"); err != nil {
		t.Fatalf("Write against a pre-provisioned table: %v", err)
	}

	// And skipping init against a table that does not exist fails when used --
	// the backend did not silently create it.
	missing, err := NewSQLBackend(openMemDB(t), "skills", WithoutSchemaInit())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missing.List(); err == nil {
		t.Error("using an unprovisioned table should fail, not silently succeed")
	}
}

func TestSQLBackendCustomSchema(t *testing.T) {
	b, err := NewSQLBackend(openMemDB(t), "skills",
		WithSchema("CREATE TABLE IF NOT EXISTS %s (slug VARCHAR(255) PRIMARY KEY, name TEXT NOT NULL, body TEXT NOT NULL)"))
	if err != nil {
		t.Fatalf("custom schema: %v", err)
	}
	if err := b.Write("Guru", "## Guru\n\nHere.\n"); err != nil {
		t.Fatal(err)
	}
}

// -- the whole library, in a database ----------------------------------------

// libraryFixture is the same authored catalogue used across backends.
var libraryFixture = map[string]map[string]string{
	"skills": {
		"Risk Grade": "## Risk Grade\n\nAssign a grade A-E from the score tier and DTI band.\n",
	},
	"policies": {
		"Loan Amount Cap": "## Loan Amount Cap\n\nThe loan must not exceed $75,000.\n\nFallback: loan_amount <= 75000\n",
	},
	"personas": {
		"Credit Risk Guru": "## Credit Risk Guru\n\nUnderwrite conservatively.\n\nSkills: Risk Grade\n",
	},
	"workflows": {
		"Underwriting": "## Underwriting\n\n1. Band the credit profile (Credit Risk Guru)\n2. Decide approve or decline (Credit Risk Guru)\n\nPolicies: Loan Amount Cap\n",
	},
	"processes": {
		"Underwrite Consumer Loan": "## Underwrite Consumer Loan\n\nUnderwrite a consumer loan application.\n\nWorkflows: Underwriting\n",
	},
}

// seedSQL writes the fixture into one database, one table per kind, and returns
// the backends.
func seedSQL(t *testing.T, db *sql.DB) LibraryBackends {
	t.Helper()
	backend := func(kind string) CatalogueBackend {
		b, err := NewSQLBackend(db, kind)
		if err != nil {
			t.Fatal(err)
		}
		for name, body := range libraryFixture[kind] {
			if err := b.Write(name, body); err != nil {
				t.Fatal(err)
			}
		}
		return b
	}
	return LibraryBackends{
		Skills:    backend("skills"),
		Policies:  backend("policies"),
		Personas:  backend("personas"),
		Workflows: backend("workflows"),
		Processes: backend("processes"),
	}
}

func TestSQLLibraryComposesAndGoverns(t *testing.T) {
	library, err := NewLibrary(seedSQL(t, openMemDB(t)))
	if err != nil {
		t.Fatalf("NewLibrary over SQL: %v", err)
	}

	// Cross-references resolved through the database: the process's workflow,
	// the workflow's policy, and the persona's skill.
	process, err := library.Processes.Load("Underwrite Consumer Loan")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	workflow := process.Workflows[0]
	if len(workflow.Policies) != 1 || workflow.Policies[0].Name != "Loan Amount Cap" {
		t.Errorf("the catalogued policy did not resolve from SQL: %+v", workflow.Policies)
	}

	runtime, err := library.Compose("Lending Desk", "Underwrite Consumer Loan")
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	// The catalogued policy governs the composed runtime.
	if _, err := runtime.Reason(context.Background(),
		NewIntent("Underwrite", map[string]any{"loan_amount": 20000.0}), nil); err != nil {
		t.Errorf("a compliant loan should decide: %v", err)
	}
	if _, err := runtime.Reason(context.Background(),
		NewIntent("Underwrite", map[string]any{"loan_amount": 90000.0}), nil); err == nil {
		t.Error("the catalogued policy should block an oversized loan")
	}
}

func TestSQLLibraryHelperUsesPrefixedTables(t *testing.T) {
	db := openMemDB(t)
	// Pre-seed via SQLLibrary's own table naming so the helper reads them back.
	for kind, objects := range libraryFixture {
		b, err := NewSQLBackend(db, "acme_"+kind)
		if err != nil {
			t.Fatal(err)
		}
		for name, body := range objects {
			b.Write(name, body)
		}
	}
	library, err := SQLLibrary(db, "acme")
	if err != nil {
		t.Fatalf("SQLLibrary: %v", err)
	}
	if _, err := library.Processes.Load("Underwrite Consumer Loan"); err != nil {
		t.Errorf("the prefixed tables did not resolve: %v", err)
	}
}

// TestSQLAndFileLibrariesAgree is the decisive test: the same authored text,
// backed by a database in one library and by files in another, must reason to
// the same governed outcomes. A catalogue cannot mean something different
// because it lives in Postgres rather than on disk.
func TestSQLAndFileLibrariesAgree(t *testing.T) {
	// File-backed.
	fileRoot := t.TempDir()
	fileBackends := LibraryBackends{}
	slots := map[string]*CatalogueBackend{
		"skills": &fileBackends.Skills, "policies": &fileBackends.Policies,
		"personas": &fileBackends.Personas, "workflows": &fileBackends.Workflows,
		"processes": &fileBackends.Processes,
	}
	for kind, into := range slots {
		store, err := NewStore(fileRoot + "/" + kind)
		if err != nil {
			t.Fatal(err)
		}
		for name, body := range libraryFixture[kind] {
			store.Write(name, body)
		}
		*into = store
	}
	fileLib, err := NewLibrary(fileBackends)
	if err != nil {
		t.Fatal(err)
	}

	// Database-backed, identical bytes.
	sqlLib, err := NewLibrary(seedSQL(t, openMemDB(t)))
	if err != nil {
		t.Fatal(err)
	}

	fileRT, err := fileLib.Compose("Desk", "Underwrite Consumer Loan")
	if err != nil {
		t.Fatal(err)
	}
	sqlRT, err := sqlLib.Compose("Desk", "Underwrite Consumer Loan")
	if err != nil {
		t.Fatal(err)
	}

	for _, amount := range []float64{20000, 90000} {
		intent := NewIntent("Underwrite", map[string]any{"loan_amount": amount})
		_, fileErr := fileRT.Reason(context.Background(), intent, nil)
		_, sqlErr := sqlRT.Reason(context.Background(), intent, nil)
		if (fileErr != nil) != (sqlErr != nil) {
			t.Errorf("$%.0f: file err = %v, sql err = %v -- the backends disagree", amount, fileErr, sqlErr)
		}
	}
}

// =====================================================================
// A minimal in-memory database/sql driver, standard library only.
//
// It understands exactly the statements SQLBackend emits -- CREATE TABLE,
// SELECT (name / 1 / body), INSERT and DELETE -- and backs them with a locked
// map keyed by DSN, so the pool's several connections share one dataset and a
// transaction can be rolled back. It is a test fixture, not a general SQL
// engine.
// =====================================================================

type memRow struct{ name, body string }

type memDB struct {
	mu     sync.Mutex
	tables map[string]map[string]memRow // table -> slug -> row
}

var memStores sync.Map // dsn -> *memDB

func memStore(dsn string) *memDB {
	actual, _ := memStores.LoadOrStore(dsn, &memDB{tables: map[string]map[string]memRow{}})
	return actual.(*memDB)
}

func (db *memDB) clone() map[string]map[string]memRow {
	out := make(map[string]map[string]memRow, len(db.tables))
	for table, rows := range db.tables {
		m := make(map[string]memRow, len(rows))
		for slug, row := range rows {
			m[slug] = row
		}
		out[table] = m
	}
	return out
}

type memDriver struct{}

func (memDriver) Open(dsn string) (driver.Conn, error) { return &memConn{db: memStore(dsn)}, nil }

type memConn struct{ db *memDB }

func (c *memConn) Prepare(query string) (driver.Stmt, error) {
	return &memStmt{db: c.db, query: query}, nil
}
func (c *memConn) Close() error { return nil }
func (c *memConn) Begin() (driver.Tx, error) {
	c.db.mu.Lock()
	snap := c.db.clone()
	c.db.mu.Unlock()
	return &memTx{db: c.db, snapshot: snap}, nil
}

type memTx struct {
	db       *memDB
	snapshot map[string]map[string]memRow
	done     bool
}

func (t *memTx) Commit() error { t.done = true; return nil }
func (t *memTx) Rollback() error {
	if t.done {
		return nil
	}
	t.db.mu.Lock()
	t.db.tables = t.snapshot
	t.db.mu.Unlock()
	t.done = true
	return nil
}

type memStmt struct {
	db    *memDB
	query string
}

func (s *memStmt) Close() error  { return nil }
func (s *memStmt) NumInput() int { return -1 } // skip arg-count validation

func (s *memStmt) Exec(args []driver.Value) (driver.Result, error) {
	fields := sqlFields(s.query)
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty statement")
	}
	s.db.mu.Lock()
	defer s.db.mu.Unlock()

	switch strings.ToUpper(fields[0]) {
	case "CREATE":
		table := after(fields, "EXISTS")
		if table == "" {
			table = after(fields, "TABLE")
		}
		if _, ok := s.db.tables[table]; !ok {
			s.db.tables[table] = map[string]memRow{}
		}
		return memResult{}, nil
	case "DELETE":
		table := after(fields, "FROM")
		rows, ok := s.db.tables[table]
		if !ok {
			return nil, fmt.Errorf("no such table: %s", table)
		}
		delete(rows, asString(args, 0))
		return memResult{}, nil
	case "INSERT":
		table := after(fields, "INTO")
		rows, ok := s.db.tables[table]
		if !ok {
			return nil, fmt.Errorf("no such table: %s", table)
		}
		// A test sentinel: a body of "FAIL" errors the INSERT, so the
		// delete-then-insert upsert's rollback can be exercised.
		if asString(args, 2) == "FAIL" {
			return nil, fmt.Errorf("simulated insert failure")
		}
		rows[asString(args, 0)] = memRow{name: asString(args, 1), body: asString(args, 2)}
		return memResult{}, nil
	}
	return nil, fmt.Errorf("unsupported Exec: %s", s.query)
}

func (s *memStmt) Query(args []driver.Value) (driver.Rows, error) {
	fields := sqlFields(s.query)
	if len(fields) < 2 || strings.ToUpper(fields[0]) != "SELECT" {
		return nil, fmt.Errorf("unsupported Query: %s", s.query)
	}
	projection := fields[1]
	table := after(fields, "FROM")

	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	rows, ok := s.db.tables[table]
	if !ok {
		return nil, fmt.Errorf("no such table: %s", table)
	}

	// A WHERE clause filters to one slug; its absence lists everything.
	if strings.Contains(strings.ToUpper(s.query), "WHERE") {
		row, present := rows[asString(args, 0)]
		if !present {
			return &memRows{cols: []string{projection}}, nil
		}
		return &memRows{cols: []string{projection}, data: [][]driver.Value{{project(projection, row)}}}, nil
	}

	// List: ORDER BY slug.
	slugs := make([]string, 0, len(rows))
	for slug := range rows {
		slugs = append(slugs, slug)
	}
	sortStrings(slugs)
	data := make([][]driver.Value, 0, len(slugs))
	for _, slug := range slugs {
		data = append(data, []driver.Value{project(projection, rows[slug])})
	}
	return &memRows{cols: []string{projection}, data: data}, nil
}

func project(column string, row memRow) driver.Value {
	switch column {
	case "name":
		return row.name
	case "body":
		return row.body
	default:
		return int64(1) // SELECT 1 existence probe
	}
}

type memResult struct{}

func (memResult) LastInsertId() (int64, error) { return 0, nil }
func (memResult) RowsAffected() (int64, error) { return 0, nil }

type memRows struct {
	cols []string
	data [][]driver.Value
	i    int
}

func (r *memRows) Columns() []string { return r.cols }
func (r *memRows) Close() error      { return nil }
func (r *memRows) Next(dest []driver.Value) error {
	if r.i >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.i])
	r.i++
	return nil
}

// sqlFields tokenizes a statement, turning punctuation into separators so a
// table name is always its own token.
func sqlFields(query string) []string {
	return strings.Fields(strings.NewReplacer("(", " ", ")", " ", ",", " ").Replace(query))
}

// after returns the token following keyword (case-insensitive), or "".
func after(fields []string, keyword string) string {
	for i, f := range fields {
		if strings.EqualFold(f, keyword) && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

func asString(args []driver.Value, i int) string {
	if i >= len(args) {
		return ""
	}
	if s, ok := args[i].(string); ok {
		return s
	}
	return fmt.Sprint(args[i])
}
