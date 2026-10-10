package recovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/lifecycle"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

var ctx = context.Background()

func TestBackupOfARunningCatalogIncludesWALAndToleratesConcurrentWrites(t *testing.T) {
	path := filepath.Join(spaced(t, "live"), "polaroid.db")
	a := serve(t, path)
	f := populate(a)
	want := f.reads(a)
	// Everything so far is committed but still in -wal: the main file has
	// not been checkpointed.
	if st, err := os.Stat(path + "-wal"); err != nil || st.Size() == 0 {
		t.Fatalf("the fixture is not in -wal: %v", err)
	}
	if v, err := sqlite.CatalogSchemaVersion(ctx, path); err != nil || v < 1 {
		t.Fatalf("schema version of the live catalog: %d, %v", v, err)
	}

	var written atomic.Int64
	stop, going := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			a.post("/v1/procedures", procedureBody(fmt.Sprintf("recovery.concurrent.%d", i), ""))
			if written.Add(1) == 5 {
				close(going)
			}
		}
	}()
	<-going
	before := written.Load()
	dir := spaced(t, "backups")
	b := mustBackup(t, path, dir)
	after := written.Load()
	close(stop)
	wg.Wait()
	if after == before {
		t.Logf("no write completed during the snapshot (%d before)", before)
	}

	r := Inspect(ctx, b.Path)
	if !r.Valid {
		t.Fatalf("the backup of a running catalog is invalid: %v", r.Problems)
	}
	captured := r.Records["procedures"]
	// The fixture's 2 procedures, plus every concurrent one committed before the
	// snapshot began, and none that it could not see whole.
	if captured < 2+before || captured > 2+written.Load() {
		t.Fatalf("the backup holds %d procedures; %d were committed before it began, %d by the end", captured, 2+before, 2+written.Load())
	}

	restored := filepath.Join(spaced(t, "restored"), "polaroid.db")
	if _, err := Restore(ctx, RestoreRequest{Backup: b.Path, Destination: restored, BackupDir: dir}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	ra := serve(t, restored)
	checkReads(t, f.reads(ra), want)
	list := string(ra.get("/v1/procedures"))
	if n := int64(strings.Count(list, `"canonical_key":`)); n != captured {
		t.Errorf("the restored catalog lists %d procedures; the backup recorded %d", n, captured)
	}
}

func TestRestoreReplacesTheCatalogWithTheSnapshot(t *testing.T) {
	path, f, want := stoppedCatalog(t)
	dir := filepath.Join(spaced(t, "home"), ".polaroid", "backups")
	b := mustBackup(t, path, dir)

	// The backup is private and complete.
	if mode(t, dir) != 0o700 || mode(t, b.Path) != 0o700 || mode(t, filepath.Join(b.Path, DatabaseFile)) != 0o600 || mode(t, filepath.Join(b.Path, MetadataFile)) != 0o600 {
		t.Errorf("backup modes: dir %v, backup %v, db %v, metadata %v", mode(t, dir), mode(t, b.Path), mode(t, filepath.Join(b.Path, DatabaseFile)), mode(t, filepath.Join(b.Path, MetadataFile)))
	}
	if names := entries(t, dir); len(names) != 1 || !strings.HasPrefix(names[0], "polaroid-") {
		t.Errorf("the backup directory holds %v, want one backup and no staging", names)
	}
	m := b.Metadata
	if m.Format != Format || m.FormatVersion != 1 || m.Reason != ReasonBackup || m.Source.Path != path || m.Source.Managed ||
		m.SchemaVersion < 8 || len(m.Database.SHA256) != 64 || m.Records["procedures"] != 2 || m.Records["execution_decisions"] != 1 || m.CreatedAt == "" {
		t.Errorf("metadata %+v", m)
	}

	// History written after the backup is lost by the restore, and kept in
	// the recovery backup.
	a := serve(t, path)
	a.post("/v1/procedures", procedureBody("recovery.after", ""))
	a.close()

	p := MakePlan(ctx, RestoreRequest{Backup: b.Path, Destination: path, BackupDir: dir})
	if !p.Ready || !p.RequiresReplace || !p.Destination.Exists || p.Destination.Managed || p.Migration != nil || p.Service != nil {
		t.Fatalf("plan %+v", p)
	}
	res, err := Restore(ctx, RestoreRequest{Backup: b.Path, Destination: path, BackupDir: dir, Replace: true})
	if err != nil || res.Outcome != Restored || res.RecoveryBackup == "" {
		t.Fatalf("restore: %+v, %v", res, err)
	}
	ra := serve(t, path)
	checkReads(t, f.reads(ra), want)
	if code, _ := ra.call("GET", "/v1/procedures/by-key/recovery.after", ""); code != 404 {
		t.Errorf("a procedure written after the backup survived the restore: %d", code)
	}
	ra.close()
	if mode(t, path) != 0o600 {
		t.Errorf("restored catalog mode %v, want 0600", mode(t, path))
	}
	if names := entries(t, filepath.Dir(path)); !slices.Equal(names, []string{"polaroid.db"}) {
		t.Errorf("beside the restored catalog: %v, want only polaroid.db", names)
	}

	rb := Inspect(ctx, res.RecoveryBackup)
	if !rb.Valid || rb.Metadata.Reason != ReasonPreRestore || rb.Records["procedures"] != 3 {
		t.Fatalf("recovery backup %+v", rb)
	}
	back := filepath.Join(spaced(t, "recovered"), "polaroid.db")
	if _, err := Restore(ctx, RestoreRequest{Backup: res.RecoveryBackup, Destination: back, BackupDir: dir}); err != nil {
		t.Fatal(err)
	}
	if code, _ := serve(t, back).call("GET", "/v1/procedures/by-key/recovery.after", ""); code != 200 {
		t.Errorf("the recovery backup lacks the record the restore replaced: %d", code)
	}
}

