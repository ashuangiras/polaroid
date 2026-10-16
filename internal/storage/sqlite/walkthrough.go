package sqlite

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"fmt"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// walkthroughColumns is shared by WalkthroughEvents and
// AllWalkthroughEvents, which scan the same row shape.
const walkthroughColumns = "token, goal, snapshot, repository, outcome_key, outcome_status, reason, note, context, created_at"

// AppendWalkthroughEvent implements memory.WalkthroughStore. Unlike every
// other write in this package, it needs no existence or conflict check:
// Walkthroughs.Advance already read the token's events first and decides
// whether to proceed.
func (s *Store) AppendWalkthroughEvent(ctx context.Context, ev memory.WalkthroughEvent) error {
	ctxJSON, err := encodeWalkthroughContext(ev.Context)
	if err != nil {
		return err
	}
	var goal, snapshot, repository, outcomeKey, outcomeStatus, reason, note any
	if ev.Goal != "" {
		goal, snapshot = ev.Goal, ev.Snapshot
		if ev.Repository != "" {
			repository = ev.Repository
		}
	}
	if ev.Outcome != nil {
		outcomeKey, outcomeStatus = ev.Outcome.Key, string(ev.Outcome.Status)
		if ev.Outcome.Reason != "" {
			reason = ev.Outcome.Reason
		}
		if ev.Outcome.Note != "" {
			note = ev.Outcome.Note
		}
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO walkthrough_events
			(token, goal, snapshot, repository, outcome_key, outcome_status, reason, note, context, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.Token, goal, snapshot, repository, outcomeKey, outcomeStatus, reason, note, ctxJSON, formatTime(ev.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert walkthrough event: %w", err)
		}
		return nil
	})
}

// scanWalkthroughEvent scans one row of walkthroughColumns, with token
// read from the row itself (needed by AllWalkthroughEvents, which spans
// every token) rather than a caller-supplied parameter.
func scanWalkthroughEvent(rows *sql.Rows) (memory.WalkthroughEvent, error) {
	var token string
	var goal, snapshot, repository, outcomeKey, outcomeStatus, reason, note sql.NullString
	var ctxJSON, created string
	if err := rows.Scan(&token, &goal, &snapshot, &repository, &outcomeKey, &outcomeStatus, &reason, &note, &ctxJSON, &created); err != nil {
		return memory.WalkthroughEvent{}, fmt.Errorf("scan walkthrough event: %w", err)
	}
	ev := memory.WalkthroughEvent{Token: token, Goal: goal.String, Snapshot: snapshot.String, Repository: repository.String}
	if outcomeKey.Valid {
		ev.Outcome = &memory.WalkthroughOutcome{Key: outcomeKey.String, Status: memory.WalkthroughOutcomeStatus(outcomeStatus.String), Reason: reason.String, Note: note.String}
	}
	ctxMap, err := decodeWalkthroughContext(ctxJSON)
	if err != nil {
		return memory.WalkthroughEvent{}, err
	}
	ev.Context = ctxMap
	if ev.CreatedAt, err = parseTime(created); err != nil {
		return memory.WalkthroughEvent{}, err
	}
	return ev, nil
}

// WalkthroughEvents implements memory.WalkthroughStore.
func (s *Store) WalkthroughEvents(ctx context.Context, token string) ([]memory.WalkthroughEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+walkthroughColumns+` FROM walkthrough_events WHERE token = ? ORDER BY id`, token)
	if err != nil {
		return nil, fmt.Errorf("query walkthrough events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var events []memory.WalkthroughEvent
	for rows.Next() {
		ev, err := scanWalkthroughEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read walkthrough events: %w", err)
	}
	if len(events) == 0 {
		return nil, memory.ErrNotFound
	}
	return events, nil
}

// AllWalkthroughEvents implements memory.WalkthroughStore (ADR-0056): every
// event of every token, ordered so one token's events are contiguous,
// oldest first within it — the raw material internal/memory folds and
// filters for an operator's list_walkthroughs. The expected scale (a
// handful of concurrent, ephemeral walkthroughs) does not justify pushing
// the fold into SQL.
func (s *Store) AllWalkthroughEvents(ctx context.Context) ([]memory.WalkthroughEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+walkthroughColumns+` FROM walkthrough_events ORDER BY token, id`)
	if err != nil {
		return nil, fmt.Errorf("query walkthrough events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var events []memory.WalkthroughEvent
	for rows.Next() {
		ev, err := scanWalkthroughEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read walkthrough events: %w", err)
	}
	return events, nil
}

// DeleteWalkthrough implements memory.WalkthroughStore: a real DELETE, no
// trace kept (ADR-0053) — the one table in this schema that is, unlike
// every other, not immutable and undeletable.
func (s *Store) DeleteWalkthrough(ctx context.Context, token string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM walkthrough_events WHERE token = ?`, token); err != nil {
			return fmt.Errorf("delete walkthrough: %w", err)
		}
		return nil
	})
}

// encodeWalkthroughContext stores context as a JSON object, "{}" for none
// (the column is NOT NULL).
func encodeWalkthroughContext(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("encode context: %w", err)
	}
	return string(b), nil
}

func decodeWalkthroughContext(raw string) (map[string]string, error) {
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("stored context: %w", err)
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}
