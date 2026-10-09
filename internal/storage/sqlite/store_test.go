package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

var created = time.Date(2026, 10, 9, 12, 0, 0, 123456789, time.UTC)

func dbPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "polaroid.db")
}

func openStore(t *testing.T, path string) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func procedure(id, key string) memory.Procedure {
	return memory.Procedure{ID: id, CanonicalKey: key, CreatedAt: created, LatestVersion: 1}
}

func version(procedureID string, n int, instructions string) memory.Version {
	return memory.Version{
		ProcedureID: procedureID,
		Number:      n,
		CreatedAt:   created.Add(time.Duration(n) * time.Minute),
		Definition: memory.Definition{
			Philosophy:     "Leave the system better understood than you found it.",
			Method:         "Inspect, change, verify.",
			Contract:       jsontext.Value(`{"inputs":{"path":"string"}}`),
			Instructions:   jsontext.Value(instructions),
			RevisionReason: fmt.Sprintf("reason for version %d", n),
		},
	}
}

// seed creates procedure "p1" with versions 1..n.
func seed(t *testing.T, s *sqlite.Store, n int) memory.History {
	t.Helper()
	ctx := context.Background()
	h := memory.History{Procedure: procedure("p1", "go.dependency.add")}
	h.Versions = append(h.Versions, version("p1", 1, `{"steps":["one"]}`))
	if err := s.CreateProcedure(ctx, h.Procedure, h.Versions[0]); err != nil {
		t.Fatalf("CreateProcedure: %v", err)
	}
	for i := 2; i <= n; i++ {
		v := version("p1", i, fmt.Sprintf(`{"steps":["v%d"]}`, i))
		if err := s.AppendVersion(ctx, i-1, v); err != nil {
			t.Fatalf("AppendVersion(base %d): %v", i-1, err)
		}
		h.Versions = append(h.Versions, v)
	}
	h.Procedure.LatestVersion = n
	return h
}

func history(t *testing.T, s *sqlite.Store, id string) memory.History {
	t.Helper()
	h, err := s.History(context.Background(), id)
	if err != nil {
		t.Fatalf("History(%s): %v", id, err)
	}
	return h
}

func TestCreateAndReadBack(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	want := seed(t, s, 1)

	if got := history(t, s, "p1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("History = %+v\nwant %+v", got, want)
	}
	byKey, err := s.HistoryByKey(ctx, "go.dependency.add")
	if err != nil || !reflect.DeepEqual(byKey, want) {
		t.Fatalf("HistoryByKey = %+v, %v\nwant %+v", byKey, err, want)
	}
	v1, err := s.Version(ctx, "p1", 1)
	if err != nil || !reflect.DeepEqual(v1, want.Versions[0]) {
		t.Fatalf("Version(1) = %+v, %v", v1, err)
	}
	list, err := s.ListProcedures(ctx)
	if err != nil || !reflect.DeepEqual(list, []memory.Procedure{want.Procedure}) {
		t.Fatalf("ListProcedures = %+v, %v", list, err)
	}
}

func TestListIsOrderedByCanonicalKeyWithLatestVersion(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	seed(t, s, 3) // go.dependency.add
	if err := s.CreateProcedure(ctx, procedure("p2", "a.first"), version("p2", 1, `{}`)); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListProcedures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range list {
		got = append(got, fmt.Sprintf("%s@%d", p.CanonicalKey, p.LatestVersion))
	}
	if want := "a.first@1,go.dependency.add@3"; strings.Join(got, ",") != want {
		t.Fatalf("list = %v, want %s", got, want)
	}
}

func TestDuplicateCanonicalKeyIsRejected(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	original := seed(t, s, 1)

	err := s.CreateProcedure(ctx, procedure("p2", "go.dependency.add"), version("p2", 1, `{}`))
	if !errors.Is(err, memory.ErrCanonicalKeyExists) {
		t.Fatalf("duplicate key: err = %v, want ErrCanonicalKeyExists", err)
	}
	if _, err := s.History(ctx, "p2"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("rejected procedure was partially stored: %v", err)
	}
	if got := history(t, s, "p1"); !reflect.DeepEqual(got, original) {
		t.Fatalf("original procedure changed: %+v", got)
	}
}

