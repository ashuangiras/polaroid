// Package sqlite implements memory.Store on a single SQLite database file.
//
// Every write runs in one transaction that takes SQLite's write lock when it
// begins, so concurrent writers queue instead of interleaving. Every read is a
// single statement, so it observes one consistent snapshot. Schema triggers
// reject updates and deletes of stored records, and gaps in version and
// revision numbers.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	sqlitedriver "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// Driver parameters appended to the database path; see modernc.org/sqlite
// Driver.Open. _txlock=immediate makes BEGIN take the write lock, and
// _busy_timeout makes a waiting writer queue for up to 5s instead of failing.
const dsnParams = "?_txlock=immediate&_busy_timeout=5000&_foreign_keys=on&_journal_mode=wal&_synchronous=full"

// timeLayout stores UTC timestamps as fixed-width text, so they sort and
// round-trip exactly.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// Store is a memory.Store backed by one SQLite database file.
type Store struct {
	db *sql.DB
}

var _ memory.Store = (*Store)(nil)

// Open opens the database file at path, creating it if needed, and applies any
// pending schema migrations. It refuses a database whose schema is newer than
// this build understands.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" || strings.HasPrefix(path, ":") || strings.HasPrefix(path, "file:") || strings.Contains(path, "?") {
		return nil, fmt.Errorf("database path %q must be a plain file path", path)
	}
	db, err := sql.Open("sqlite", path+dsnParams)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("prepare database %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close releases the database. Stored records remain on disk.
func (s *Store) Close() error {
	return s.db.Close()
}

// Ping reports whether the database can be reached.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// CreateProcedure implements memory.Store.
func (s *Store) CreateProcedure(ctx context.Context, p memory.Procedure, first memory.Version) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO procedures (id, canonical_key, created_at) VALUES (?, ?, ?)`,
			p.ID, p.CanonicalKey, formatTime(p.CreatedAt))
		// canonical_key is the only UNIQUE constraint on procedures; a
		// duplicate id would be a PRIMARYKEY violation instead.
		if resultCode(err) == sqlitelib.SQLITE_CONSTRAINT_UNIQUE {
			return memory.ErrCanonicalKeyExists
		}
		if err != nil {
			return fmt.Errorf("insert procedure: %w", err)
		}
		if p.Origin != nil {
			if err := insertOrigin(ctx, tx, p.ID, *p.Origin); err != nil {
				return err
			}
		}
		return insertVersion(ctx, tx, first)
	})
}

// SetOrigin implements memory.Store.
func (s *Store) SetOrigin(ctx context.Context, procedureID string, o memory.Origin) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var exists, recorded bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM procedures WHERE id = ?),
			EXISTS (SELECT 1 FROM procedure_origins WHERE procedure_id = ?)`, procedureID, procedureID).Scan(&exists, &recorded)
		switch {
		case err != nil:
			return fmt.Errorf("read procedure: %w", err)
		case !exists:
			return memory.ErrNotFound
		case recorded:
			return memory.ErrOriginExists
		}
		return insertOrigin(ctx, tx, procedureID, o)
	})
}

