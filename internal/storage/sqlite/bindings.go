package sqlite

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"fmt"

	sqlitelib "modernc.org/sqlite/lib"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// CreateBinding implements memory.Store.
func (s *Store) CreateBinding(ctx context.Context, b memory.Binding, first memory.BindingRevision) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM procedures WHERE id = ?)`, b.ProcedureID).Scan(&exists); err != nil {
			return fmt.Errorf("read procedure: %w", err)
		}
		if !exists {
			return memory.ErrNotFound
		}
		if err := checkPin(ctx, tx, b.ProcedureID, first.VersionPolicy); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO bindings (id, repository, name, procedure_id, created_at) VALUES (?, ?, ?, ?, ?)`,
			b.ID, b.Repository, b.Name, b.ProcedureID, formatTime(b.CreatedAt))
		// (repository, name) is the only UNIQUE constraint on bindings; a
		// duplicate id would be a PRIMARYKEY violation instead.
		if resultCode(err) == sqlitelib.SQLITE_CONSTRAINT_UNIQUE {
			return memory.ErrBindingExists
		}
		if err != nil {
			return fmt.Errorf("insert binding: %w", err)
		}
		return insertBindingRevision(ctx, tx, first)
	})
}

// AppendBindingRevision implements memory.Store.
func (s *Store) AppendBindingRevision(ctx context.Context, baseRevision int, next memory.BindingRevision) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var procedureID sql.NullString
		var latest sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			SELECT b.procedure_id, MAX(r.revision)
			FROM bindings AS b
			JOIN binding_revisions AS r ON r.binding_id = b.id
			WHERE b.id = ?`, next.BindingID).Scan(&procedureID, &latest)
		if err != nil {
			return fmt.Errorf("read latest revision: %w", err)
		}
		if !latest.Valid {
			return memory.ErrNotFound
		}
		if int(latest.Int64) != baseRevision {
			return &memory.RevisionConflictError{BaseRevision: baseRevision, LatestRevision: int(latest.Int64)}
		}
		if err := checkPin(ctx, tx, procedureID.String, next.VersionPolicy); err != nil {
			return err
		}
		return insertBindingRevision(ctx, tx, next)
	})
}

// ListBindings implements memory.Store.
func (s *Store) ListBindings(ctx context.Context, repository string) ([]memory.Binding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.id, b.repository, b.name, b.procedure_id, b.created_at, MAX(r.revision)
		FROM bindings AS b
		JOIN binding_revisions AS r ON r.binding_id = b.id
		WHERE b.repository = ?
		GROUP BY b.id
		ORDER BY b.name`, repository)
	if err != nil {
		return nil, fmt.Errorf("query bindings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	bindings := []memory.Binding{}
	for rows.Next() {
		var b memory.Binding
		var created string
		if err := rows.Scan(&b.ID, &b.Repository, &b.Name, &b.ProcedureID, &created, &b.LatestRevision); err != nil {
			return nil, fmt.Errorf("scan binding: %w", err)
		}
		if b.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read bindings: %w", err)
	}
	return bindings, nil
}

// BindingHistory implements memory.Store.
func (s *Store) BindingHistory(ctx context.Context, id string) (memory.BindingHistory, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.id, b.repository, b.name, b.procedure_id, b.created_at,
		       r.revision, r.inputs, r.policy, r.pinned_version, r.revision_reason, r.created_at
		FROM bindings AS b
		JOIN binding_revisions AS r ON r.binding_id = b.id
		WHERE b.id = ?
		ORDER BY r.revision`, id)
	if err != nil {
		return memory.BindingHistory{}, fmt.Errorf("query binding history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var h memory.BindingHistory
	var bindingCreated string
	for rows.Next() {
		var r memory.BindingRevision
		var inputs []byte
		var policy, revisionCreated string
		var pinned sql.NullInt64
		if err := rows.Scan(&h.Binding.ID, &h.Binding.Repository, &h.Binding.Name, &h.Binding.ProcedureID, &bindingCreated,
			&r.Number, &inputs, &policy, &pinned, &r.RevisionReason, &revisionCreated); err != nil {
			return memory.BindingHistory{}, fmt.Errorf("scan binding revision: %w", err)
		}
		r.BindingID = h.Binding.ID
		r.Inputs = jsontext.Value(inputs)
		r.VersionPolicy = memory.VersionPolicy{Kind: memory.PolicyKind(policy), Pin: int(pinned.Int64)}
		if r.CreatedAt, err = parseTime(revisionCreated); err != nil {
			return memory.BindingHistory{}, err
		}
		h.Revisions = append(h.Revisions, r)
	}
	if err := rows.Err(); err != nil {
		return memory.BindingHistory{}, fmt.Errorf("read binding history: %w", err)
	}
	// Every binding is created together with revision 1, so no rows means no
	// binding.
	if len(h.Revisions) == 0 {
		return memory.BindingHistory{}, memory.ErrNotFound
	}
	if h.Binding.CreatedAt, err = parseTime(bindingCreated); err != nil {
		return memory.BindingHistory{}, err
	}
	h.Binding.LatestRevision = h.Revisions[len(h.Revisions)-1].Number
	return h, nil
}

// BindingRevision implements memory.Store.
func (s *Store) BindingRevision(ctx context.Context, bindingID string, number int) (memory.BindingRevision, error) {
	r := memory.BindingRevision{BindingID: bindingID}
	var inputs []byte
	var policy, created string
	var pinned sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT revision, inputs, policy, pinned_version, revision_reason, created_at
		FROM binding_revisions
		WHERE binding_id = ? AND revision = ?`, bindingID, number).
		Scan(&r.Number, &inputs, &policy, &pinned, &r.RevisionReason, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return memory.BindingRevision{}, memory.ErrNotFound
	}
	if err != nil {
		return memory.BindingRevision{}, fmt.Errorf("query binding revision: %w", err)
	}
	r.Inputs = jsontext.Value(inputs)
	r.VersionPolicy = memory.VersionPolicy{Kind: memory.PolicyKind(policy), Pin: int(pinned.Int64)}
	if r.CreatedAt, err = parseTime(created); err != nil {
		return memory.BindingRevision{}, err
	}
	return r, nil
}

// checkPin returns memory.ErrPinnedVersionNotFound if p pins a version that
// procedureID does not have. Versions are never deleted, so the answer holds
// for the rest of the transaction.
func checkPin(ctx context.Context, tx *sql.Tx, procedureID string, p memory.VersionPolicy) error {
	if p.Kind != memory.PolicyPin {
		return nil
	}
	var exists bool
	err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM procedure_versions WHERE procedure_id = ? AND version = ?)`,
		procedureID, p.Pin).Scan(&exists)
	if err != nil {
		return fmt.Errorf("read pinned version: %w", err)
	}
	if !exists {
		return memory.ErrPinnedVersionNotFound
	}
	return nil
}

func insertBindingRevision(ctx context.Context, tx *sql.Tx, r memory.BindingRevision) error {
	var pinned any
	if r.VersionPolicy.Kind == memory.PolicyPin {
		pinned = r.VersionPolicy.Pin
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO binding_revisions
			(binding_id, revision, inputs, policy, pinned_version, revision_reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.BindingID, r.Number, string(r.Inputs), string(r.VersionPolicy.Kind), pinned,
		r.RevisionReason, formatTime(r.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert binding revision %d: %w", r.Number, err)
	}
	return nil
}
