package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

var (
	pin2       = memory.VersionPolicy{Kind: memory.PolicyPin, Pin: 2}
	contextual = memory.VersionPolicy{Kind: memory.PolicyContextual}
)

func binding(id, repository, name string) memory.Binding {
	return memory.Binding{ID: id, Repository: repository, Name: name, ProcedureID: "p1", CreatedAt: created, LatestRevision: 1}
}

func revision(bindingID string, n int, policy memory.VersionPolicy, inputs string) memory.BindingRevision {
	return memory.BindingRevision{
		BindingID: bindingID,
		Number:    n,
		CreatedAt: created.Add(time.Duration(n) * time.Second),
		BindingConfig: memory.BindingConfig{
			Inputs:         jsontext.Value(inputs),
			VersionPolicy:  policy,
			RevisionReason: fmt.Sprintf("reason for revision %d", n),
		},
	}
}

// seedBinding creates binding "b1" of procedure "p1" with revisions 1..n.
func seedBinding(t *testing.T, s *sqlite.Store, n int) memory.BindingHistory {
	t.Helper()
	ctx := context.Background()
	h := memory.BindingHistory{Binding: binding("b1", "github.com/ashuangiras/polaroid", "add-dependency")}
	h.Revisions = append(h.Revisions, revision("b1", 1, pin2, `{"module":"modernc.org/sqlite"}`))
	if err := s.CreateBinding(ctx, h.Binding, h.Revisions[0]); err != nil {
		t.Fatalf("CreateBinding: %v", err)
	}
	for i := 2; i <= n; i++ {
		r := revision("b1", i, contextual, fmt.Sprintf(`{"revision":%d}`, i))
		if err := s.AppendBindingRevision(ctx, i-1, r); err != nil {
			t.Fatalf("AppendBindingRevision(base %d): %v", i-1, err)
		}
		h.Revisions = append(h.Revisions, r)
	}
	h.Binding.LatestRevision = n
	return h
}

func bindingHistory(t *testing.T, s *sqlite.Store, id string) memory.BindingHistory {
	t.Helper()
	h, err := s.BindingHistory(context.Background(), id)
	if err != nil {
		t.Fatalf("BindingHistory(%s): %v", id, err)
	}
	return h
}

func TestTwoRepositoriesBindOneProcedure(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	procedure := seed(t, s, 2)

	first := seedBinding(t, s, 1)
	second := memory.BindingHistory{
		Binding:   binding("b2", "scratch", "add-dependency"),
		Revisions: []memory.BindingRevision{revision("b2", 1, contextual, `{}`)},
	}
	if err := s.CreateBinding(ctx, second.Binding, second.Revisions[0]); err != nil {
		t.Fatalf("CreateBinding(second repository): %v", err)
	}

	for _, want := range []memory.BindingHistory{first, second} {
		got := bindingHistory(t, s, want.Binding.ID)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BindingHistory = %+v\nwant %+v", got, want)
		}
		if got.Binding.ProcedureID != "p1" {
			t.Fatalf("binding %s resolves to procedure %s, want p1", got.Binding.ID, got.Binding.ProcedureID)
		}
		r, err := s.BindingRevision(ctx, want.Binding.ID, 1)
		if err != nil || !reflect.DeepEqual(r, want.Revisions[0]) {
			t.Fatalf("BindingRevision(%s, 1) = %+v, %v", want.Binding.ID, r, err)
		}
		list, err := s.ListBindings(ctx, memory.BindingFilter{Repository: want.Binding.Repository})
		if err != nil || !reflect.DeepEqual(list, []memory.Binding{want.Binding}) {
			t.Fatalf("ListBindings(%s) = %+v, %v", want.Binding.Repository, list, err)
		}
	}
	if got := history(t, s, "p1"); !reflect.DeepEqual(got, procedure) {
		t.Fatalf("binding changed the procedure's history:\n got %+v\nwant %+v", got, procedure)
	}
}