func TestRestoreNeedsConfirmationAndCreatesNewCatalogs(t *testing.T) {
	path, f, want := stoppedCatalog(t)
	dir := spaced(t, "backups")
	b := mustBackup(t, path, dir)
	before := digest(t, path)
	_, err := Restore(ctx, RestoreRequest{Backup: b.Path, Destination: path, BackupDir: dir})
	if !errors.Is(err, ErrConfirmationRequired) || !errors.Is(err, ErrRefused) || digest(t, path) != before {
		t.Fatalf("restore without -replace over an existing catalog: %v", err)
	}
	if len(entries(t, dir)) != 1 {
		t.Errorf("a refused restore took a recovery backup: %v", entries(t, dir))
	}

	// A new catalog, in directories that do not exist yet, needs no confirmation.
	fresh := filepath.Join(spaced(t, "new"), "a", "b", "polaroid.db")
	res, err := Restore(ctx, RestoreRequest{Backup: b.Path, Destination: fresh, BackupDir: dir})
	if err != nil || res.Outcome != Restored || res.RecoveryBackup != "" || res.Plan.RequiresReplace {
		t.Fatalf("restore into a new path: %+v, %v", res, err)
	}
	if mode(t, fresh) != 0o600 || mode(t, filepath.Dir(fresh)) != 0o700 {
		t.Errorf("modes: catalog %v, directory %v", mode(t, fresh), mode(t, filepath.Dir(fresh)))
	}
	checkReads(t, f.reads(serve(t, fresh)), want)
}

// invalidBackup copies a valid backup and damages it with damage.
func invalidBackup(t *testing.T, valid string, damage func(dir string)) string {
	t.Helper()
	dir := filepath.Join(spaced(t, "damaged"), "backup")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{DatabaseFile, MetadataFile} {
		writeFile(t, filepath.Join(dir, n), readFile(t, filepath.Join(valid, n)))
	}
	damage(dir)
	return dir
}

// rewriteDigest records the database file's current size and SHA-256.
func rewriteDigest(t *testing.T, dir string, m Metadata) {
	t.Helper()
	size, sum, err := fileDigest(filepath.Join(dir, DatabaseFile))
	if err != nil {
		t.Fatal(err)
	}
	b := readFile(t, filepath.Join(dir, MetadataFile))
	b = []byte(strings.Replace(string(b), m.Database.SHA256, sum, 1))
	b = []byte(strings.Replace(string(b), fmt.Sprintf(`"size":%d`, m.Database.Size), fmt.Sprintf(`"size":%d`, size), 1))
	writeFile(t, filepath.Join(dir, MetadataFile), b)
}