func insertOrigin(ctx context.Context, tx *sql.Tx, procedureID string, o memory.Origin) error {
	if err := checkRepositoryExists(ctx, tx, o.RepositoryID, "origin.repository_id"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO procedure_origins (procedure_id, repository_id, reason, created_at) VALUES (?, ?, ?, ?)`,
		procedureID, o.RepositoryID, o.Reason, formatTime(o.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert origin: %w", err)
	}
	return nil
}

// checkRepositoryExists returns a *memory.ValidationError naming field if no
// repository has id. Repositories are never deleted, so the answer holds for
// the transaction.
func checkRepositoryExists(ctx context.Context, tx *sql.Tx, id, field string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM repositories WHERE id = ?)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("read repository: %w", err)
	}
	if !exists {
		return &memory.ValidationError{Problems: []memory.FieldProblem{{Field: field, Message: fmt.Sprintf("repository %q is not registered", id)}}}
	}
	return nil
}

// AppendVersion implements memory.Store.
func (s *Store) AppendVersion(ctx context.Context, baseVersion int, next memory.Version) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var latest sql.NullInt64
		err := tx.QueryRowContext(ctx,
			`SELECT MAX(version) FROM procedure_versions WHERE procedure_id = ?`,
			next.ProcedureID).Scan(&latest)
		if err != nil {
			return fmt.Errorf("read latest version: %w", err)
		}
		if !latest.Valid {
			return memory.ErrNotFound
		}
		if int(latest.Int64) != baseVersion {
			return &memory.VersionConflictError{BaseVersion: baseVersion, LatestVersion: int(latest.Int64)}
		}
		return insertVersion(ctx, tx, next)
	})
}

// ListProcedures implements memory.Store: the latest version decides goal,
// applicability and the scope and repository filters. Every row read is at
// or below the snapshot's boundary, which is unbounded for a live read.
func (s *Store) ListProcedures(ctx context.Context, f memory.ProcedureFilter) ([]memory.Procedure, error) {
	marks, err := boundary(f.Snapshot, 4)
	if err != nil {
		return nil, err
	}
	afterKey := ""
	if f.After != nil {
		afterKey = f.After.Key
	}
	scope := f.Scope
	if scope == "unspecified" {
		scope = "none"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.canonical_key, p.created_at, v.version, v.goal, v.scope, v.scope_repository_id,
		       o.repository_id, o.reason, o.created_at
		FROM procedures AS p
		JOIN procedure_versions AS v ON v.procedure_id = p.id
		     AND v.version = (SELECT MAX(pv.version) FROM procedure_versions AS pv
		                      WHERE pv.procedure_id = p.id AND pv.rowid <= ?)
		LEFT JOIN procedure_origins AS o ON o.procedure_id = p.id AND o.rowid <= ?
		WHERE p.rowid <= ?
		  AND (? = '' OR p.canonical_key > ?)
		  AND (? = '' OR COALESCE(v.scope, 'none') = ?)
		  AND (? = '' OR v.scope IS NOT 'local' OR v.scope_repository_id = (
		       SELECT ri.repository_id FROM repository_identifiers AS ri WHERE ri.identifier = ? AND ri.rowid <= ?))
		  AND (? = '' OR instr(p.canonical_key, ?) > 0 OR instr(lower(COALESCE(v.goal, '')), ?) > 0)
		ORDER BY p.canonical_key
		LIMIT ?`,
		marks[1], marks[2], marks[0], afterKey, afterKey, scope, scope, f.Repository, f.Repository, marks[3],
		f.Query, f.Query, f.Query, fetchLimit(f.Page.Limit))
	if err != nil {
		return nil, fmt.Errorf("query procedures: %w", err)
	}
	defer func() { _ = rows.Close() }()

	procedures := []memory.Procedure{}
	for rows.Next() {
		var p memory.Procedure
		var created string
		var goal, scope, scopeRepository, originRepository, originReason, originCreated sql.NullString
		if err := rows.Scan(&p.ID, &p.CanonicalKey, &created, &p.LatestVersion, &goal, &scope, &scopeRepository,
			&originRepository, &originReason, &originCreated); err != nil {
			return nil, fmt.Errorf("scan procedure: %w", err)
		}
		if p.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		p.Goal, p.Applicability = goal.String, applicability(scope, scopeRepository)
		if p.Origin, err = origin(originRepository, originReason, originCreated); err != nil {
			return nil, err
		}
		procedures = append(procedures, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read procedures: %w", err)
	}
	return procedures, nil
}

// fetchLimit is the SQL LIMIT for a page: one extra item, so the service can
// tell whether more exist, or -1 (no limit) for a complete list.
func fetchLimit(limit int) int {
	if limit == 0 {
		return -1
	}
	return limit + 1
}

// Snapshot boundaries (ADR-0023) are row ID high-water marks. The tables
// they cover are append-only (triggers reject UPDATE and DELETE) and writes
// are serialized (dsnParams), so row IDs grow in commit order and a mark
// includes exactly the rows committed before it was read.
const (
	procedureSnapshot = `SELECT
		(SELECT COALESCE(MAX(rowid), 0) FROM procedures),
		(SELECT COALESCE(MAX(rowid), 0) FROM procedure_versions),
		(SELECT COALESCE(MAX(rowid), 0) FROM procedure_origins),
		(SELECT COALESCE(MAX(rowid), 0) FROM repository_identifiers)`
	bindingSnapshot = `SELECT
		(SELECT COALESCE(MAX(rowid), 0) FROM bindings),
		(SELECT COALESCE(MAX(rowid), 0) FROM binding_revisions),
		(SELECT COALESCE(MAX(rowid), 0) FROM repository_identifiers)`
)

// ProcedureSnapshot implements memory.Store: procedures, versions, origins
// and repository identifiers, read in one statement.
func (s *Store) ProcedureSnapshot(ctx context.Context) (memory.Snapshot, error) {
	marks := make(memory.Snapshot, 4)
	if err := s.db.QueryRowContext(ctx, procedureSnapshot).Scan(&marks[0], &marks[1], &marks[2], &marks[3]); err != nil {
		return nil, fmt.Errorf("read procedure snapshot: %w", err)
	}
	return marks, nil
}

// BindingSnapshot implements memory.Store: bindings, binding revisions and
// repository identifiers, read in one statement.
func (s *Store) BindingSnapshot(ctx context.Context) (memory.Snapshot, error) {
	marks := make(memory.Snapshot, 3)
	if err := s.db.QueryRowContext(ctx, bindingSnapshot).Scan(&marks[0], &marks[1], &marks[2]); err != nil {
		return nil, fmt.Errorf("read binding snapshot: %w", err)
	}
	return marks, nil
}

// boundary returns the n marks of snapshot, each unbounded for a live read,
// or memory.ErrInvalidSnapshot if snapshot has another length.
func boundary(snapshot memory.Snapshot, n int) (memory.Snapshot, error) {
	if snapshot == nil {
		marks := make(memory.Snapshot, n)
		for i := range marks {
			marks[i] = math.MaxInt64
		}
		return marks, nil
	}
	if len(snapshot) != n {
		return nil, memory.ErrInvalidSnapshot
	}
	return snapshot, nil
}

func applicability(scope, repositoryID sql.NullString) memory.Applicability {
	if !scope.Valid {
		return memory.Applicability{}
	}
	return memory.Applicability{Kind: memory.ScopeKind(scope.String), RepositoryID: repositoryID.String}
}

func origin(repositoryID, reason, created sql.NullString) (*memory.Origin, error) {
	if !repositoryID.Valid {
		return nil, nil
	}
	at, err := parseTime(created.String)
	if err != nil {
		return nil, err
	}
	return &memory.Origin{RepositoryID: repositoryID.String, Reason: reason.String, CreatedAt: at}, nil
}

const historySelect = `
	SELECT p.id, p.canonical_key, p.created_at,
	       o.repository_id, o.reason, o.created_at,
	       v.version, v.philosophy, v.method, v.goal, v.scope, v.scope_repository_id,
	       v.contract, v.instructions, v.revision_reason, v.created_at,
	       r.name, r.target_procedure_id, r.policy, r.pinned_version, r.inputs
	FROM procedures AS p
	JOIN procedure_versions AS v ON v.procedure_id = p.id
	LEFT JOIN procedure_origins AS o ON o.procedure_id = p.id
	LEFT JOIN procedure_version_references AS r ON r.procedure_id = v.procedure_id AND r.version = v.version
`

// History implements memory.Store.
func (s *Store) History(ctx context.Context, id string) (memory.History, error) {
	return s.history(ctx, historySelect+`WHERE p.id = ? ORDER BY v.version, r.position`, id)
}

// HistoryByKey implements memory.Store.
func (s *Store) HistoryByKey(ctx context.Context, canonicalKey string) (memory.History, error) {
	return s.history(ctx, historySelect+`WHERE p.canonical_key = ? ORDER BY v.version, r.position`, canonicalKey)
}

// history reads historySelect rows: one per reference, or one per version
// without references, ordered by version and reference position.
func (s *Store) history(ctx context.Context, query string, args ...any) (memory.History, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return memory.History{}, fmt.Errorf("query history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var h memory.History
	var procedureCreated string
	var originRepository, originReason, originCreated sql.NullString
	for rows.Next() {
		var v memory.Version
		var contract, instructions, refInputs []byte
		var versionCreated string
		var goal, scope, scopeRepository sql.NullString
		var refName, refTarget, refPolicy sql.NullString
		var refPin sql.NullInt64
		if err := rows.Scan(&h.Procedure.ID, &h.Procedure.CanonicalKey, &procedureCreated,
			&originRepository, &originReason, &originCreated,
			&v.Number, &v.Philosophy, &v.Method, &goal, &scope, &scopeRepository,
			&contract, &instructions, &v.RevisionReason, &versionCreated,
			&refName, &refTarget, &refPolicy, &refPin, &refInputs); err != nil {
			return memory.History{}, fmt.Errorf("scan version: %w", err)
		}
		if n := len(h.Versions); n == 0 || h.Versions[n-1].Number != v.Number {
			v.ProcedureID = h.Procedure.ID
			v.Goal, v.Applicability = goal.String, applicability(scope, scopeRepository)
			v.Contract = jsontext.Value(contract)
			v.Instructions = jsontext.Value(instructions)
			if v.CreatedAt, err = parseTime(versionCreated); err != nil {
				return memory.History{}, err
			}
			h.Versions = append(h.Versions, v)
		}
		if refName.Valid {
			last := &h.Versions[len(h.Versions)-1]
			last.References = append(last.References, memory.Reference{
				Name:          refName.String,
				ProcedureID:   refTarget.String,
				VersionPolicy: memory.VersionPolicy{Kind: memory.PolicyKind(refPolicy.String), Pin: int(refPin.Int64)},
				Inputs:        jsontext.Value(refInputs),
			})
		}
	}
	if err := rows.Err(); err != nil {
		return memory.History{}, fmt.Errorf("read history: %w", err)
	}
	// Every procedure is created together with version 1, so no rows means
	// no procedure.
	if len(h.Versions) == 0 {
		return memory.History{}, memory.ErrNotFound
	}
	if h.Procedure.CreatedAt, err = parseTime(procedureCreated); err != nil {
		return memory.History{}, err
	}
	if h.Procedure.Origin, err = origin(originRepository, originReason, originCreated); err != nil {
		return memory.History{}, err
	}
	latest := h.Versions[len(h.Versions)-1]
	h.Procedure.LatestVersion = latest.Number
	h.Procedure.Goal, h.Procedure.Applicability = latest.Goal, latest.Applicability
	return h, nil
}

// Version implements memory.Store.
func (s *Store) Version(ctx context.Context, procedureID string, number int) (memory.Version, error) {
	h, err := s.history(ctx, historySelect+`WHERE p.id = ? AND v.version = ? ORDER BY r.position`, procedureID, number)
	if err != nil {
		return memory.Version{}, err
	}
	return h.Versions[0], nil
}

// write runs fn in a transaction that holds SQLite's write lock from the start
// (see dsnParams) and commits only if fn succeeds.
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin write: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit write: %w", err)
	}
	return nil
}

