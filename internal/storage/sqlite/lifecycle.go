package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// lifecycleStatus is the current lifecycle state of the procedure whose ID
// is the SQL expression id: its latest event's status, or active
// (ADR-0035).
func lifecycleStatus(id string) string {
	return `COALESCE((SELECT le.status FROM procedure_lifecycle_events AS le
		WHERE le.procedure_id = ` + id + ` ORDER BY le.event DESC LIMIT 1), 'active')`
}

// isRetired reports whether a procedure is retired now. Lifecycle events
// are appended only under the write lock, so the answer holds for the
// transaction.
func isRetired(ctx context.Context, tx *sql.Tx, procedureID string) (bool, error) {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT `+lifecycleStatus("?"), procedureID).Scan(&status); err != nil {
		return false, fmt.Errorf("read lifecycle status: %w", err)
	}
	return status == string(memory.LifecycleRetired), nil
}

// ProcedureLifecycle implements memory.Store.
func (s *Store) ProcedureLifecycle(ctx context.Context, procedureID string) (memory.Lifecycle, error) {
	return readLifecycle(ctx, s.db, procedureID)
}

// AppendLifecycleEvent implements memory.Store.
func (s *Store) AppendLifecycleEvent(ctx context.Context, baseEvent int, e memory.LifecycleEvent) (memory.Lifecycle, error) {
	var l memory.Lifecycle
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		if l, err = readLifecycle(ctx, tx, e.ProcedureID); err != nil {
			return err
		}
		if latest := l.LatestEvent(); latest != baseEvent {
			return &memory.LifecycleConflictError{BaseEvent: baseEvent, LatestEvent: latest}
		}
		if l.Status() == e.Status {
			return memory.ErrLifecycleUnchanged
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO procedure_lifecycle_events (procedure_id, event, status, reason, actor, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, e.ProcedureID, e.Event, string(e.Status), e.Reason, e.Actor, formatTime(e.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert lifecycle event: %w", err)
		}
		l.Events = append(l.Events, e)
		return nil
	})
	return l, err
}

// readLifecycle reads a procedure's events in one statement; a procedure
// without events reads as one row of NULLs.
func readLifecycle(ctx context.Context, q querier, procedureID string) (memory.Lifecycle, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.event, e.status, e.reason, e.actor, e.created_at
		FROM procedures AS p LEFT JOIN procedure_lifecycle_events AS e ON e.procedure_id = p.id
		WHERE p.id = ? ORDER BY e.event`, procedureID)
	if err != nil {
		return memory.Lifecycle{}, fmt.Errorf("query lifecycle: %w", err)
	}
	defer func() { _ = rows.Close() }()
	l := memory.Lifecycle{ProcedureID: procedureID, Events: []memory.LifecycleEvent{}}
	found := false
	for rows.Next() {
		found = true
		var event sql.NullInt64
		var status, reason, actor, created sql.NullString
		if err := rows.Scan(&event, &status, &reason, &actor, &created); err != nil {
			return memory.Lifecycle{}, fmt.Errorf("scan lifecycle: %w", err)
		}
		if !event.Valid {
			continue
		}
		e := memory.LifecycleEvent{ProcedureID: procedureID, Event: int(event.Int64), Status: memory.LifecycleStatus(status.String),
			Reason: reason.String, Actor: actor.String}
		if e.CreatedAt, err = parseTime(created.String); err != nil {
			return memory.Lifecycle{}, err
		}
		l.Events = append(l.Events, e)
	}
	if err := rows.Err(); err != nil {
		return memory.Lifecycle{}, fmt.Errorf("read lifecycle: %w", err)
	}
	if !found {
		return memory.Lifecycle{}, memory.ErrNotFound
	}
	return l, nil
}

// checkRetiredTargets returns a *memory.RetiredProcedureError listing every
// reference whose target procedure is retired.
func checkRetiredTargets(ctx context.Context, tx *sql.Tx, refs []memory.Reference) error {
	var retired []int
	for i, r := range refs {
		ok, err := isRetired(ctx, tx, r.ProcedureID)
		if err != nil {
			return err
		}
		if ok {
			retired = append(retired, i)
		}
	}
	if len(retired) > 0 {
		return &memory.RetiredProcedureError{References: retired}
	}
	return nil
}

// checkNotRetired returns a *memory.RetiredProcedureError if the procedure
// is retired.
func checkNotRetired(ctx context.Context, tx *sql.Tx, procedureID string) error {
	retired, err := isRetired(ctx, tx, procedureID)
	if err != nil {
		return err
	}
	if retired {
		return &memory.RetiredProcedureError{ProcedureID: procedureID}
	}
	return nil
}