func TestListBindingsIsPerRepositoryAndOrderedByName(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	seed(t, s, 2)
	seedBinding(t, s, 3) // add-dependency
	for _, b := range []memory.Binding{
		binding("b2", "github.com/ashuangiras/polaroid", "a-first"),
		binding("b3", "github.com/ashuangiras/other", "only-here"),
	} {
		if err := s.CreateBinding(ctx, b, revision(b.ID, 1, contextual, `{}`)); err != nil {
			t.Fatal(err)
		}
	}

	list, err := s.ListBindings(ctx, memory.BindingFilter{Repository: "github.com/ashuangiras/polaroid"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range list {
		got = append(got, fmt.Sprintf("%s@%d", b.Name, b.LatestRevision))
	}
	if want := []string{"a-first@1", "add-dependency@3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("list = %v, want %v", got, want)
	}
	if empty, err := s.ListBindings(ctx, memory.BindingFilter{Repository: "no/such/repository"}); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("ListBindings(unknown) = %#v, %v; want an empty, non-nil list", empty, err)
	}
}

func TestCreateBindingRejections(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	seed(t, s, 2)
	original := seedBinding(t, s, 1)

	unknown := binding("b2", "scratch", "x")
	unknown.ProcedureID = "missing"
	if err := s.CreateBinding(ctx, unknown, revision("b2", 1, contextual, `{}`)); !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("unknown procedure: err = %v, want ErrNotFound", err)
	}
	pin3 := memory.VersionPolicy{Kind: memory.PolicyPin, Pin: 3}
	if err := s.CreateBinding(ctx, binding("b2", "scratch", "x"), revision("b2", 1, pin3, `{}`)); !errors.Is(err, memory.ErrPinnedVersionNotFound) {
		t.Errorf("missing pinned version: err = %v, want ErrPinnedVersionNotFound", err)
	}
	if err := s.CreateBinding(ctx, binding("b2", original.Binding.Repository, original.Binding.Name), revision("b2", 1, contextual, `{}`)); !errors.Is(err, memory.ErrBindingExists) {
		t.Errorf("duplicate (repository, name): err = %v, want ErrBindingExists", err)
	}
	if _, err := s.BindingHistory(ctx, "b2"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("a rejected binding was partially stored: %v", err)
	}
	if got := bindingHistory(t, s, "b1"); !reflect.DeepEqual(got, original) {
		t.Fatalf("original binding changed: %+v", got)
	}

	// The same procedure under another name, and the same name in another
	// repository, are distinct bindings.
	for _, b := range []memory.Binding{
		binding("b3", original.Binding.Repository, "add-dependency-again"),
		binding("b4", "scratch", original.Binding.Name),
	} {
		if err := s.CreateBinding(ctx, b, revision(b.ID, 1, pin2, `{}`)); err != nil {
			t.Errorf("CreateBinding(%s/%s): %v", b.Repository, b.Name, err)
		}
	}
}

func TestAppendBindingRevisionRequiresLatestBaseAndKeepsHistory(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	seed(t, s, 2)
	want := seedBinding(t, s, 2)

	for _, base := range []int{1, 3} {
		err := s.AppendBindingRevision(ctx, base, revision("b1", base+1, contextual, `{"late":true}`))
		var conflict *memory.RevisionConflictError
		if !errors.As(err, &conflict) || conflict.BaseRevision != base || conflict.LatestRevision != 2 {
			t.Fatalf("AppendBindingRevision(base %d) = %v, want conflict with latest 2", base, err)
		}
	}
	pin9 := memory.VersionPolicy{Kind: memory.PolicyPin, Pin: 9}
	if err := s.AppendBindingRevision(ctx, 2, revision("b1", 3, pin9, `{}`)); !errors.Is(err, memory.ErrPinnedVersionNotFound) {
		t.Fatalf("pin to a missing version: err = %v, want ErrPinnedVersionNotFound", err)
	}
	if err := s.AppendBindingRevision(ctx, 1, revision("missing", 2, contextual, `{}`)); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("missing binding: err = %v, want ErrNotFound", err)
	}
	if got := bindingHistory(t, s, "b1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("history changed by rejected revisions:\n got %+v\nwant %+v", got, want)
	}
}

