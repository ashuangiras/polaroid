package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

// migrationFiles holds the schema history. File N must be named NNNN_*.sql;
// PRAGMA user_version records how many have been applied.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrate applies pending migrations in one write transaction, so concurrent
// openers of the same file apply each migration exactly once.
func migrate(ctx context.Context, db *sql.DB) error {
	scripts, err := loadMigrations()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var applied int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&applied); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if applied > len(scripts) {
		return fmt.Errorf("database schema version %d is newer than this build supports (%d)", applied, len(scripts))
	}
	if applied == len(scripts) {
		return nil
	}
	for i := applied; i < len(scripts); i++ {
		if _, err := tx.ExecContext(ctx, scripts[i]); err != nil {
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
	}
	// PRAGMA statements cannot take bound parameters; the value is an int.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, len(scripts))); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func loadMigrations() ([]string, error) {
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	scripts := make([]string, len(names))
	for i, name := range names {
		if want := fmt.Sprintf("migrations/%04d_", i+1); !strings.HasPrefix(name, want) {
			return nil, fmt.Errorf("migration %s is out of sequence: want prefix %s", name, want)
		}
		b, err := migrationFiles.ReadFile(name)
		if err != nil {
			return nil, err
		}
		scripts[i] = string(b)
	}
	return scripts, nil
}
