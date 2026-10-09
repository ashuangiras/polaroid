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

// Execution implements memory.Store. It reads the execution and its child
// links in one statement: one row per child, or one row without children.
func (s *Store) Execution(ctx context.Context, id string) (memory.Execution, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+executionColumns+`, inputs, evidence, c.reference, c.child_execution_id
		FROM executions LEFT JOIN execution_children AS c ON c.parent_execution_id = executions.id
		WHERE id = ? ORDER BY c.position`, id)
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