func TestInvalidBackupsAreRejectedBeforeAnythingChanges(t *testing.T) {
	path, _, _ := stoppedCatalog(t)
	dir := spaced(t, "backups")
	good := mustBackup(t, path, dir)
	supported, _ := sqlite.SupportedSchema()
	db := func(d string) string { return filepath.Join(d, DatabaseFile) }
	for _, tc := range []struct {
		name, want string
		damage     func(dir string)
	}{
		{"missing metadata", "backup.json is missing", func(d string) { _ = os.Remove(filepath.Join(d, MetadataFile)) }},
		{"missing database", "the database file is missing", func(d string) { _ = os.Remove(db(d)) }},
		{"truncated database", "bytes; the metadata records", func(d string) { writeFile(t, db(d), readFile(t, db(d))[:4096]) }},
		{"corrupt database", "SHA-256", func(d string) {
			b := readFile(t, db(d))
			for i := 4096; i < len(b) && i < 8192; i++ {
				b[i] ^= 0xff
			}
			writeFile(t, db(d), b)
		}},
		{"corrupt database, checksum rewritten", "integrity", func(d string) {
			b := readFile(t, db(d))
			for i := 4096; i < len(b) && i < 8192; i++ {
				b[i] ^= 0xff
			}
			writeFile(t, db(d), b)
			rewriteDigest(t, d, good.Metadata)
		}},
		{"checksum mismatch", "SHA-256", func(d string) {
			replaceInFile(t, filepath.Join(d, MetadataFile), good.Metadata.Database.SHA256, strings.Repeat("0", 64))
		}},
		{"unsupported metadata version", "format_version 2", func(d string) {
			replaceInFile(t, filepath.Join(d, MetadataFile), `"format_version":1`, `"format_version":2`)
		}},
		{"unknown metadata member", "does not match format version 1", func(d string) {
			replaceInFile(t, filepath.Join(d, MetadataFile), `"format":"polaroid-backup"`, `"format":"polaroid-backup","extra":true`)
		}},
		{"not a backup", "format", func(d string) { writeFile(t, filepath.Join(d, MetadataFile), []byte(`{"format":"other"}`)) }},
		{"newer schema", "newer than this polaroid supports", func(d string) {
			setSchemaVersion(t, db(d), supported+1)
			rewriteDigest(t, d, good.Metadata)
			replaceInFile(t, filepath.Join(d, MetadataFile), fmt.Sprintf(`"schema_version":%d`, good.Metadata.SchemaVersion), fmt.Sprintf(`"schema_version":%d`, supported+1))
		}},
		{"schema differs from metadata", "the metadata records", func(d string) {
			setSchemaVersion(t, db(d), good.Metadata.SchemaVersion-1)
			rewriteDigest(t, d, good.Metadata)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := invalidBackup(t, good.Path, tc.damage)
			before, beforeDB := digest(t, path), ""
			if _, err := os.Stat(db(bad)); err == nil {
				beforeDB = digest(t, db(bad))
			}
			r := Inspect(ctx, bad)
			if r.Valid || !strings.Contains(strings.Join(r.Problems, "; "), tc.want) {
				t.Fatalf("inspect: valid %v, problems %v; want %q", r.Valid, r.Problems, tc.want)
			}
			res, err := Restore(ctx, RestoreRequest{Backup: bad, Destination: path, BackupDir: dir, Replace: true})
			if !errors.Is(err, ErrRefused) || res.Outcome != Unchanged || res.Plan.Ready || digest(t, path) != before {
				t.Fatalf("restore of an invalid backup: %+v, %v", res.Outcome, err)
			}
			if beforeDB != "" && digest(t, db(bad)) != beforeDB {
				t.Error("inspection changed the backup's database file")
			}
			if names := entries(t, filepath.Dir(path)); !slices.Equal(names, []string{"polaroid.db"}) {
				t.Errorf("beside the catalog: %v", names)
			}
		})
	}
	if names := entries(t, dir); len(names) != 1 {
		t.Errorf("refused restores wrote to the backup directory: %v", names)
	}
}

