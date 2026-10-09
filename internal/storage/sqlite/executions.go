package sqlite

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"fmt"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// CreateExecution implements memory.Store.
func (s *Store) CreateExecution(ctx context.Context, e memory.Execution) error {
	var bindingID, bindingRevision any
	if e.BindingID != "" {
		bindingID, bindingRevision = e.BindingID, e.BindingRevision
	}
	return s.write(ctx, func(tx *sql.Tx) error {
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
	var inputs, evidence []byte
	row := s.db.QueryRowContext(ctx, `SELECT `+executionColumns+`, inputs, evidence FROM executions WHERE id = ?`, id)
	e, err := scanExecution(row, &inputs, &evidence)
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Execution{}, memory.ErrNotFound
	}
	if err != nil {
		return memory.Execution{}, err
	}
	e.Inputs = jsontext.Value(inputs)
	e.Evidence = jsontext.Value(evidence)
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