func TestConcurrentBindingRevisionsFromSameBase(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	seed(t, s, 2)
	seedBinding(t, s, 1)

	const writers = 8
	start := make(chan struct{})
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			<-start
			errs[i] = s.AppendBindingRevision(ctx, 1, revision("b1", 2, contextual, fmt.Sprintf(`{"writer":%d}`, i)))
		})
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for i, err := range errs {
		var conflict *memory.RevisionConflictError
		switch {
		case err == nil:
			succeeded++
		case errors.As(err, &conflict) && conflict.LatestRevision == 2:
		default:
			t.Errorf("writer %d: unexpected error %v", i, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d writers succeeded from the same base, want exactly 1", succeeded)
	}
	if h := bindingHistory(t, s, "b1"); len(h.Revisions) != 2 {
		t.Fatalf("history has %d revisions, want 2", len(h.Revisions))
	}
}

func TestReopenPreservesBindings(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	seed(t, s, 2)
	want := seedBinding(t, s, 3)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openStore(t, path)
	if got := bindingHistory(t, reopened, "b1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("after reopen:\n got %+v\nwant %+v", got, want)
	}
}

// The schema itself must refuse to rewrite binding history, even for a
// client that bypasses Store.
func TestSchemaRejectsChangesToBindings(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	seed(t, s, 2)
	want := seedBinding(t, s, 2)

	raw, err := sql.Open("sqlite", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()

	const insertRevision = `INSERT INTO binding_revisions
		(binding_id, revision, inputs, policy, pinned_version, revision_reason, created_at)
		VALUES ('b1', %d, '%s', '%s', %s, 'r', '2026-10-09T12:00:00.000000000Z')`
	const insertBinding = `INSERT INTO bindings (id, repository, name, procedure_id, created_at)
		VALUES ('b9', '%s', 'x', '%s', '2026-10-09T12:00:00.000000000Z')`
	statements := map[string]string{
		"update revision":          `UPDATE binding_revisions SET inputs = '{"x":1}' WHERE binding_id = 'b1' AND revision = 1`,
		"delete revision":          `DELETE FROM binding_revisions WHERE binding_id = 'b1' AND revision = 2`,
		"update binding":           `UPDATE bindings SET procedure_id = 'p2' WHERE id = 'b1'`,
		"delete binding":           `DELETE FROM bindings WHERE id = 'b1'`,
		"skip a revision":          fmt.Sprintf(insertRevision, 4, `{}`, "contextual", "NULL"),
		"reuse a revision":         fmt.Sprintf(insertRevision, 2, `{}`, "contextual", "NULL"),
		"non-object inputs":        fmt.Sprintf(insertRevision, 3, `[]`, "contextual", "NULL"),
		"pin a missing version":    fmt.Sprintf(insertRevision, 3, `{}`, "pin", "3"),
		"pin without a version":    fmt.Sprintf(insertRevision, 3, `{}`, "pin", "NULL"),
		"contextual with version":  fmt.Sprintf(insertRevision, 3, `{}`, "contextual", "1"),
		"unknown policy":           fmt.Sprintf(insertRevision, 3, `{}`, "latest", "NULL"),
		"invalid repository":       fmt.Sprintf(insertBinding, "GitHub.com/O/R", "p1"),
		"empty repository segment": fmt.Sprintf(insertBinding, "github.com//r", "p1"),
		"unknown procedure":        fmt.Sprintf(insertBinding, "scratch", "missing"),
	}
	for name, stmt := range statements {
		if _, err := raw.ExecContext(context.Background(), stmt); err == nil {
			t.Errorf("%s: statement was accepted: %s", name, stmt)
		}
	}
	if got := bindingHistory(t, s, "b1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("history changed:\n got %+v\nwant %+v", got, want)
	}
}

// A database created before bindings existed is upgraded in place, and its
// procedures are kept.
func TestOpenMigratesSchemaVersion1(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	script, err := os.ReadFile("migrations/0001_procedures.sql")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		string(script),
		`PRAGMA user_version = 1`,
		`INSERT INTO procedures (id, canonical_key, created_at) VALUES ('p1', 'go.dependency.add', '2026-10-09T12:00:00.000000000Z')`,
		`INSERT INTO procedure_versions (procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at)
		 VALUES ('p1', 1, 'p', 'm', '{}', '{}', 'r', '2026-10-09T12:00:00.000000000Z')`,
	} {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	_ = raw.Close()

	s := openStore(t, path)
	if h := history(t, s, "p1"); len(h.Versions) != 1 || h.Procedure.CanonicalKey != "go.dependency.add" {
		t.Fatalf("procedure after migration = %+v", h)
	}
	b := binding("b1", "scratch", "x")
	if err := s.CreateBinding(ctx, b, revision("b1", 1, memory.VersionPolicy{Kind: memory.PolicyPin, Pin: 1}, `{}`)); err != nil {
		t.Fatalf("CreateBinding after migration: %v", err)
	}
}

func TestMissingBindingsAreNotFound(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	seed(t, s, 2)
	seedBinding(t, s, 1)

	_, errHistory := s.BindingHistory(ctx, "missing")
	_, errRevision := s.BindingRevision(ctx, "b1", 2)
	_, errBindingRevision := s.BindingRevision(ctx, "missing", 1)
	for name, err := range map[string]error{"BindingHistory": errHistory, "BindingRevision": errRevision, "BindingRevision of missing binding": errBindingRevision} {
		if !errors.Is(err, memory.ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
}