// schema7Catalog writes a catalog at schema version 7 with one procedure,
// applying the first seven migration files.
func schema7Catalog(t *testing.T, path string) {
	t.Helper()
	files, err := filepath.Glob("../storage/sqlite/migrations/*.sql")
	if err != nil || len(files) < 8 {
		t.Fatalf("migrations: %v %v", files, err)
	}
	slices.Sort(files)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, f := range files[:7] {
		if _, err := db.ExecContext(ctx, string(readFile(t, f))); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	for _, s := range []string{
		`PRAGMA user_version = 7`,
		`INSERT INTO procedures (id, canonical_key, created_at) VALUES ('01a12000-0000-7000-8000-000000000001', 'old.schema', '2026-10-01T00:00:00.000000000Z')`,
		`INSERT INTO procedure_versions (procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at) VALUES ('01a12000-0000-7000-8000-000000000001', 1, 'p', 'm', '{}', '{}', 'r', '2026-10-01T00:00:00.000000000Z')`,
	} {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func TestOlderBackupsAreMigratedExplicitlyAndNewerCatalogsRefused(t *testing.T) {
	supported, _ := sqlite.SupportedSchema()
	old := filepath.Join(spaced(t, "old"), "polaroid.db")
	schema7Catalog(t, old)
	dir := spaced(t, "backups")
	b := mustBackup(t, old, dir)
	if v, _ := sqlite.CatalogSchemaVersion(ctx, old); v != 7 || b.Metadata.SchemaVersion != 7 {
		t.Fatalf("backing up migrated the source or the snapshot: source %d, backup %d", v, b.Metadata.SchemaVersion)
	}
	dbSum := digest(t, filepath.Join(b.Path, DatabaseFile))

	dest := filepath.Join(spaced(t, "dest"), "polaroid.db")
	p := MakePlan(ctx, RestoreRequest{Backup: b.Path, Destination: dest, BackupDir: dir})
	if !p.Ready || p.Migration == nil || p.Migration.From != 7 || p.Migration.To != supported || !strings.Contains(strings.Join(p.Actions, "\n"), "migrate the staged copy from schema 7") {
		t.Fatalf("plan for a schema-7 backup: %+v", p)
	}
	if digest(t, filepath.Join(b.Path, DatabaseFile)) != dbSum {
		t.Fatal("planning changed the backup")
	}
	if _, err := Restore(ctx, RestoreRequest{Backup: b.Path, Destination: dest, BackupDir: dir}); err != nil {
		t.Fatal(err)
	}
	if v, _ := sqlite.CatalogSchemaVersion(ctx, dest); v != supported {
		t.Errorf("restored schema %d, want %d", v, supported)
	}
	if digest(t, filepath.Join(b.Path, DatabaseFile)) != dbSum {
		t.Error("restoring changed the backup")
	}
	if !strings.Contains(string(serve(t, dest).get("/v1/procedures/by-key/old.schema")), `"canonical_key":"old.schema"`) {
		t.Error("the migrated restore lost the record")
	}

	// A catalog newer than this build is never replaced: that would downgrade it.
	newer := filepath.Join(spaced(t, "newer"), "polaroid.db")
	writeFile(t, newer, readFile(t, filepath.Join(b.Path, DatabaseFile)))
	setSchemaVersion(t, newer, supported+1)
	before := digest(t, newer)
	res, err := Restore(ctx, RestoreRequest{Backup: b.Path, Destination: newer, BackupDir: dir, Replace: true})
	if !errors.Is(err, ErrRefused) || !strings.Contains(strings.Join(res.Plan.Blockers, " "), "downgrade") || digest(t, newer) != before {
		t.Fatalf("restore over a newer catalog: %v %v", res.Plan.Blockers, err)
	}
	if _, err := Backup(ctx, BackupRequest{Source: newer, Dir: dir}); err == nil || !strings.Contains(err.Error(), "supports 1 to") {
		t.Errorf("backup of a newer catalog: %v", err)
	}
}

func TestRestoreFailuresLeaveTheCatalogUnchanged(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not restrict root")
	}
	path, _, _ := stoppedCatalog(t)
	dir := spaced(t, "backups")
	b := mustBackup(t, path, dir)
	a := serve(t, path)
	a.post("/v1/procedures", procedureBody("recovery.current", ""))
	a.close()
	before := digest(t, path)
	unchanged := func(t *testing.T, res Result, err error, want string) {
		t.Helper()
		if err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), want) || res.Outcome != Unchanged || digest(t, path) != before {
			t.Fatalf("got %q (outcome %s, unchanged %v), want a failure containing %q", err, res.Outcome, digest(t, path) == before, want)
		}
		if names := entries(t, filepath.Dir(path)); !slices.Equal(names, []string{"polaroid.db"}) {
			t.Errorf("beside the catalog: %v", names)
		}
	}
	req := RestoreRequest{Backup: b.Path, Destination: path, BackupDir: dir, Replace: true}

	t.Run("staging fails", func(t *testing.T) {
		catalogDir := filepath.Dir(path)
		if err := os.Chmod(catalogDir, 0o500); err != nil {
			t.Fatal(err)
		}
		res, err := Restore(ctx, req)
		_ = os.Chmod(catalogDir, 0o700)
		unchanged(t, res, err, "stage the restore")
		if len(entries(t, dir)) != 1 {
			t.Errorf("a recovery backup was taken before staging: %v", entries(t, dir))
		}
	})
	t.Run("recovery backup fails", func(t *testing.T) {
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		res, err := Restore(ctx, req)
		_ = os.Chmod(dir, 0o700)
		unchanged(t, res, err, "recovery backup of the current catalog")
		if res.RecoveryBackup != "" || len(entries(t, dir)) != 1 {
			t.Errorf("recovery backup %q, backups %v", res.RecoveryBackup, entries(t, dir))
		}
	})
	t.Run("replacement fails", func(t *testing.T) {
		renameFile = func(string, string) error { return errors.New("injected rename failure") }
		defer func() { renameFile = os.Rename }()
		res, err := Restore(ctx, req)
		unchanged(t, res, err, "injected rename failure")
		// The recovery backup is evidence and stays.
		if r := Inspect(ctx, res.RecoveryBackup); !r.Valid || r.Records["procedures"] != 3 {
			t.Errorf("recovery backup after a failed replacement: %+v", r)
		}
	})
	t.Run("the backup changes after it was inspected", func(t *testing.T) {
		backupDB := filepath.Join(b.Path, DatabaseFile)
		original := readFile(t, backupDB)
		afterPlan = func() {
			damaged := append([]byte(nil), original...)
			damaged[len(damaged)-1] ^= 0xff
			writeFile(t, backupDB, damaged)
		}
		defer func() { afterPlan = func() {}; writeFile(t, backupDB, original) }()
		res, err := Restore(ctx, req)
		unchanged(t, res, err, "differs from the backup's")
	})
	t.Run("an unmanaged catalog that may be open", func(t *testing.T) {
		writeFile(t, path+"-wal", nil)
		defer func() { _ = os.Remove(path + "-wal") }()
		res, err := Restore(ctx, req)
		if !errors.Is(err, ErrRefused) || !strings.Contains(strings.Join(res.Plan.Blockers, " "), "a process may have it open") || digest(t, path) != before {
			t.Fatalf("restore beside a -wal file: %v %v", res.Plan.Blockers, err)
		}
		if _, err := os.Stat(path + "-wal"); err != nil {
			t.Error("the restore removed the -wal file")
		}
	})
}

