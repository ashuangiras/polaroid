package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

func repository(id, identifier string, n int) *memory.Repository {
	return &memory.Repository{ID: id, Name: "Repository " + id, Identifier: identifier, CreatedAt: created.Add(time.Duration(n) * time.Hour)}
}

func mustRegister(t *testing.T, s *sqlite.Store, r *memory.Repository) {
	t.Helper()
	if err := s.CreateRepository(context.Background(), r); err != nil {
		t.Fatalf("CreateRepository(%s): %v", r.Identifier, err)
	}
}

func TestRegisterAndLookUpRepositories(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	a, b := repository("ra", "github.com/o/a", 1), repository("rb", "github.com/o/b", 2)
	mustRegister(t, s, a)
	mustRegister(t, s, b)
	alias := memory.RepositoryAlias{Identifier: "mirror.example/o/a", Reason: "A's mirror.", CreatedAt: created.Add(3 * time.Hour)}
	if err := s.AddRepositoryAlias(ctx, "ra", alias); err != nil {
		t.Fatal(err)
	}
	a.Aliases = []memory.RepositoryAlias{alias}

	for _, lookup := range []func() (memory.Repository, error){
		func() (memory.Repository, error) { return s.Repository(ctx, "ra") },
		func() (memory.Repository, error) { return s.RepositoryByIdentifier(ctx, "github.com/o/a") },
		func() (memory.Repository, error) { return s.RepositoryByIdentifier(ctx, "mirror.example/o/a") },
	} {
		if got, err := lookup(); err != nil || !reflect.DeepEqual(got, *a) {
			t.Fatalf("lookup = %+v, %v; want %+v", got, err, *a)
		}
	}
	if all, err := s.ListRepositories(ctx, nil, 0); err != nil || !reflect.DeepEqual(all, []memory.Repository{*a, *b}) {
		t.Fatalf("ListRepositories = %+v, %v", all, err)
	}
	if page, err := s.ListRepositories(ctx, &memory.Position{At: a.CreatedAt, ID: a.ID}, 1); err != nil || !reflect.DeepEqual(page, []memory.Repository{*b}) {
		t.Fatalf("ListRepositories after a = %+v, %v", page, err)
	}

	for _, identifier := range []string{"github.com/o/a", "mirror.example/o/a", "github.com/o/b"} {
		if err := s.CreateRepository(ctx, repository("rc", identifier, 4)); !errors.Is(err, memory.ErrIdentifierExists) {
			t.Errorf("registering %s again: err = %v", identifier, err)
		}
		if err := s.AddRepositoryAlias(ctx, "rb", memory.RepositoryAlias{Identifier: identifier, Reason: "r", CreatedAt: created}); identifier != "github.com/o/b" && !errors.Is(err, memory.ErrIdentifierExists) {
			t.Errorf("aliasing %s to b: err = %v", identifier, err)
		}
	}
	if err := s.AddRepositoryAlias(ctx, "missing", memory.RepositoryAlias{Identifier: "x/y", Reason: "r", CreatedAt: created}); !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("alias of a missing repository: err = %v", err)
	}
	if _, err := s.RepositoryByIdentifier(ctx, "github.com/o/c"); !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("unregistered identifier: err = %v", err)
	}
	if all, _ := s.ListRepositories(ctx, nil, 0); len(all) != 2 {
		t.Errorf("rejected writes stored repositories: %+v", all)
	}
}

func TestConcurrentRegistrationOfOneIdentifier(t *testing.T) {
	s := openStore(t, dbPath(t))
	const writers = 8
	start := make(chan struct{})
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			<-start
			if i%2 == 0 {
				errs[i] = s.CreateRepository(context.Background(), repository(fmt.Sprint("r", i), "github.com/o/same", i))
				return
			}
			mustRegisterOnce(s, fmt.Sprint("own", i), fmt.Sprint("github.com/o/own", i))
			errs[i] = s.AddRepositoryAlias(context.Background(), fmt.Sprint("own", i), memory.RepositoryAlias{Identifier: "github.com/o/same", Reason: "r", CreatedAt: created})
		})
	}
	close(start)
	wg.Wait()
	succeeded := 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case !errors.Is(err, memory.ErrIdentifierExists):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d writers registered the same identifier, want exactly 1", succeeded)
	}
}

func mustRegisterOnce(s *sqlite.Store, id, identifier string) {
	_ = s.CreateRepository(context.Background(), repository(id, identifier, 0))
}

