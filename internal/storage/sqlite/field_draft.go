package sqlite

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"fmt"

	"github.com/ashuangiras/polaroid/internal/memory"
)

const fieldDraftColumns = "token, key, snapshot, record_type, is_new, proposer, rationale, answer_entity, answer_field, answer_operation, answer_value, answer_citations, created_at"

func (s *Store) AppendFieldDraftEvent(ctx context.Context, ev memory.FieldDraftEvent, expectedEvents int) error {
	var key, snapshot, recordType, proposer, rationale any
	var isNew any
	if ev.Key != "" {
		key, snapshot, recordType, proposer, rationale = ev.Key, ev.Snapshot, ev.RecordType, ev.Proposer, ev.Rationale
		isNew = boolToInt(ev.New)
	}
	var answerEntity, answerField, answerOperation, answerValue, answerCitations any
	if ev.Answer != nil {
		answerEntity, answerField, answerOperation = ev.Answer.Entity, ev.Answer.Field, ev.Answer.Operation
		if len(ev.Answer.Value) > 0 {
			answerValue = string(ev.Answer.Value)
		}
		citations := ev.Answer.Citations
		if citations == nil {
			citations = []memory.FieldDraftCitation{}
		}
		cb, err := json.Marshal(citations)
		if err != nil {
			return fmt.Errorf("encode field draft citations: %w", err)
		}
		answerCitations = string(cb)
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		var eventCount int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM field_draft_events WHERE token = ?`, ev.Token).Scan(&eventCount); err != nil {
			return fmt.Errorf("count field draft events: %w", err)
		}
		if eventCount != expectedEvents {
			if eventCount == 0 {
				return memory.ErrNotFound
			}
			return memory.ErrFieldDraftConflict
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO field_draft_events
			(token, key, snapshot, record_type, is_new, proposer, rationale, answer_entity, answer_field, answer_operation, answer_value, answer_citations, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.Token, key, snapshot, recordType, isNew, proposer, rationale,
			answerEntity, answerField, answerOperation, answerValue, answerCitations, formatTime(ev.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert field draft event: %w", err)
		}
		return nil
	})
}

func scanFieldDraftEvent(rows *sql.Rows) (memory.FieldDraftEvent, error) {
	var token string
	var key, snapshot, recordType, proposer, rationale sql.NullString
	var isNew sql.NullInt64
	var answerEntity, answerField, answerOperation, answerValue, answerCitations sql.NullString
	var created string
	if err := rows.Scan(&token, &key, &snapshot, &recordType, &isNew, &proposer, &rationale,
		&answerEntity, &answerField, &answerOperation, &answerValue, &answerCitations, &created); err != nil {
		return memory.FieldDraftEvent{}, fmt.Errorf("scan field draft event: %w", err)
	}
	ev := memory.FieldDraftEvent{
		Token: token, Key: key.String, Snapshot: snapshot.String, RecordType: recordType.String,
		New: isNew.Valid && isNew.Int64 != 0, Proposer: proposer.String, Rationale: rationale.String,
	}
	if answerEntity.Valid {
		var citations []memory.FieldDraftCitation
		if err := json.Unmarshal([]byte(answerCitations.String), &citations); err != nil {
			return memory.FieldDraftEvent{}, fmt.Errorf("stored field draft citations: %w", err)
		}
		ev.Answer = &memory.FieldDraftAnswer{
			Entity: answerEntity.String, Field: answerField.String, Operation: answerOperation.String,
			Value: []byte(answerValue.String), Citations: citations,
		}
	}
	var err error
	if ev.CreatedAt, err = parseTime(created); err != nil {
		return memory.FieldDraftEvent{}, err
	}
	return ev, nil
}

func (s *Store) FieldDraftEvents(ctx context.Context, token string) ([]memory.FieldDraftEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+fieldDraftColumns+` FROM field_draft_events WHERE token = ? ORDER BY id`, token)
	if err != nil {
		return nil, fmt.Errorf("query field draft events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var events []memory.FieldDraftEvent
	for rows.Next() {
		ev, err := scanFieldDraftEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read field draft events: %w", err)
	}
	if len(events) == 0 {
		return nil, memory.ErrNotFound
	}
	return events, nil
}

func (s *Store) DeleteFieldDraft(ctx context.Context, token string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM field_draft_events WHERE token = ?`, token); err != nil {
			return fmt.Errorf("delete field draft: %w", err)
		}
		return nil
	})
}