func TestAppendRequiresLatestBaseAndKeepsHistory(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	want := seed(t, s, 2)

	for _, base := range []int{1, 3} {
		err := s.AppendVersion(ctx, base, version("p1", base+1, `{"steps":["late"]}`))
		var conflict *memory.VersionConflictError
		if !errors.As(err, &conflict) || conflict.BaseVersion != base || conflict.LatestVersion != 2 {
			t.Fatalf("AppendVersion(base %d) = %v, want conflict with latest 2", base, err)
		}
	}
	if got := history(t, s, "p1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("history changed by rejected revisions:\n got %+v\nwant %+v", got, want)
	}
}

func TestAppendToMissingProcedure(t *testing.T) {
	s := openStore(t, dbPath(t))
	err := s.AppendVersion(context.Background(), 1, version("missing", 2, `{}`))
	if !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestConcurrentAppendsFromSameBase(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	seed(t, s, 1)

	const writers = 8
	start := make(chan struct{})
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			<-start
			errs[i] = s.AppendVersion(ctx, 1, version("p1", 2, fmt.Sprintf(`{"writer":%d}`, i)))
		})
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for i, err := range errs {
		var conflict *memory.VersionConflictError
		switch {
		case err == nil:
			succeeded++
		case errors.As(err, &conflict) && conflict.LatestVersion == 2:
		default:
			t.Errorf("writer %d: unexpected error %v", i, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d writers succeeded from the same base, want exactly 1", succeeded)
	}
	if h := history(t, s, "p1"); len(h.Versions) != 2 {
		t.Fatalf("history has %d versions, want 2", len(h.Versions))
	}
}

func TestReopenPreservesHistory(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	want := seed(t, s, 3)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openStore(t, path)
	if got := history(t, reopened, "p1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("after reopen:\n got %+v\nwant %+v", got, want)
	}
}

// The schema itself must refuse to rewrite history, even for a client that
// bypasses Store.
func TestSchemaRejectsChangesToStoredRecords(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	want := seed(t, s, 2)

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()

	const insertVersion = `INSERT INTO procedure_versions
		(procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at)
		VALUES ('p1', %d, 'p', 'm', '%s', '{}', 'r', '2026-10-09T12:00:00.000000000Z')`
	statements := map[string]string{
		"update version":     `UPDATE procedure_versions SET instructions = '{"steps":[]}' WHERE procedure_id = 'p1' AND version = 1`,
		"delete version":     `DELETE FROM procedure_versions WHERE procedure_id = 'p1' AND version = 2`,
		"update identity":    `UPDATE procedures SET canonical_key = 'other' WHERE id = 'p1'`,
		"delete procedure":   `DELETE FROM procedures WHERE id = 'p1'`,
		"skip a version":     fmt.Sprintf(insertVersion, 4, `{}`),
		"reuse a version":    fmt.Sprintf(insertVersion, 2, `{}`),
		"non-object JSON":    fmt.Sprintf(insertVersion, 3, `[]`),
		"invalid key format": `INSERT INTO procedures (id, canonical_key, created_at) VALUES ('p9', 'Not A Key', '2026-10-09T12:00:00.000000000Z')`,
	}
	for name, stmt := range statements {
		if _, err := raw.ExecContext(context.Background(), stmt); err == nil {
			t.Errorf("%s: statement was accepted: %s", name, stmt)
		}
	}
	if got := history(t, s, "p1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("history changed:\n got %+v\nwant %+v", got, want)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := dbPath(t)
	if err := openStore(t, path).Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(context.Background(), `PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	if s, err := sqlite.Open(context.Background(), path); err == nil {
		_ = s.Close()
		t.Fatal("Open accepted a database with a newer schema")
	} else if !strings.Contains(err.Error(), "newer than this build supports") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenRejectsNonFilePaths(t *testing.T) {
	for _, path := range []string{"", ":memory:", "file:polaroid.db", "polaroid.db?mode=ro"} {
		if s, err := sqlite.Open(context.Background(), path); err == nil {
			_ = s.Close()
			t.Errorf("Open(%q) succeeded", path)
		}
	}
}

func TestMissingRecordsAreNotFound(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	seed(t, s, 1)

	_, errHistory := s.History(ctx, "missing")
	_, errKey := s.HistoryByKey(ctx, "missing.key")
	_, errVersion := s.Version(ctx, "p1", 2)
	_, errProcedureVersion := s.Version(ctx, "missing", 1)
	for name, err := range map[string]error{"History": errHistory, "HistoryByKey": errKey, "Version": errVersion, "Version of missing procedure": errProcedureVersion} {
		if !errors.Is(err, memory.ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
}
