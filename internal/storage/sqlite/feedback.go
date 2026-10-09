package sqlite

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"fmt"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// CreateFeedback implements memory.Store.
func (s *Store) CreateFeedback(ctx context.Context, f memory.Feedback) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO feedback (id, kind, summary, details, reporter, context, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			f.ID, string(f.Kind), f.Summary, f.Details, f.Reporter, string(f.Context), formatTime(f.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert feedback: %w", err)
		}
		return nil
	})
}

const feedbackColumns = `id, kind, summary, details, reporter, context, created_at`

// Feedback implements memory.Store.
func (s *Store) Feedback(ctx context.Context, id string) (memory.Feedback, error) {
	f, err := scanFeedback(s.db.QueryRowContext(ctx, `SELECT `+feedbackColumns+` FROM feedback WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Feedback{}, memory.ErrNotFound
	}
	return f, err
}

// ListFeedback implements memory.Store.
func (s *Store) ListFeedback(ctx context.Context, kind memory.FeedbackKind) ([]memory.Feedback, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+feedbackColumns+` FROM feedback
		WHERE ? = '' OR kind = ? ORDER BY created_at, id`, string(kind), string(kind))
	if err != nil {
		return nil, fmt.Errorf("query feedback: %w", err)
	}
	defer func() { _ = rows.Close() }()

	reports := []memory.Feedback{}
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, err
		}
		reports = append(reports, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read feedback: %w", err)
	}
	return reports, nil
}

func scanFeedback(row interface{ Scan(...any) error }) (memory.Feedback, error) {
	var f memory.Feedback
	var kind, created string
	var reportContext []byte
	if err := row.Scan(&f.ID, &kind, &f.Summary, &f.Details, &f.Reporter, &reportContext, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return memory.Feedback{}, err
		}
		return memory.Feedback{}, fmt.Errorf("scan feedback: %w", err)
	}
	f.Kind, f.Context = memory.FeedbackKind(kind), jsontext.Value(reportContext)
	var err error
	if f.CreatedAt, err = parseTime(created); err != nil {
		return memory.Feedback{}, err
	}
	return f, nil
}