// insertVersion stores v and its references. References go first: the schema
// accepts them only for a version that does not exist yet.
func insertVersion(ctx context.Context, tx *sql.Tx, v memory.Version) error {
	if err := checkTargets(ctx, tx, v.References); err != nil {
		return err
	}
	var scope, scopeRepository, goal any
	switch v.Applicability.Kind {
	case memory.ScopeShared:
		scope = string(memory.ScopeShared)
	case memory.ScopeLocal:
		scope, scopeRepository = string(memory.ScopeLocal), v.Applicability.RepositoryID
		if err := checkRepositoryExists(ctx, tx, v.Applicability.RepositoryID, "version.applicability.repository"); err != nil {
			return err
		}
	}
	if v.Goal != "" {
		goal = v.Goal
	}
	if err := memory.CheckReferenceScopes(ctx, txGraph{tx}, v); err != nil {
		return err
	}
	for i, r := range v.References {
		var pinned any
		if r.VersionPolicy.Kind == memory.PolicyPin {
			pinned = r.VersionPolicy.Pin
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO procedure_version_references
				(procedure_id, version, position, name, target_procedure_id, policy, pinned_version, inputs)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			v.ProcedureID, v.Number, i, r.Name, r.ProcedureID, string(r.VersionPolicy.Kind), pinned, string(r.Inputs))
		if err != nil {
			return fmt.Errorf("insert reference %q of version %d: %w", r.Name, v.Number, err)
		}
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO procedure_versions
			(procedure_id, version, philosophy, method, goal, scope, scope_repository_id,
			 contract, instructions, revision_reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ProcedureID, v.Number, v.Philosophy, v.Method, goal, scope, scopeRepository,
		string(v.Contract), string(v.Instructions), v.RevisionReason, formatTime(v.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert version %d: %w", v.Number, err)
	}
	if len(v.References) == 0 {
		return nil
	}
	// Walk the new version's graph as the transaction now sees it; a cycle or
	// an exceeded limit fails the write and rolls everything back (ADR-0009).
	_, err = memory.ExpandGraph(ctx, txGraph{tx}, v.ProcedureID, v.Number)
	return err
}

// Resolve implements memory.Store. The walk takes several queries, so it
// runs in one transaction to read a single snapshot.
func (s *Store) Resolve(ctx context.Context, procedureID string, policy memory.VersionPolicy, c memory.ResolutionContext) (memory.Resolution, error) {
	var res memory.Resolution
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		res, err = memory.ResolveGraph(ctx, txResolution{txGraph{tx}, txRuns{tx}}, procedureID, policy, c)
		return err
	})
	return res, err
}

