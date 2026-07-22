package ear

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// SQLBackend is a CatalogueBackend over any database/sql driver -- the
// db-agnostic catalogue.
//
// database/sql is itself the driver-agnostic wrapper: the caller opens a
// *sql.DB with whatever driver they registered (a blank import of a Postgres,
// SQLite, MySQL, … driver in their own code), and this backend speaks only the
// portable subset of SQL to it. So the same catalogue -- the same Skills,
// Personas, Workflows, Processes and Policies, with the same cross-reference
// resolution and the same governed outcomes -- lives in any SQL database
// without a line of it changing, and this module keeps its zero third-party
// dependencies: the driver is the caller's import, never ours.
//
// One table holds one kind, three columns: the slug (an address, folded the
// same way every cross-reference in the stack is), the object's name, and its
// markdown body -- the exact bytes the loader parses. Nothing is stored that
// the file backend would not; a row is a store file that happens to live in a
// database.
//
// Portability, deliberately:
//
//   - Values are always parameterized; the table name (which cannot be a
//     parameter) is validated as a plain identifier, so nothing off the wire
//     reaches the query text.
//   - Upsert is a delete-then-insert inside a transaction, not an
//     INSERT … ON CONFLICT / REPLACE / MERGE -- those spellings differ per
//     database, and delete+insert is the one every SQL database agrees on.
//   - The placeholder style is configurable (`?` by default, `$N` for
//     Postgres via the Postgres option), because that too differs per driver.
//   - The schema DDL is overridable, and can be skipped entirely for a
//     database whose tables a deployment provisions itself.
type SQLBackend struct {
	db     *sql.DB
	table  string
	ph     func(n int) string
	schema string // CREATE statement template; %s is the table name. "" skips init.
}

// validSQLIdentifier guards the one piece of a query that cannot be
// parameterized -- the table name.
var validSQLIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// defaultSchema is portable across SQLite, Postgres and MySQL: a VARCHAR key
// (MySQL will not key a TEXT column without a length) and TEXT payloads.
const defaultSchema = "CREATE TABLE IF NOT EXISTS %s (slug VARCHAR(255) PRIMARY KEY, name TEXT NOT NULL, body TEXT NOT NULL)"

// SQLOption configures a SQLBackend.
type SQLOption func(*SQLBackend)

// WithPlaceholders sets how positional parameters are rendered. The default is
// `?` (SQLite, MySQL); Postgres wants `$1`, `$2`, ….
func WithPlaceholders(render func(n int) string) SQLOption {
	return func(b *SQLBackend) { b.ph = render }
}

// Postgres renders `$N` placeholders, the pq/pgx convention.
var Postgres = WithPlaceholders(func(n int) string { return "$" + strconv.Itoa(n) })

// WithSchema overrides the CREATE statement used to provision the table. The
// template's single %s is the table name -- for a database whose type names or
// key syntax differ from the portable default.
func WithSchema(ddlTemplate string) SQLOption {
	return func(b *SQLBackend) { b.schema = ddlTemplate }
}

// WithoutSchemaInit skips table creation entirely, for a schema the deployment
// manages itself (migrations, least-privilege grants, and the like).
func WithoutSchemaInit() SQLOption {
	return func(b *SQLBackend) { b.schema = "" }
}

// NewSQLBackend opens a catalogue backend over db, storing one kind in table.
// It provisions the table unless WithoutSchemaInit is given.
func NewSQLBackend(db *sql.DB, table string, opts ...SQLOption) (*SQLBackend, error) {
	if db == nil {
		return nil, errors.New("NewSQLBackend needs an open *sql.DB")
	}
	if !validSQLIdentifier.MatchString(table) {
		return nil, fmt.Errorf("invalid catalogue table name %q -- must be a plain SQL identifier", table)
	}
	b := &SQLBackend{
		db:     db,
		table:  table,
		ph:     func(n int) string { return "?" },
		schema: defaultSchema,
	}
	for _, opt := range opts {
		opt(b)
	}
	if b.schema != "" {
		if _, err := db.Exec(fmt.Sprintf(b.schema, table)); err != nil {
			return nil, fmt.Errorf("provisioning catalogue table %q: %w", table, err)
		}
	}
	return b, nil
}