func TestRestoreCoordinatesWithTheManagedService(t *testing.T) {
	setup := func(t *testing.T) (string, string, string, string) {
		path, _, _ := stoppedCatalog(t)
		dir := spaced(t, "backups")
		b := mustBackup(t, path, dir)
		a := serve(t, path)
		a.post("/v1/procedures", procedureBody("recovery.current", ""))
		a.close()
		return path, dir, b.Path, digest(t, path)
	}
	backupSum := func(t *testing.T, backup string) string { return digest(t, filepath.Join(backup, DatabaseFile)) }

	t.Run("running: stopped, restored, started", func(t *testing.T) {
		path, dir, backup, _ := setup(t)
		svc := &fakeService{state: lifecycle.Running, pid: 101, db: path}
		p := MakePlan(ctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: svc})
		if !p.Destination.Managed || p.Service.State != lifecycle.Running || !strings.Contains(strings.Join(p.Actions, "\n"), "stop the managed service (process 101)") {
			t.Fatalf("plan %+v", p)
		}
		res, err := Restore(ctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: svc, Replace: true})
		if err != nil || res.Outcome != Restored || !slices.Equal(svc.calls, []string{"stop", "start"}) || res.Service.State != lifecycle.Running {
			t.Fatalf("restore: %+v %v calls %v", res, err, svc.calls)
		}
		if r := Inspect(ctx, res.RecoveryBackup); !r.Valid || !r.Metadata.Source.Managed {
			t.Errorf("recovery backup %+v", r)
		}
	})
	t.Run("stopped: stays stopped", func(t *testing.T) {
		path, dir, backup, _ := setup(t)
		svc := &fakeService{state: lifecycle.Stopped, db: path}
		res, err := Restore(ctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: svc, Replace: true})
		if err != nil || res.Outcome != Restored || len(svc.calls) != 0 || svc.state != lifecycle.Stopped {
			t.Fatalf("restore: %+v %v calls %v", res, err, svc.calls)
		}
	})
	t.Run("unmanaged path: the service is not touched", func(t *testing.T) {
		path, dir, backup, _ := setup(t)
		svc := &fakeService{state: lifecycle.Running, pid: 101, db: filepath.Join(t.TempDir(), "other.db")}
		res, err := Restore(ctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: svc, Replace: true})
		if err != nil || res.Plan.Destination.Managed || len(svc.calls) != 0 {
			t.Fatalf("restore: %+v %v calls %v", res, err, svc.calls)
		}
	})
	for _, tc := range []struct {
		name string
		svc  fakeService
		want string
	}{
		{"endpoint held by another process", fakeService{state: lifecycle.Failed, conflict: true}, "held by another process"},
		{"failed", fakeService{state: lifecycle.Failed}, "restore needs it running or stopped"},
		{"starting", fakeService{state: lifecycle.Starting, pid: 7}, "restore needs it running or stopped"},
	} {
		t.Run("refused when "+tc.name, func(t *testing.T) {
			path, dir, backup, before := setup(t)
			svc := tc.svc
			svc.db = path
			res, err := Restore(ctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: &svc, Replace: true})
			if !errors.Is(err, ErrRefused) || !strings.Contains(strings.Join(res.Plan.Blockers, " "), tc.want) || len(svc.calls) != 0 || digest(t, path) != before {
				t.Fatalf("got %v %v, calls %v", res.Plan.Blockers, err, svc.calls)
			}
		})
	}
	t.Run("restart fails, shutdown confirmed: the original is put back", func(t *testing.T) {
		path, dir, backup, before := setup(t)
		svc := &fakeService{state: lifecycle.Running, pid: 101, db: path, startErrs: []error{errors.New("injected start failure"), nil}}
		res, err := Restore(ctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: svc, Replace: true})
		if err == nil || !strings.Contains(err.Error(), "injected start failure") || !strings.Contains(err.Error(), "original catalog was put back") || res.Outcome != RolledBack {
			t.Fatalf("restore: %+v, %v", res.Outcome, err)
		}
		if digest(t, path) != before || !slices.Equal(svc.calls, []string{"stop", "start", "stop", "start"}) || svc.state != lifecycle.Running {
			t.Fatalf("after the rollback: original %v, calls %v, state %s", digest(t, path) == before, svc.calls, svc.state)
		}
		if len(res.Kept) != 2 || !strings.Contains(res.Kept[0], ".failed-restore-") || digest(t, res.Kept[0]) != backupSum(t, backup) || !Inspect(ctx, res.Kept[1]).Valid {
			t.Fatalf("evidence kept: %v", res.Kept)
		}
	})
	t.Run("restart and rollback start both fail", func(t *testing.T) {
		path, dir, backup, before := setup(t)
		svc := &fakeService{state: lifecycle.Running, pid: 101, db: path, startErrs: []error{errors.New("first"), errors.New("second")}}
		res, err := Restore(ctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: svc, Replace: true})
		if err == nil || !strings.Contains(err.Error(), "did not start on it either") || res.Outcome != RolledBack || digest(t, path) != before {
			t.Fatalf("restore: %+v, %v", res.Outcome, err)
		}
		if len(res.Kept) != 2 || !strings.Contains(res.Kept[0], ".failed-restore-") || !Inspect(ctx, res.Kept[1]).Valid || res.Service == nil || res.Service.State != lifecycle.Failed {
			t.Fatalf("evidence kept %v, service %+v", res.Kept, res.Service)
		}
	})

	// blocked checks that a rollback that could not confirm shutdown changed
	// no file: the restored catalog stays in place, the original and the
	// recovery backup are kept, and the result says how to recover.
	blocked := func(t *testing.T, path, before string, restored os.FileInfo, res Result, err error, why string) {
		t.Helper()
		if err == nil || errors.Is(err, ErrRefused) || res.Outcome != RollbackBlocked || !strings.Contains(err.Error(), why) {
			t.Fatalf("restore: outcome %s, %v; want %s mentioning %q", res.Outcome, err, RollbackBlocked, why)
		}
		now, statErr := os.Stat(path)
		if statErr != nil || !os.SameFile(restored, now) {
			t.Fatalf("the catalog at %s is no longer the restored file: %v", path, statErr)
		}
		if res.Original == "" || !strings.Contains(res.Original, ".pre-restore-") || digest(t, res.Original) != before {
			t.Fatalf("original %q not kept intact", res.Original)
		}
		if r := Inspect(ctx, res.RecoveryBackup); !r.Valid || r.Records["procedures"] != 3 {
			t.Fatalf("recovery backup %q: %+v", res.RecoveryBackup, r)
		}
		if !slices.Equal(res.Kept, []string{res.Original, res.RecoveryBackup}) {
			t.Errorf("kept %v", res.Kept)
		}
		for _, name := range entries(t, filepath.Dir(path)) {
			switch name {
			case "polaroid.db", "polaroid.db-wal", "polaroid.db-shm", filepath.Base(res.Original):
			default:
				t.Errorf("unexpected file beside the catalog after a blocked rollback: %s", name)
			}
		}
		steps := strings.Join(res.ManualRecovery, "\n")
		for _, want := range []string{path, res.Original, res.RecoveryBackup} {
			if !strings.Contains(steps, want) || !strings.Contains(err.Error(), want) {
				t.Errorf("the recovery steps and the error must name %q:\n%s\n%v", want, steps, err)
			}
		}
		if !strings.Contains(steps, "polaroid restore -replace") {
			t.Errorf("the recovery steps do not say how to put the original back:\n%s", steps)
		}
	}
	// failedStart, on the first start, records the restored file and, if
	// hold, opens it as a daemon that did not become healthy would.
	failedStart := func(t *testing.T, path string, hold bool, restored *os.FileInfo, holder **api) func(int) {
		return func(n int) {
			if n != 1 {
				return
			}
			st, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			*restored = st
			if hold {
				*holder = serve(t, path)
			}
		}
	}

	t.Run("restart fails and the failed process cannot be stopped: rollback blocked", func(t *testing.T) {
		path, dir, backup, before := setup(t)
		var restored os.FileInfo
		var holder *api
		svc := &fakeService{state: lifecycle.Running, pid: 101, db: path, startErrs: []error{errors.New("injected start failure")},
			stopErrs: []error{nil, errors.New("injected: the failed process did not stop")}}
		svc.onStart = failedStart(t, path, true, &restored, &holder)
		res, err := Restore(ctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: svc, Replace: true})
		blocked(t, path, before, restored, res, err, "injected: the failed process did not stop")
		if !slices.Equal(svc.calls, []string{"stop", "start", "stop"}) {
			t.Errorf("calls %v: rollback must not start the service on a catalog it did not put back", svc.calls)
		}
		if sc := sqlite.Sidecars(path); len(sc) == 0 {
			t.Error("the live process's -wal/-shm files are gone")
		}
		// The process that kept running still uses the catalog at the destination.
		holder.post("/v1/procedures", procedureBody("recovery.after-failure", ""))
		if code, _ := holder.call("GET", "/v1/procedures/by-key/recovery.current", ""); code != 404 {
			t.Errorf("the live process does not serve the restored snapshot: %d", code)
		}
		holder.close()
		a := serve(t, path)
		if code, _ := a.call("GET", "/v1/procedures/by-key/recovery.after-failure", ""); code != 200 {
			t.Errorf("a write by the live process is not in the catalog at %s: %d", path, code)
		}
		a.close()
	})
	t.Run("shutdown reported but sidecars remain: rollback blocked, sidecars kept", func(t *testing.T) {
		path, dir, backup, before := setup(t)
		var restored os.FileInfo
		svc := &fakeService{state: lifecycle.Running, pid: 101, db: path, startErrs: []error{errors.New("injected start failure")}}
		svc.onStart = func(n int) {
			failedStart(t, path, false, &restored, nil)(n)
			if n == 1 {
				writeFile(t, path+"-wal", []byte("the failed process's log"))
			}
		}
		res, err := Restore(ctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: svc, Replace: true})
		blocked(t, path, before, restored, res, err, path+"-wal")
		if b := readFile(t, path+"-wal"); string(b) != "the failed process's log" {
			t.Error("the -wal file was changed or removed")
		}
	})
	t.Run("ownership ambiguous after stopping: rollback blocked", func(t *testing.T) {
		path, dir, backup, before := setup(t)
		var restored os.FileInfo
		svc := &fakeService{state: lifecycle.Running, pid: 101, db: path, startErrs: []error{errors.New("injected start failure")}}
		svc.onStart = func(n int) {
			failedStart(t, path, false, &restored, nil)(n)
			svc.conflict = n == 1
		}
		res, err := Restore(ctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: svc, Replace: true})
		blocked(t, path, before, restored, res, err, "held by")
	})
	t.Run("the caller's context expires: rollback blocked", func(t *testing.T) {
		path, dir, backup, before := setup(t)
		var restored os.FileInfo
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		svc := &fakeService{state: lifecycle.Running, pid: 101, db: path, startErrs: []error{errors.New("injected start failure")}}
		svc.onStart = func(n int) {
			failedStart(t, path, false, &restored, nil)(n)
			cancel()
		}
		res, err := Restore(cctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: svc, Replace: true})
		blocked(t, path, before, restored, res, err, "context canceled")
		if !slices.Equal(svc.calls, []string{"stop", "start"}) {
			t.Errorf("calls %v: nothing may be asked of the service once the context expired", svc.calls)
		}
	})
	t.Run("sidecars remain after stopping: aborted and restarted", func(t *testing.T) {
		path, dir, backup, before := setup(t)
		svc := &fakeService{state: lifecycle.Running, pid: 101, db: path, onStop: func() { writeFile(t, path+"-shm", nil) }}
		res, err := Restore(ctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: svc, Replace: true})
		if !errors.Is(err, ErrRefused) || res.Outcome != Unchanged || digest(t, path) != before || !slices.Equal(svc.calls, []string{"stop", "start"}) {
			t.Fatalf("restore: %v, outcome %s, calls %v", err, res.Outcome, svc.calls)
		}
		if _, err := os.Stat(path + "-shm"); err != nil {
			t.Error("the restore removed the -shm file")
		}
	})
	t.Run("stop fails: unchanged", func(t *testing.T) {
		path, dir, backup, before := setup(t)
		svc := &fakeService{state: lifecycle.Running, pid: 101, db: path, stopErr: errors.New("injected stop failure")}
		res, err := Restore(ctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: svc, Replace: true})
		if err == nil || !strings.Contains(err.Error(), "injected stop failure") || res.Outcome != Unchanged || digest(t, path) != before {
			t.Fatalf("restore: %v, outcome %s", err, res.Outcome)
		}
	})
}

func TestBackupNamesAreNeverReused(t *testing.T) {
	path, _, _ := stoppedCatalog(t)
	dir := spaced(t, "backups")
	fixed := func() BackupRequest {
		return BackupRequest{Source: path, Dir: dir, Now: func() time.Time { return time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC) }}
	}
	var paths []string
	for range 3 {
		r, err := Backup(ctx, fixed())
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, filepath.Base(r.Path))
	}
	if !slices.Equal(paths, []string{"polaroid-20261010T000000Z", "polaroid-20261010T000000Z-2", "polaroid-20261010T000000Z-3"}) {
		t.Errorf("names %v", paths)
	}
	for _, p := range paths {
		if !Inspect(ctx, filepath.Join(dir, p)).Valid {
			t.Errorf("%s is invalid", p)
		}
	}
	if _, err := Backup(ctx, BackupRequest{Source: filepath.Join(dir, "missing.db"), Dir: dir}); err == nil {
		t.Error("a backup of a missing catalog succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "missing.db")); err == nil {
		t.Error("backing up a missing catalog created it")
	}
	if names := entries(t, dir); len(names) != 3 {
		t.Errorf("failed backups left %v", names)
	}
}
