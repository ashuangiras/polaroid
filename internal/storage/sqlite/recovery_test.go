package sqlite_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

// ADR-0030: the SQLite side of backup and restore.

func TestSnapshotCopiesWithoutCreatingMigratingOrLeavingSidecars(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "with spaces")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	empty := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	missing := filepath.Join(dir, "missing.db")
	if err := sqlite.Snapshot(ctx, missing, empty("a.db")); err == nil {
		t.Fatal("a snapshot of a missing catalog succeeded")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("the snapshot created the missing catalog")
	}

	src := filepath.Join(dir, "src.db")
	schema7(t, src)
	out := empty("out.db")
	if err := sqlite.Snapshot(ctx, src, out); err != nil {
		t.Fatal(err)
	}
	if got := sqlite.Sidecars(src); len(got) != 0 {
		t.Errorf("the snapshot left %v beside a stopped catalog", got)
	}
	if v, err := sqlite.CatalogSchemaVersion(ctx, src); err != nil || v != 7 {
		t.Errorf("source schema after the snapshot: %d %v", v, err)
	}
	ex, err := sqlite.Examine(ctx, out)
	if err != nil || ex.SchemaVersion != 7 || !ex.IntegrityOK() || !ex.Catalog() || ex.Tables["procedure_versions"] != 1 {
		t.Fatalf("examination %+v, %v", ex, err)
	}
	if len(sqlite.Sidecars(out)) != 0 {
		t.Error("examining created sidecars")
	}
	if err := sqlite.Snapshot(ctx, src, out); err == nil {
		t.Error("a snapshot overwrote a non-empty file")
	}

	from, to, err := sqlite.Upgrade(ctx, out)
	supported, _ := sqlite.SupportedSchema()
	if err != nil || from != 7 || to != supported || len(sqlite.Sidecars(out)) != 0 {
		t.Fatalf("upgrade %d -> %d, %v, sidecars %v", from, to, err, sqlite.Sidecars(out))
	}
	if ex, err := sqlite.Examine(ctx, out); err != nil || ex.SchemaVersion != supported || !ex.IntegrityOK() {
		t.Fatalf("after the upgrade: %+v, %v", ex, err)
	}
}

func TestCatalogSchemaVersionOfStoppedAndRunningCatalogs(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	s := openStore(t, path)
	supported, _ := sqlite.SupportedSchema()
	if v, err := sqlite.CatalogSchemaVersion(ctx, path); err != nil || v != supported {
		t.Errorf("running catalog: %d %v", v, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if v, err := sqlite.CatalogSchemaVersion(ctx, path); err != nil || v != supported || len(sqlite.Sidecars(path)) != 0 {
		t.Errorf("stopped catalog: %d %v, sidecars %v", v, err, sqlite.Sidecars(path))
	}
	text := filepath.Join(t.TempDir(), "notes.db")
	if err := os.WriteFile(text, []byte(strings.Repeat("not a database ", 10)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.CatalogSchemaVersion(ctx, text); err == nil {
		t.Error("a text file has a schema version")
	}
	if _, err := sqlite.Examine(ctx, text); err == nil {
		t.Error("a text file was examined as a database")
	}
}

// schema7 writes a catalog at schema version 7, as the build before
// migration 8 left it, with one procedure; the snapshot must not migrate it.
func schema7(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	files, err := filepath.Glob("migrations/000[1-7]_*.sql")
	if err != nil || len(files) != 7 {
		t.Fatalf("migrations 1 to 7: %v %v", files, err)
	}
	for _, f := range files {
		script, err := os.ReadFile(f) //nolint:gosec // repository files
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(script)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	const at = "2026-10-10T12:00:00.000000000Z"
	for _, stmt := range []string{
		`PRAGMA user_version = 7`,
		`INSERT INTO procedures (id, canonical_key, created_at) VALUES ('p', 'old.schema', '` + at + `')`,
		`INSERT INTO procedure_versions (procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at) VALUES ('p', 1, 'p', 'm', '{}', '{}', 'r', '` + at + `')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}