func TestBindingNamesAreUniqueAcrossARepositorysIdentifiers(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	seed(t, s, 1)
	for _, b := range []memory.Binding{binding("b1", "github.com/o/a", "verify"), binding("b2", "mirror.example/o/a", "verify")} {
		if err := s.CreateBinding(ctx, b, revision(b.ID, 1, contextual, `{}`)); err != nil {
			t.Fatal(err)
		}
	}
	mustRegister(t, s, repository("ra", "github.com/o/a", 1))
	var conflict *memory.BindingNameConflictError
	if err := s.AddRepositoryAlias(ctx, "ra", memory.RepositoryAlias{Identifier: "mirror.example/o/a", Reason: "r", CreatedAt: created}); !errors.As(err, &conflict) || conflict.Name != "verify" {
		t.Fatalf("alias with a duplicate binding name: err = %v", err)
	}
	if _, err := s.RepositoryByIdentifier(ctx, "mirror.example/o/a"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("the refused alias was stored: %v", err)
	}
	if err := s.AddRepositoryAlias(ctx, "ra", memory.RepositoryAlias{Identifier: "old.example/o/a", Reason: "r", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBinding(ctx, binding("b3", "old.example/o/a", "verify"), revision("b3", 1, contextual, `{}`)); !errors.Is(err, memory.ErrBindingExists) {
		t.Fatalf("binding name taken under the canonical identifier: err = %v", err)
	}

	// Two writers race to bind one name under two identifiers of a repository.
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, identifier := range []string{"github.com/o/a", "old.example/o/a"} {
		wg.Go(func() {
			<-start
			id := fmt.Sprint("race", i)
			errs[i] = s.CreateBinding(ctx, binding(id, identifier, "race"), revision(id, 1, contextual, `{}`))
		})
	}
	close(start)
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) || !errors.Is(errors.Join(errs...), memory.ErrBindingExists) {
		t.Fatalf("racing bindings: %v, want exactly one binding_exists", errs)
	}
	list, err := s.ListBindings(ctx, memory.BindingFilter{Repository: "old.example/o/a"})
	if err != nil || len(list) != 2 {
		t.Fatalf("bindings of the repository = %+v, %v", list, err)
	}
}

// created_at follows commit order in the time-ordered lists, so a cursor
// never skips a later commit (ADR-0021).
func TestCreationTimesFollowCommitOrder(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	later, earlier := feedback("f1", 5, memory.FeedbackProblem), feedback("f2", 1, memory.FeedbackProblem)
	for _, f := range []*memory.Feedback{&later, &earlier} {
		if err := s.CreateFeedback(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	if want := later.CreatedAt.Add(time.Nanosecond); !earlier.CreatedAt.Equal(want) {
		t.Fatalf("second report's created_at = %v, want %v", earlier.CreatedAt, want)
	}
	all, err := s.ListFeedback(ctx, memory.FeedbackFilter{})
	if err != nil || len(all) != 2 || all[0].ID != "f1" || all[1].ID != "f2" || !all[1].CreatedAt.Equal(earlier.CreatedAt) {
		t.Fatalf("ListFeedback = %+v, %v; want commit order with the stored times", all, err)
	}
}

// The schema refuses changes to the registry and origins, inconsistent
// scopes and missing feedback subjects, even from a client that bypasses
// Store.
func TestSchemaRejectsChangesToRepositoriesAndScopes(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	mustRegister(t, s, repository("ra", "github.com/o/a", 1))
	seed(t, s, 1)
	if err := s.SetOrigin(context.Background(), "p1", memory.Origin{RepositoryID: "ra", Reason: "r", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	for name, stmt := range map[string]string{
		"rename a repository":        `UPDATE repositories SET name = 'x'`,
		"delete a repository":        `DELETE FROM repositories`,
		"move an identifier":         `UPDATE repository_identifiers SET identifier = 'x'`,
		"delete an identifier":       `DELETE FROM repository_identifiers`,
		"second canonical":           `INSERT INTO repository_identifiers VALUES ('x/y', 'ra', 1, NULL, '2026-10-09T12:00:00.000000000Z')`,
		"alias without reason":       `INSERT INTO repository_identifiers VALUES ('x/y', 'ra', 0, NULL, '2026-10-09T12:00:00.000000000Z')`,
		"uppercase identifier":       `INSERT INTO repository_identifiers VALUES ('X/y', 'ra', 0, 'r', '2026-10-09T12:00:00.000000000Z')`,
		"change an origin":           `UPDATE procedure_origins SET reason = 'x'`,
		"delete an origin":           `DELETE FROM procedure_origins`,
		"local without repository":   `INSERT INTO procedure_versions (procedure_id, version, philosophy, method, scope, contract, instructions, revision_reason, created_at) VALUES ('p1', 2, 'p', 'm', 'local', '{}', '{}', 'r', '2026-10-09T12:00:00.000000000Z')`,
		"shared with a repository":   `INSERT INTO procedure_versions (procedure_id, version, philosophy, method, scope, scope_repository_id, contract, instructions, revision_reason, created_at) VALUES ('p1', 2, 'p', 'm', 'shared', 'ra', '{}', '{}', 'r', '2026-10-09T12:00:00.000000000Z')`,
		"multi-line goal":            `INSERT INTO procedure_versions (procedure_id, version, philosophy, method, goal, contract, instructions, revision_reason, created_at) VALUES ('p1', 2, 'p', 'm', 'a` + "\n" + `b', '{}', '{}', 'r', '2026-10-09T12:00:00.000000000Z')`,
		"missing subject":            `INSERT INTO feedback (id, kind, summary, details, reporter, context, subject_type, subject_id, created_at) VALUES ('f', 'problem', 's', 'd', 'r', '{}', 'procedure', 'nope', '2026-10-09T12:00:00.000000000Z')`,
		"missing subject version":    `INSERT INTO feedback (id, kind, summary, details, reporter, context, subject_type, subject_id, subject_version, created_at) VALUES ('f', 'problem', 's', 'd', 'r', '{}', 'procedure', 'p1', 9, '2026-10-09T12:00:00.000000000Z')`,
		"service subject with an ID": `INSERT INTO feedback (id, kind, summary, details, reporter, context, subject_type, subject_id, created_at) VALUES ('f', 'problem', 's', 'd', 'r', '{}', 'service', 'p1', '2026-10-09T12:00:00.000000000Z')`,
	} {
		if _, err := raw.ExecContext(context.Background(), stmt); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := raw.ExecContext(context.Background(), `INSERT INTO feedback (id, kind, summary, details, reporter, context, subject_type, subject_id, subject_version, created_at)
		VALUES ('ok', 'problem', 's', 'd', 'r', '{}', 'procedure', 'p1', 1, '2026-10-09T12:00:00.000000000Z')`); err != nil {
		t.Errorf("a valid subject was refused: %v", err)
	}
}

// A database written at schema version 6, with every kind of record, reads
// back unchanged after migration 7, and again after reopening.
func TestOpenMigratesSchemaVersion6(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	raw, err := sql.Open("sqlite", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	for i, file := range []string{"0001_procedures", "0002_bindings", "0003_procedure_references", "0004_executions", "0005_execution_children", "0006_feedback"} {
		script, err := os.ReadFile("migrations/" + file + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, string(script)); err != nil {
			t.Fatalf("migration %d: %v", i+1, err)
		}
	}
	const at = "2026-10-09T12:00:00.123456789Z"
	commit := strings.Repeat("ab", 20)
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`PRAGMA user_version = 6`,
		`INSERT INTO procedures (id, canonical_key, created_at) VALUES ('leaf', 'go.module.build', '` + at + `')`,
		`INSERT INTO procedure_versions (procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at)
		 VALUES ('leaf', 1, 'p', 'm', '{"inputs":{}}', '{"steps":["build"]}', 'r', '` + at + `')`,
		`INSERT INTO procedures (id, canonical_key, created_at) VALUES ('root', 'dev.change.verify', '` + at + `')`,
		`INSERT INTO procedure_version_references (procedure_id, version, position, name, target_procedure_id, policy, pinned_version, inputs)
		 VALUES ('root', 1, 0, 'build', 'leaf', 'contextual', NULL, '{"cmd":{"input":"cmd"}}')`,
		`INSERT INTO procedure_versions (procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at)
		 VALUES ('root', 1, 'p', 'm', '{}', '{"steps":["verify"]}', 'r', '` + at + `')`,
		`INSERT INTO bindings (id, repository, name, procedure_id, created_at) VALUES ('b1', 'github.com/o/a', 'verify', 'root', '` + at + `')`,
		`INSERT INTO binding_revisions (binding_id, revision, inputs, policy, pinned_version, revision_reason, created_at)
		 VALUES ('b1', 1, '{"cmd":"make"}', 'contextual', NULL, 'r', '` + at + `')`,
		`INSERT INTO executions (id, procedure_id, version, binding_id, binding_revision, repository, commit_hash, environment_name, environment_attributes, inputs, outcome, evidence, created_at)
		 VALUES ('child', 'leaf', 1, NULL, NULL, 'github.com/o/a', '` + commit + `', 'ci', '{}', '{"cmd":"make"}', 'succeeded', '{"exit":0}', '2026-10-09T12:00:01.000000000Z')`,
		`INSERT INTO execution_children (parent_execution_id, parent_procedure_id, parent_version, parent_repository, parent_commit_hash, position, reference, child_execution_id)
		 VALUES ('parent', 'root', 1, 'github.com/o/a', '` + commit + `', 0, 'build', 'child')`,
		`INSERT INTO executions (id, procedure_id, version, binding_id, binding_revision, repository, commit_hash, environment_name, environment_attributes, inputs, outcome, evidence, created_at)
		 VALUES ('parent', 'root', 1, 'b1', 1, 'github.com/o/a', '` + commit + `', 'ci', '{"os":"linux"}', '{"cmd":"make"}', 'succeeded', '{"run":1}', '2026-10-09T12:00:02.000000000Z')`,
		`INSERT INTO feedback (id, kind, summary, details, reporter, context, created_at)
		 VALUES ('f1', 'problem', 's', 'd', 'copilot.vscode', '{"tool":"x"}', '` + at + `')`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	stamp, _ := time.Parse(time.RFC3339Nano, at)
	wantRoot := memory.History{
		Procedure: memory.Procedure{ID: "root", CanonicalKey: "dev.change.verify", CreatedAt: stamp, LatestVersion: 1},
		Versions: []memory.Version{{ProcedureID: "root", Number: 1, CreatedAt: stamp, Definition: memory.Definition{
			Philosophy: "p", Method: "m", Contract: jsontext.Value(`{}`), Instructions: jsontext.Value(`{"steps":["verify"]}`), RevisionReason: "r",
			References: []memory.Reference{{Name: "build", ProcedureID: "leaf", VersionPolicy: memory.VersionPolicy{Kind: memory.PolicyContextual}, Inputs: jsontext.Value(`{"cmd":{"input":"cmd"}}`)}},
		}}},
	}
	check := func(s *sqlite.Store) {
		t.Helper()
		if got, err := s.History(ctx, "root"); err != nil || !reflect.DeepEqual(got, wantRoot) {
			t.Fatalf("History(root) = %+v, %v\nwant %+v", got, err, wantRoot)
		}
		b, err := s.BindingHistory(ctx, "b1")
		if err != nil || b.Binding.Repository != "github.com/o/a" || string(b.Revisions[0].Inputs) != `{"cmd":"make"}` {
			t.Fatalf("BindingHistory(b1) = %+v, %v", b, err)
		}
		e, err := s.Execution(ctx, "parent")
		if err != nil || string(e.Evidence) != `{"run":1}` || len(e.Children) != 1 || e.Children[0].ExecutionID != "child" || e.BindingID != "b1" {
			t.Fatalf("Execution(parent) = %+v, %v", e, err)
		}
		v, err := s.ExecutionVerification(ctx, "parent")
		if err != nil || !v.Verified || v.Combination.Repository != "github.com/o/a" || string(v.Combination.Inputs) != `{"cmd":"make"}` ||
			!reflect.DeepEqual(v.Combination.Children, []memory.ChildVersion{{Reference: "build", Version: 1}}) {
			t.Fatalf("verification = %+v, %v", v, err)
		}
		f, err := s.Feedback(ctx, "f1")
		if err != nil || f.Subject != nil || f.Repository != "" || string(f.Context) != `{"tool":"x"}` {
			t.Fatalf("Feedback(f1) = %+v, %v", f, err)
		}
		about, err := s.ListFeedback(ctx, memory.FeedbackFilter{SubjectType: memory.SubjectService})
		if err != nil || len(about) != 1 {
			t.Fatalf("reports about the service = %+v, %v; want the report without a subject", about, err)
		}
		if repos, err := s.ListRepositories(ctx, nil, 0); err != nil || len(repos) != 0 {
			t.Fatalf("the migration registered repositories: %+v, %v", repos, err)
		}
	}
	s := openStore(t, path)
	check(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	check(openStore(t, path))

	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	var schema int
	if err := raw.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil || schema != 7 {
		t.Fatalf("schema version = %d, %v; want 7", schema, err)
	}
}
