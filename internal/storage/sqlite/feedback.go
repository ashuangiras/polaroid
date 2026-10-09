package sqlite

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// CreateFeedback implements memory.Store.
func (s *Store) CreateFeedback(ctx context.Context, f *memory.Feedback) error {
	var subjectType, subjectID, subjectVersion, repository, executionID any
	if sub := f.Subject; sub != nil {
		subjectType = string(sub.Type)
		if id := sub.ID(); id != "" {
			subjectID = id
		}
		if n := sub.Number(); n != 0 {
			subjectVersion = n
		}
	}
	if f.Repository != "" {
		repository = f.Repository
	}
	if f.ExecutionID != "" {
		executionID = f.ExecutionID
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		at, err := appendTime(ctx, tx, latestFeedbackTime, f.CreatedAt)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO feedback (id, kind, summary, details, reporter, context,
			                      subject_type, subject_id, subject_version, repository, execution_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.ID, string(f.Kind), f.Summary, f.Details, f.Reporter, string(f.Context),
			subjectType, subjectID, subjectVersion, repository, executionID, formatTime(at))
		if err != nil {
			return fmt.Errorf("insert feedback: %w", err)
		}
		f.CreatedAt = at
		return nil
	})
}

const feedbackColumns = `id, kind, summary, details, reporter, context,
	subject_type, subject_id, subject_version, repository, execution_id, created_at`

// Feedback implements memory.Store.
func (s *Store) Feedback(ctx context.Context, id string) (memory.Feedback, error) {
	f, err := scanFeedback(s.db.QueryRowContext(ctx, `SELECT `+feedbackColumns+` FROM feedback WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Feedback{}, memory.ErrNotFound
	}
	return f, err
}

// ListFeedback implements memory.Store. Reports without a subject are about
// the service (ADR-0015, ADR-0021).
func (s *Store) ListFeedback(ctx context.Context, f memory.FeedbackFilter) ([]memory.Feedback, error) {
	at, id := "", ""
	if f.After != nil {
		at, id = formatTime(f.After.At), f.After.ID
	}
	var repositories any
	if f.Repositories != nil {
		list, err := json.Marshal(f.Repositories)
		if err != nil {
			return nil, fmt.Errorf("encode repositories: %w", err)
		}
		repositories = string(list)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+feedbackColumns+` FROM feedback
		WHERE (? = '' OR kind = ?)
		  AND (? = '' OR COALESCE(subject_type, 'service') = ?)
		  AND (? = '' OR subject_id = ?)
		  AND (? = 0 OR subject_version = ?)
		  AND (? IS NULL OR repository IN (SELECT value FROM json_each(?))
		       OR (subject_type = 'repository' AND subject_id = ?))
		  AND (? = '' OR created_at > ? OR (created_at = ? AND id > ?))
		ORDER BY created_at, id
		LIMIT ?`,
		string(f.Kind), string(f.Kind), string(f.SubjectType), string(f.SubjectType), f.SubjectID, f.SubjectID,
		f.SubjectVersion, f.SubjectVersion, repositories, repositories, f.RepositoryID,
		at, at, at, id, fetchLimit(f.Page.Limit))
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
	var subjectType, subjectID, repository, executionID sql.NullString
	var subjectVersion sql.NullInt64
	if err := row.Scan(&f.ID, &kind, &f.Summary, &f.Details, &f.Reporter, &reportContext,
		&subjectType, &subjectID, &subjectVersion, &repository, &executionID, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return memory.Feedback{}, err
		}
		return memory.Feedback{}, fmt.Errorf("scan feedback: %w", err)
	}
	f.Kind, f.Context = memory.FeedbackKind(kind), jsontext.Value(reportContext)
	if subjectType.Valid {
		sub := memory.NewSubject(memory.SubjectType(subjectType.String), subjectID.String, int(subjectVersion.Int64))
		f.Subject = &sub
	}
	f.Repository, f.ExecutionID = repository.String, executionID.String
	var err error
	if f.CreatedAt, err = parseTime(created); err != nil {
		return memory.Feedback{}, err
	}
	return f, nil
}