// txResolution implements memory.ResolutionReader inside one transaction.
type txResolution struct {
	txGraph
	txRuns
}

// txGraph implements memory.GraphReader inside one transaction.
type txGraph struct {
	tx *sql.Tx
}

func (g txGraph) LatestVersion(ctx context.Context, procedureID string) (int, error) {
	var latest sql.NullInt64
	if err := g.tx.QueryRowContext(ctx,
		`SELECT MAX(version) FROM procedure_versions WHERE procedure_id = ?`, procedureID).Scan(&latest); err != nil {
		return 0, fmt.Errorf("read latest version: %w", err)
	}
	if !latest.Valid {
		return 0, memory.ErrNotFound
	}
	return int(latest.Int64), nil
}

func (g txGraph) VersionNode(ctx context.Context, procedureID string, version int) (memory.NodeVersion, error) {
	rows, err := g.tx.QueryContext(ctx, `
		SELECT p.canonical_key, v.scope, v.scope_repository_id,
		       r.name, r.target_procedure_id, r.policy, r.pinned_version, r.inputs
		FROM procedures AS p
		JOIN procedure_versions AS v ON v.procedure_id = p.id
		LEFT JOIN procedure_version_references AS r ON r.procedure_id = v.procedure_id AND r.version = v.version
		WHERE p.id = ? AND v.version = ?
		ORDER BY r.position`, procedureID, version)
	if err != nil {
		return memory.NodeVersion{}, fmt.Errorf("query version references: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var node memory.NodeVersion
	found := false
	for rows.Next() {
		var scope, scopeRepository, name, target, policy sql.NullString
		var pin sql.NullInt64
		var inputs []byte
		if err := rows.Scan(&node.CanonicalKey, &scope, &scopeRepository, &name, &target, &policy, &pin, &inputs); err != nil {
			return memory.NodeVersion{}, fmt.Errorf("scan version references: %w", err)
		}
		found = true
		node.Applicability = applicability(scope, scopeRepository)
		if name.Valid {
			node.References = append(node.References, memory.Reference{
				Name:          name.String,
				ProcedureID:   target.String,
				VersionPolicy: memory.VersionPolicy{Kind: memory.PolicyKind(policy.String), Pin: int(pin.Int64)},
				Inputs:        jsontext.Value(inputs),
			})
		}
	}
	if err := rows.Err(); err != nil {
		return memory.NodeVersion{}, fmt.Errorf("read version references: %w", err)
	}
	if !found {
		return memory.NodeVersion{}, memory.ErrNotFound
	}
	return node, nil
}

// checkTargets returns a *memory.MissingTargetsError listing every reference
// whose target procedure, or pinned version of it, does not exist. Procedures
// and versions are never deleted, so the answer holds for the transaction.
func checkTargets(ctx context.Context, tx *sql.Tx, refs []memory.Reference) error {
	var missing []memory.MissingTarget
	for i, r := range refs {
		var procedureExists, versionExists bool
		err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM procedures WHERE id = ?),
			       EXISTS (SELECT 1 FROM procedure_versions WHERE procedure_id = ? AND version = ?)`,
			r.ProcedureID, r.ProcedureID, r.VersionPolicy.Pin).Scan(&procedureExists, &versionExists)
		if err != nil {
			return fmt.Errorf("read reference target: %w", err)
		}
		switch {
		case !procedureExists:
			missing = append(missing, memory.MissingTarget{Index: i, Procedure: true})
		case r.VersionPolicy.Kind == memory.PolicyPin && !versionExists:
			missing = append(missing, memory.MissingTarget{Index: i})
		}
	}
	if len(missing) > 0 {
		return &memory.MissingTargetsError{Missing: missing}
	}
	return nil
}

// resultCode returns the extended SQLite result code of err, or 0.
func resultCode(err error) int {
	var serr *sqlitedriver.Error
	if errors.As(err, &serr) {
		return serr.Code()
	}
	return 0
}

func formatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("stored timestamp %q: %w", s, err)
	}
	return t, nil
}
