package sqlite

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"fmt"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// CreateExecution implements memory.Store. Child links go first: the schema
// accepts them only for a parent that does not exist yet (ADR-0011).
func (s *Store) CreateExecution(ctx context.Context, e memory.Execution) error {
	var bindingID, bindingRevision any
	if e.BindingID != "" {
		bindingID, bindingRevision = e.BindingID, e.BindingRevision
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		for i, c := range e.Children {
			var parent string
			err := tx.QueryRowContext(ctx,
				`SELECT parent_execution_id FROM execution_children WHERE child_execution_id = ?`, c.ExecutionID).Scan(&parent)
			if err == nil {
				return &memory.LinkedChildError{Index: i, ParentID: parent}
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("read parent of child %d: %w", i, err)
			}
			_, err = tx.ExecContext(ctx, `
				INSERT INTO execution_children
					(parent_execution_id, parent_procedure_id, parent_version, parent_repository, parent_commit_hash,
					 position, reference, child_execution_id)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				e.ID, e.ProcedureID, e.Version, e.Repository, e.Commit, i, c.Reference, c.ExecutionID)
			if err != nil {
				return fmt.Errorf("insert child %d: %w", i, err)
			}
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO executions
				(id, procedure_id, version, binding_id, binding_revision, repository, commit_hash,
				 environment_name, environment_attributes, inputs, outcome, evidence, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.ProcedureID, e.Version, bindingID, bindingRevision, e.Repository, e.Commit,
			e.Environment.Name, string(e.Environment.Attributes), string(e.Inputs), string(e.Outcome),
			string(e.Evidence), formatTime(e.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert execution: %w", err)
		}
		return nil
	})
}

const executionColumns = `id, procedure_id, version, binding_id, binding_revision, repository, commit_hash,
	environment_name, environment_attributes, outcome, created_at`

// Execution implements memory.Store.
func (s *Store) Execution(ctx context.Context, id string) (memory.Execution, error) {
	return readExecution(ctx, s.db, id, true)
}

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// readExecution reads an execution and its child links in one statement:
// one row per child, or one row without children. Without withEvidence,
// Evidence is left empty.
func readExecution(ctx context.Context, q querier, id string, withEvidence bool) (memory.Execution, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+executionColumns+`, inputs, CASE WHEN ? THEN evidence END,
		c.reference, c.child_execution_id
		FROM executions LEFT JOIN execution_children AS c ON c.parent_execution_id = executions.id
		WHERE id = ? ORDER BY c.position`, withEvidence, id)
	if err != nil {
		return memory.Execution{}, fmt.Errorf("query execution: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var e memory.Execution
	found := false
	for rows.Next() {
		var inputs, evidence []byte
		var reference, child sql.NullString
		row, err := scanExecution(rows, &inputs, &evidence, &reference, &child)
		if err != nil {
			return memory.Execution{}, err
		}
		if !found {
			e, found = row, true
			e.Inputs, e.Evidence = jsontext.Value(inputs), jsontext.Value(evidence)
		}
		if child.Valid {
			e.Children = append(e.Children, memory.ChildExecution{Reference: reference.String, ExecutionID: child.String})
		}
	}
	if err := rows.Err(); err != nil {
		return memory.Execution{}, fmt.Errorf("read execution: %w", err)
	}
	if !found {
		return memory.Execution{}, memory.ErrNotFound
	}
	return e, nil
}

// ListExecutions implements memory.Store.
func (s *Store) ListExecutions(ctx context.Context, f memory.ExecutionFilter) ([]memory.Execution, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+executionColumns+` FROM executions
		WHERE procedure_id = ? AND (? = 0 OR version = ?) AND (? = '' OR repository = ?)
		ORDER BY created_at, id`,
		f.ProcedureID, f.Version, f.Version, f.Repository, f.Repository)
	if err != nil {
		return nil, fmt.Errorf("query executions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	executions := []memory.Execution{}
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		executions = append(executions, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read executions: %w", err)
	}
	return executions, nil
}

// ExecutionVerification implements memory.Store. Verification takes several
// queries, so it runs in one transaction to read a single snapshot.
func (s *Store) ExecutionVerification(ctx context.Context, id string) (memory.Verification, error) {
	var v memory.Verification
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		v, err = memory.VerifyExecution(ctx, txRuns{tx}, id)
		return err
	})
	return v, err
}

// Verifications implements memory.Store, in one transaction like
// ExecutionVerification.
func (s *Store) Verifications(ctx context.Context, f memory.VerificationFilter) ([]memory.CombinationStatus, error) {
	var statuses []memory.CombinationStatus
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		statuses, err = memory.ListCombinations(ctx, txRuns{tx}, f)
		return err
	})
	return statuses, err
}

// txRuns implements memory.VerificationReader inside one transaction.
type txRuns struct {
	tx *sql.Tx
}

func (r txRuns) Run(ctx context.Context, id string) (memory.Execution, error) {
	return readExecution(ctx, r.tx, id, false)
}

func (r txRuns) RunIDs(ctx context.Context, f memory.VerificationFilter) ([]string, error) {
	return queryStrings(ctx, r.tx, `SELECT id FROM executions
		WHERE procedure_id = ? AND version = ? AND (? = '' OR repository = ?)
		  AND (? = '' OR commit_hash = ?) AND (? = '' OR environment_name = ?)
		ORDER BY created_at, id`,
		f.ProcedureID, f.Version, f.Repository, f.Repository, f.Commit, f.Commit, f.Environment, f.Environment)
}

func (r txRuns) ReferenceNames(ctx context.Context, procedureID string, version int) ([]string, error) {
	return queryStrings(ctx, r.tx, `SELECT name FROM procedure_version_references
		WHERE procedure_id = ? AND version = ? ORDER BY position`, procedureID, version)
}

func (r txRuns) LatestRuns(ctx context.Context, procedureID string, c memory.ResolutionContext) ([]memory.VersionRun, error) {
	rows, err := r.tx.QueryContext(ctx, `SELECT version, id FROM executions
		WHERE procedure_id = ? AND repository = ? AND environment_name = ?
		ORDER BY version DESC, created_at DESC, id DESC`, procedureID, c.Repository, c.Environment)
	if err != nil {
		return nil, fmt.Errorf("query latest runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var runs []memory.VersionRun
	for rows.Next() {
		var run memory.VersionRun
		if err := rows.Scan(&run.Version, &run.ExecutionID); err != nil {
			return nil, fmt.Errorf("scan latest runs: %w", err)
		}
		// Rows are newest first within a version, so the first one is the latest.
		if len(runs) == 0 || runs[len(runs)-1].Version != run.Version {
			runs = append(runs, run)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read latest runs: %w", err)
	}
	return runs, nil
}

// queryStrings returns the single text column of every row.
func queryStrings(ctx context.Context, q querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read rows: %w", err)
	}
	return out, nil
}

// scanExecution reads executionColumns, followed by any extra destinations.
func scanExecution(row interface{ Scan(...any) error }, extra ...any) (memory.Execution, error) {
	var e memory.Execution
	var bindingID sql.NullString
	var bindingRevision sql.NullInt64
	var attributes []byte
	var outcome, created string
	dest := append([]any{&e.ID, &e.ProcedureID, &e.Version, &bindingID, &bindingRevision, &e.Repository, &e.Commit,
		&e.Environment.Name, &attributes, &outcome, &created}, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return memory.Execution{}, err
		}
		return memory.Execution{}, fmt.Errorf("scan execution: %w", err)
	}
	e.BindingID, e.BindingRevision = bindingID.String, int(bindingRevision.Int64)
	e.Environment.Attributes = jsontext.Value(attributes)
	e.Outcome = memory.Outcome(outcome)
	var err error
	if e.CreatedAt, err = parseTime(created); err != nil {
		return memory.Execution{}, err
	}
	return e, nil
}