// SQLLibrary opens a whole backend-agnostic library in one database: five
// tables (skills, policies, personas, workflows, processes) under an optional
// prefix, then assembles them through the same NewLibrary the file path uses.
//
// This is the db-agnostic library the file OpenLibrary mirrors: the resolution
// order and cross-reference wiring are identical, so a catalogue means the same
// thing whether it lives on disk or in Postgres.
func SQLLibrary(db *sql.DB, prefix string, opts ...SQLOption) (*Library, error) {
	if prefix != "" && !validSQLIdentifier.MatchString(prefix) {
		return nil, fmt.Errorf("invalid catalogue table prefix %q", prefix)
	}
	backend := func(kind string) (CatalogueBackend, error) {
		table := kind
		if prefix != "" {
			table = prefix + "_" + kind
		}
		return NewSQLBackend(db, table, opts...)
	}

	var stores LibraryBackends
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
		b, err := backend(spec.kind)
		if err != nil {
			return nil, err
		}
		*spec.into = b
	}
	return NewLibrary(stores)
}

// -- the CatalogueBackend contract -------------------------------------------

// List returns the catalogued names, ordered by slug so the listing is stable
// whatever order rows were written -- the name column carries what each
// object's heading said, exactly as the file backend reads from the heading.
func (b *SQLBackend) List() ([]string, error) {
	rows, err := b.db.Query("SELECT name FROM " + b.table + " ORDER BY slug")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// Exists reports whether a name is catalogued.
func (b *SQLBackend) Exists(name string) (bool, error) {
	row := b.db.QueryRow("SELECT 1 FROM "+b.table+" WHERE slug = "+b.ph(1), slug(name))
	var one int
	switch err := row.Scan(&one); {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, err
	default:
		return true, nil
	}
}

// Read returns a catalogued object's markdown, failing loudly with what is
// catalogued when a name is absent -- the same discipline the file backend and
// the loader apply to an unresolved reference.
func (b *SQLBackend) Read(name string) (string, error) {
	row := b.db.QueryRow("SELECT body FROM "+b.table+" WHERE slug = "+b.ph(1), slug(name))
	var body string
	switch err := row.Scan(&body); {
	case err == sql.ErrNoRows:
		known, listErr := b.List()
		if listErr != nil {
			return "", fmt.Errorf("%q is not in catalogue table %s", name, b.table)
		}
		catalogued := strings.Join(known, ", ")
		if catalogued == "" {
			catalogued = "none"
		}
		return "", fmt.Errorf("%q is not in catalogue table %s -- catalogued: %s", name, b.table, catalogued)
	case err != nil:
		return "", err
	default:
		return body, nil
	}
}

// Write upserts an object by slug. The upsert is a delete-then-insert inside a
// transaction rather than a dialect-specific ON CONFLICT / REPLACE / MERGE:
// delete+insert is the spelling every SQL database agrees on, so this one path
// works on all of them.
func (b *SQLBackend) Write(name, text string) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	// Roll back on any early return; a no-op after a successful Commit.
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM "+b.table+" WHERE slug = "+b.ph(1), slug(name)); err != nil {
		return err
	}
	insert := "INSERT INTO " + b.table + " (slug, name, body) VALUES (" +
		b.ph(1) + ", " + b.ph(2) + ", " + b.ph(3) + ")"
	if _, err := tx.Exec(insert, slug(name), name, text); err != nil {
		return err
	}
	return tx.Commit()
}

// Delete removes a catalogued object. Deleting one that is not there is not an
// error -- the postcondition the caller wanted already holds, matching the
// file backend.
func (b *SQLBackend) Delete(name string) error {
	_, err := b.db.Exec("DELETE FROM "+b.table+" WHERE slug = "+b.ph(1), slug(name))
	return err
}
