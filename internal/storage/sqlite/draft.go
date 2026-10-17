package sqlite

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"fmt"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// draftColumns is shared by DraftEvents and any future all-tokens read.
const draftColumns = "token, key, snapshot, proposer, rationale, answer_field, answer_values, answer_citations, confirmed_absent, reason, created_at"

// AppendDraftEvent implements memory.DraftStore. Unlike every other write
// in this package, it needs no existence or conflict check: Drafts.Advance
// already read the token's events first and decides whether to proceed.
func (s *Store) AppendDraftEvent(ctx context.Context, ev memory.DraftEvent) error {
	var key, snapshot, proposer, rationale, answerField, answerValues, answerCitations, reason any
	var confirmedAbsent any
	if ev.Key != "" {
		key, snapshot, proposer, rationale = ev.Key, ev.Snapshot, ev.Proposer, ev.Rationale
	}
	if ev.Answer != nil {
		answerField = string(ev.Answer.Field)
		vb, err := json.Marshal(ev.Answer.Values)
		if err != nil {
			return fmt.Errorf("encode answer values: %w", err)
		}
		answerValues = string(vb)
		cb, err := json.Marshal(ev.Answer.Citations)
		if err != nil {
			return fmt.Errorf("encode answer citations: %w", err)
		}
		answerCitations = string(cb)
		confirmedAbsent = boolToInt(ev.Answer.ConfirmedAbsent)
		if ev.Answer.Reason != "" {
			reason = ev.Answer.Reason
		}
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO draft_events
			(token, key, snapshot, proposer, rationale, answer_field, answer_values, answer_citations, confirmed_absent, reason, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.Token, key, snapshot, proposer, rationale, answerField, answerValues, answerCitations, confirmedAbsent, reason, formatTime(ev.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert draft event: %w", err)
		}
		return nil
	})
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// scanDraftEvent scans one row of draftColumns.
func scanDraftEvent(rows *sql.Rows) (memory.DraftEvent, error) {
	var token string
	var key, snapshot, proposer, rationale, answerField, answerValues, answerCitations, reason sql.NullString
	var confirmedAbsent sql.NullInt64
	var created string
	if err := rows.Scan(&token, &key, &snapshot, &proposer, &rationale, &answerField, &answerValues, &answerCitations, &confirmedAbsent, &reason, &created); err != nil {
		return memory.DraftEvent{}, fmt.Errorf("scan draft event: %w", err)
	}
	ev := memory.DraftEvent{Token: token, Key: key.String, Snapshot: snapshot.String, Proposer: proposer.String, Rationale: rationale.String}
	if answerField.Valid {
		var values []string
		if err := json.Unmarshal([]byte(answerValues.String), &values); err != nil {
			return memory.DraftEvent{}, fmt.Errorf("stored answer values: %w", err)
		}
		var citations []memory.DraftCitation
		if err := json.Unmarshal([]byte(answerCitations.String), &citations); err != nil {
			return memory.DraftEvent{}, fmt.Errorf("stored answer citations: %w", err)
		}
		ev.Answer = &memory.DraftFieldAnswer{Field: memory.DraftField(answerField.String), Values: values, Citations: citations,
			ConfirmedAbsent: confirmedAbsent.Valid && confirmedAbsent.Int64 != 0, Reason: reason.String}
	}
	var err error
	if ev.CreatedAt, err = parseTime(created); err != nil {
		return memory.DraftEvent{}, err
	}
	return ev, nil
}

// DraftEvents implements memory.DraftStore.
func (s *Store) DraftEvents(ctx context.Context, token string) ([]memory.DraftEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+draftColumns+` FROM draft_events WHERE token = ? ORDER BY id`, token)
	if err != nil {
		return nil, fmt.Errorf("query draft events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var events []memory.DraftEvent
	for rows.Next() {
		ev, err := scanDraftEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read draft events: %w", err)
	}
	if len(events) == 0 {
		return nil, memory.ErrNotFound
	}
	return events, nil
}

// DeleteDraft implements memory.DraftStore: a real DELETE, no trace kept
// (ADR-0060) -- the same, narrow exception walkthrough_events already
// has, applied to a second table.
func (s *Store) DeleteDraft(ctx context.Context, token string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM draft_events WHERE token = ?`, token); err != nil {
			return fmt.Errorf("delete draft: %w", err)
		}
		return nil
	})
}
