package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// SupportedSchema is the newest schema version this build opens: the number
// of embedded migrations.
func SupportedSchema() (int, error) {
	scripts, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	return len(scripts), nil
}

// fileURI names an absolute path as an SQLite URI, so that spaces, '?' and
// '#' in it are escaped and the open flags in query apply.
func fileURI(path, query string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("database path %q is not absolute", path)
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: query}).String(), nil
}

// Snapshot writes a transactionally consistent copy of the catalog at src to
// dst with VACUUM INTO (ADR-0030). It reads within one transaction, so
// transactions committed to src's -wal file are included and writers on
// other connections keep writing. src must exist; it is opened read-write,
// so that, if this is its last connection, the -wal and -shm files opening
// it may create are removed again, but nothing is written to it and no
// migration runs. dst must be an existing empty file; it becomes a single
// rollback-journal database with src's user_version.
func Snapshot(ctx context.Context, src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("catalog %s: %w", src, err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("catalog %s is not a regular file", src)
	}
	uri, err := fileURI(src, "mode=rw&_busy_timeout=5000")
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return fmt.Errorf("open catalog %s: %w", src, err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("snapshot of %s: %w", src, err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close catalog %s: %w", src, err)
	}
	return nil
}

// Examination describes a database file that no connection writes.
type Examination struct {
	SchemaVersion        int
	Integrity            []string // "ok", or integrity_check's findings
	ForeignKeyViolations int
	Tables               map[string]int64 // rows per table
}

// IntegrityOK reports a clean integrity check without foreign key violations.
func (e Examination) IntegrityOK() bool {
	return len(e.Integrity) == 1 && e.Integrity[0] == "ok" && e.ForeignKeyViolations == 0
}

// Catalog reports whether the file holds Polaroid's tables.
func (e Examination) Catalog() bool {
	_, ok := e.Tables["procedures"]
	return ok
}

// maxIntegrityFindings bounds what Examine keeps of a damaged file's report.
const maxIntegrityFindings = 10

// Examine reads path, a database no connection writes (a backup, or a staged
// copy), opened read-only and immutable: it takes no lock, creates no -wal or
// -shm file, and changes nothing. It returns an error when the file is not a
// readable SQLite database.
func Examine(ctx context.Context, path string) (Examination, error) {
	var e Examination
	if _, err := os.Stat(path); err != nil {
		return e, err
	}
	uri, err := fileURI(path, "mode=ro&immutable=1")
	if err != nil {
		return e, err
	}
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return e, err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&e.SchemaVersion); err != nil {
		return e, fmt.Errorf("read schema version: %w", err)
	}
	if e.Integrity, err = integrityFindings(ctx, db); err != nil {
		return e, err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&e.ForeignKeyViolations); err != nil {
		return e, fmt.Errorf("foreign key check: %w", err)
	}
	names, err := tableNames(ctx, db)
	if err != nil {
		return e, err
	}
	e.Tables = make(map[string]int64, len(names))
	for _, name := range names {
		var n int64
		// The name comes from sqlite_schema; quoting keeps it an identifier.
		q := `SELECT count(*) FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"` //nolint:gosec // identifier from sqlite_schema, quoted
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			return e, fmt.Errorf("count %s: %w", name, err)
		}
		e.Tables[name] = n
	}
	return e, nil
}

func integrityFindings(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`PRAGMA integrity_check(%d)`, maxIntegrityFindings))
	if err != nil {
		return nil, fmt.Errorf("integrity check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var findings []string
	for rows.Next() {
		var finding string
		if err := rows.Scan(&finding); err != nil {
			return nil, fmt.Errorf("integrity check: %w", err)
		}
		findings = append(findings, finding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("integrity check: %w", err)
	}
	return findings, nil
}

func tableNames(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("list tables: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// Sidecars returns the -wal, -shm and -journal files that exist beside the
// database at path. Any of them means a connection may have it open, or one
// ended without closing it.
func Sidecars(path string) []string {
	var found []string
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			found = append(found, path+suffix)
		}
	}
	return found
}

// CatalogSchemaVersion reads the schema version of the catalog at path
// without migrating it or changing any file. Without sidecars, the main file
// is complete and its header is read directly; with them, a running
// polaroidd may hold a newer version in -wal, so it is read through a
// read-only connection, which the existing sidecars allow.
func CatalogSchemaVersion(ctx context.Context, path string) (int, error) {
	if len(Sidecars(path)) == 0 {
		return headerSchemaVersion(path)
	}
	uri, err := fileURI(path, "mode=ro&_busy_timeout=5000")
	if err != nil {
		return 0, err
	}
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()
	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema version of %s: %w", path, err)
	}
	return v, nil
}

// headerSchemaVersion reads user_version from the database header: bytes
// 60 to 63, big-endian. An empty file is schema 0.
func headerSchemaVersion(path string) (int, error) {
	f, err := os.Open(path) //nolint:gosec // the catalog the user named
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	var h [100]byte
	n, err := io.ReadFull(f, h[:])
	switch {
	case n == 0 && errors.Is(err, io.EOF):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("read the header of %s: %w", path, err)
	case string(h[:16]) != "SQLite format 3\x00":
		return 0, fmt.Errorf("%s is not an SQLite database", path)
	}
	return int(binary.BigEndian.Uint32(h[60:64])), nil
}

// Upgrade applies any pending migrations to the database at path, which no
// other connection may have open, and closes it again. It returns the schema
// versions before and after.
func Upgrade(ctx context.Context, path string) (from, to int, err error) {
	if from, err = headerSchemaVersion(path); err != nil {
		return 0, 0, err
	}
	s, err := Open(ctx, path)
	if err != nil {
		return from, 0, err
	}
	if err := s.Close(); err != nil {
		return from, 0, fmt.Errorf("close %s: %w", path, err)
	}
	if to, err = headerSchemaVersion(path); err != nil {
		return from, 0, err
	}
	return from, to, nil
}
