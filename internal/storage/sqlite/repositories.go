package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	sqlitelib "modernc.org/sqlite/lib"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// CreateRepository implements memory.Store.
func (s *Store) CreateRepository(ctx context.Context, r *memory.Repository) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		at, err := appendTime(ctx, tx, latestRepositoryTime, r.CreatedAt)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO repositories (id, name, created_at) VALUES (?, ?, ?)`,
			r.ID, r.Name, formatTime(at)); err != nil {
			return fmt.Errorf("insert repository: %w", err)
		}
		if err := insertIdentifier(ctx, tx, r.ID, r.Identifier, nil, at); err != nil {
			return err
		}
		r.CreatedAt = at
		return nil
	})
}

// AddRepositoryAlias implements memory.Store.
func (s *Store) AddRepositoryAlias(ctx context.Context, repositoryID string, a memory.RepositoryAlias) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM repositories WHERE id = ?)`, repositoryID).Scan(&exists); err != nil {
			return fmt.Errorf("read repository: %w", err)
		}
		if !exists {
			return memory.ErrNotFound
		}
		// Binding names are unique within a repository across its identifiers
		// (ADR-0019), so an alias may not bring a second binding of a name.
		var name string
		err := tx.QueryRowContext(ctx, `
			SELECT a.name FROM bindings AS a
			JOIN bindings AS b ON b.name = a.name
			WHERE a.repository = ?
			  AND b.repository IN (SELECT identifier FROM repository_identifiers WHERE repository_id = ?)
			LIMIT 1`, a.Identifier, repositoryID).Scan(&name)
		if err == nil {
			return &memory.BindingNameConflictError{Name: name}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read binding names: %w", err)
		}
		return insertIdentifier(ctx, tx, repositoryID, a.Identifier, &a.Reason, a.CreatedAt)
	})
}

func insertIdentifier(ctx context.Context, tx *sql.Tx, repositoryID, identifier string, reason *string, at time.Time) error {
	canonical := 0
	if reason == nil {
		canonical = 1
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO repository_identifiers (identifier, repository_id, canonical, reason, created_at)
		VALUES (?, ?, ?, ?, ?)`, identifier, repositoryID, canonical, reason, formatTime(at))
	// identifier is the primary key, and the only key a client chooses.
	if c := resultCode(err); c == sqlitelib.SQLITE_CONSTRAINT_PRIMARYKEY || c == sqlitelib.SQLITE_CONSTRAINT_UNIQUE {
		return memory.ErrIdentifierExists
	}
	if err != nil {
		return fmt.Errorf("insert repository identifier: %w", err)
	}
	return nil
}

const repositorySelect = `
	SELECT r.id, r.name, r.created_at, i.identifier, i.canonical, i.reason, i.created_at
	FROM repositories AS r
	JOIN repository_identifiers AS i ON i.repository_id = r.id
`

// Repository implements memory.Store.
func (s *Store) Repository(ctx context.Context, id string) (memory.Repository, error) {
	return s.repository(ctx, repositorySelect+`WHERE r.id = ? ORDER BY i.canonical DESC, i.created_at, i.identifier`, id)
}

// RepositoryByIdentifier implements memory.Store.
func (s *Store) RepositoryByIdentifier(ctx context.Context, identifier string) (memory.Repository, error) {
	return s.repository(ctx, repositorySelect+`
		WHERE r.id = (SELECT repository_id FROM repository_identifiers WHERE identifier = ?)
		ORDER BY i.canonical DESC, i.created_at, i.identifier`, identifier)
}

func (s *Store) repository(ctx context.Context, query string, args ...any) (memory.Repository, error) {
	repos, err := scanRepositories(s.db.QueryContext(ctx, query, args...))
	if err != nil {
		return memory.Repository{}, err
	}
	if len(repos) == 0 {
		return memory.Repository{}, memory.ErrNotFound
	}
	return repos[0], nil
}

// ListRepositories implements memory.Store.
func (s *Store) ListRepositories(ctx context.Context, after *memory.Position, limit int) ([]memory.Repository, error) {
	at, id := "", ""
	if after != nil {
		at, id = formatTime(after.At), after.ID
	}
	return scanRepositories(s.db.QueryContext(ctx, repositorySelect+`
		WHERE r.id IN (SELECT id FROM repositories
		               WHERE ? = '' OR created_at > ? OR (created_at = ? AND id > ?)
		               ORDER BY created_at, id LIMIT ?)
		ORDER BY r.created_at, r.id, i.canonical DESC, i.created_at, i.identifier`, at, at, at, id, fetchLimit(limit)))
}

// scanRepositories reads repositorySelect rows, one per identifier, grouped
// by repository with the canonical identifier first.
func scanRepositories(rows *sql.Rows, err error) ([]memory.Repository, error) {
	if err != nil {
		return nil, fmt.Errorf("query repositories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	repos := []memory.Repository{}
	for rows.Next() {
		var id, name, created, identifier, identifierCreated string
		var canonical bool
		var reason sql.NullString
		if err := rows.Scan(&id, &name, &created, &identifier, &canonical, &reason, &identifierCreated); err != nil {
			return nil, fmt.Errorf("scan repository: %w", err)
		}
		if n := len(repos); n == 0 || repos[n-1].ID != id {
			at, err := parseTime(created)
			if err != nil {
				return nil, err
			}
			repos = append(repos, memory.Repository{ID: id, Name: name, CreatedAt: at})
		}
		r := &repos[len(repos)-1]
		if canonical {
			r.Identifier = identifier
			continue
		}
		at, err := parseTime(identifierCreated)
		if err != nil {
			return nil, err
		}
		r.Aliases = append(r.Aliases, memory.RepositoryAlias{Identifier: identifier, Reason: reason.String, CreatedAt: at})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read repositories: %w", err)
	}
	return repos, nil
}

// Queries for the latest created_at of the time-ordered lists (ADR-0021).
const (
	latestRepositoryTime = `SELECT MAX(created_at) FROM repositories`
	latestExecutionTime  = `SELECT MAX(created_at) FROM executions`
	latestFeedbackTime   = `SELECT MAX(created_at) FROM feedback`
)

// appendTime returns t, or one nanosecond after the latest stored time if t
// is not later. Writes hold the write lock, so every record of a
// time-ordered list sorts after every record committed before it, and a
// client paging with a cursor never misses a later commit.
func appendTime(ctx context.Context, tx *sql.Tx, latestQuery string, t time.Time) (time.Time, error) {
	var latest sql.NullString
	if err := tx.QueryRowContext(ctx, latestQuery).Scan(&latest); err != nil {
		return time.Time{}, fmt.Errorf("read latest time: %w", err)
	}
	if !latest.Valid {
		return t, nil
	}
	last, err := parseTime(latest.String)
	if err != nil {
		return time.Time{}, err
	}
	if t.After(last) {
		return t, nil
	}
	return last.Add(time.Nanosecond), nil
}
