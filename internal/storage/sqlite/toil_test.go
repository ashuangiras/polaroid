package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

func toilRevision(item string, n int, status memory.ToilStatus, sources ...memory.ToilSource) memory.ToilRevision {
	r := memory.ToilRevision{ToilItemID: item, Number: n, CreatedAt: time.Date(2026, 10, 11, 12, 0, n, 0, time.UTC), ToilContent: memory.ToilContent{
		Activity:        "Classify changed files by hand",
		Applicability:   memory.Applicability{Kind: memory.ScopeShared},
		Sources:         sources,
		Assessment:      jsontext.Value(`{"repetition":{"status":"observed"},"burden":{"status":"hypothesis"}}`),
		Status:          status,
		StatusRationale: "r",
		RevisionReason:  "r",
		Author:          "test.agent",
	}}
	if status != memory.ToilObserved && status != memory.ToilRetired {
		r.Proposal = jsontext.Value(`{"inputs":1,"outputs":2,"remaining_judgment":3,"failure_and_recovery":4,"acceptance_criteria":["c"]}`)
	}
	return r
}

func mustCreateToil(t *testing.T, s *sqlite.Store, id, key string, first memory.ToilRevision) {
	t.Helper()
	item := memory.ToilItem{ID: id, CanonicalKey: key, CreatedAt: first.CreatedAt}
	if err := s.CreateToilItem(context.Background(), item, first); err != nil {
		t.Fatalf("CreateToilItem(%s): %v", key, err)
	}
}

// TestOpenMigratesSchemaVersion8 applies migrations 1 to 8 to a new file,
// stores every record kind of schema 8 with raw SQL, and opens it with this
// build: every record reads back unchanged, nothing appears in the toil
// tables, and toil items can then link the old records (migration 9).
func TestOpenMigratesSchemaVersion8(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	raw, err := sql.Open("sqlite", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	for i, file := range []string{"0001_procedures", "0002_bindings", "0003_procedure_references", "0004_executions",
		"0005_execution_children", "0006_feedback", "0007_repositories_scope_feedback", "0008_conditional_references"} {
		script, err := os.ReadFile("migrations/" + file + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, string(script)); err != nil {
			t.Fatalf("migration %d: %v", i+1, err)
		}
	}
	const at = "2026-10-10T12:00:00.000000000Z"
	commit := strings.Repeat("cd", 20)
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`PRAGMA user_version = 8`,
		`INSERT INTO repositories (id, name, created_at) VALUES ('repo-a', 'A', '` + at + `')`,
		`INSERT INTO repository_identifiers (identifier, repository_id, canonical, reason, created_at) VALUES ('github.com/o/a', 'repo-a', 1, NULL, '` + at + `')`,
		`INSERT INTO procedures (id, canonical_key, created_at) VALUES ('leaf', 'go.module.build', '` + at + `')`,
		`INSERT INTO procedure_versions (procedure_id, version, philosophy, method, goal, scope, scope_repository_id, contract, instructions, revision_reason, created_at)
		 VALUES ('leaf', 1, 'p', 'm', 'Build.', 'shared', NULL, '{}', '{"steps":[{"id":"build"}]}', 'r', '` + at + `')`,
		`INSERT INTO procedure_origins (procedure_id, repository_id, reason, created_at) VALUES ('leaf', 'repo-a', 'made here', '` + at + `')`,
		`INSERT INTO procedures (id, canonical_key, created_at) VALUES ('root', 'dev.change.verify', '` + at + `')`,
		`INSERT INTO procedure_version_references (procedure_id, version, position, name, target_procedure_id, policy, pinned_version, inputs, condition)
		 VALUES ('root', 1, 0, 'build', 'leaf', 'pin', 1, '{}', 'Only when something is built.')`,
		`INSERT INTO procedure_versions (procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at)
		 VALUES ('root', 1, 'p', 'm', '{}', '{}', 'r', '` + at + `')`,
		`INSERT INTO bindings (id, repository, name, procedure_id, created_at) VALUES ('bind', 'github.com/o/a', 'verify', 'root', '` + at + `')`,
		`INSERT INTO binding_revisions (binding_id, revision, inputs, policy, pinned_version, revision_reason, created_at)
		 VALUES ('bind', 1, '{"cmd":"make"}', 'pin', 1, 'r', '` + at + `')`,
		`INSERT INTO executions (id, procedure_id, version, repository, commit_hash, environment_name, environment_attributes, inputs, outcome, evidence, created_at)
		 VALUES ('child', 'leaf', 1, 'github.com/o/a', '` + commit + `', 'ci', '{}', '{}', 'failed', '{"exit":1}', '2026-10-10T12:00:01.000000000Z')`,
		`INSERT INTO execution_decisions (parent_execution_id, parent_procedure_id, parent_version, parent_repository, parent_commit_hash, position, reference, applicable, rationale)
		 VALUES ('parent', 'root', 1, 'github.com/o/a', '` + commit + `', 0, 'build', 0, 'Nothing to build.')`,
		`INSERT INTO executions (id, procedure_id, version, repository, commit_hash, environment_name, environment_attributes, inputs, outcome, evidence, created_at)
		 VALUES ('parent', 'root', 1, 'github.com/o/a', '` + commit + `', 'ci', '{}', '{"m":1}', 'succeeded', '{"run":1}', '2026-10-10T12:00:02.000000000Z')`,
		`INSERT INTO feedback (id, kind, summary, details, reporter, context, created_at, subject_type, subject_id, subject_version, repository, execution_id)
		 VALUES ('fb', 'problem', 's', 'd', 'test.agent', '{}', '2026-10-10T12:00:03.000000000Z', 'execution', 'parent', NULL, 'github.com/o/a', 'parent')`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	var before struct {
		root, leaf memory.History
		parent     memory.Execution
		v          memory.Verification
		binding    memory.BindingHistory
		fb         memory.Feedback
	}
	read := func(s *sqlite.Store) (out struct {
		root, leaf memory.History
		parent     memory.Execution
		v          memory.Verification
		binding    memory.BindingHistory
		fb         memory.Feedback
	}) {
		t.Helper()
		var err error
		for _, step := range []func() error{
			func() error { out.root, err = s.History(ctx, "root"); return err },
			func() error { out.leaf, err = s.History(ctx, "leaf"); return err },
			func() error { out.parent, err = s.Execution(ctx, "parent"); return err },
			func() error { out.v, err = s.ExecutionVerification(ctx, "parent"); return err },
			func() error { out.binding, err = s.BindingHistory(ctx, "bind"); return err },
			func() error { out.fb, err = s.Feedback(ctx, "fb"); return err },
		} {
			if err := step(); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	s := openStore(t, path)
	before = read(s)
	if c := before.root.Versions[0].References[0].Condition; c == nil || *c != "Only when something is built." ||
		len(before.parent.Decisions) != 1 || !before.v.Verified || before.binding.Revisions[0].VersionPolicy.Pin != 1 ||
		before.fb.Subject.ExecutionID != "parent" || before.leaf.Procedure.Origin.RepositoryID != "repo-a" {
		t.Fatalf("schema-8 records after the upgrade: %+v", before)
	}
	if items, err := s.ListToilItems(ctx, memory.ToilFilter{}); err != nil || len(items) != 0 {
		t.Fatalf("toil items after the upgrade = %v, %v", items, err)
	}
	first := toilRevision("toil-1", 1, memory.ToilObserved, memory.ToilSource{ProcedureID: "leaf", Version: 1, Steps: []string{"build"}})
	first.Observations = []memory.ToilEvidence{{ExecutionID: "child", Note: "a failed build"}}
	mustCreateToil(t, s, "toil-1", "toil.build", first)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, path)
	if after := read(s); !reflect.DeepEqual(after, before) {
		t.Fatalf("records changed after a toil item linked them:\n%+v\n%+v", after, before)
	}
	h, err := s.ToilHistory(ctx, "toil-1")
	if err != nil || len(h.Revisions) != 1 || !reflect.DeepEqual(h.Revisions[0], first) {
		t.Fatalf("ToilHistory after reopening = %+v, %v\nwant %+v", h, err, first)
	}
}

// TestSchemaEnforcesToilRules: the schema repeats ADR-0034's structural
// rules for clients that write SQL directly, and keeps history immutable.
func TestSchemaEnforcesToilRules(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	s := openStore(t, path)
	mustPut(t, s, "leaf", 1)
	mustPut(t, s, "leaf", 2)
	mustRecord(t, s, runOf("run", "leaf", 1, 1))
	src := memory.ToilSource{ProcedureID: "leaf", Version: 1}
	mustCreateToil(t, s, "toil-1", "toil.one", toilRevision("toil-1", 1, memory.ToilObserved, src))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()

	const proposal = `'{"inputs":1,"outputs":2,"remaining_judgment":3,"failure_and_recovery":4,"acceptance_criteria":["c"]}'`
	revision := func(n, status, proposal, extra string) string {
		cols, vals := "", ""
		if extra != "" {
			parts := strings.SplitN(extra, "=", 2)
			cols, vals = ", "+parts[0], ", "+parts[1]
		}
		return `INSERT INTO toil_revisions (toil_item_id, revision, activity, scope, assessment, proposal, status, status_rationale, revision_reason, author, created_at` + cols + `)
			VALUES ('toil-1', ` + n + `, 'a', 'shared', '{"repetition":{"status":"observed"},"burden":{"status":"unknown"}}', ` + proposal + `, '` + status + `', 'r', 'r', 'test.agent', '2030-01-01T00:00:00.000000000Z'` + vals + `)`
	}
	source := func(n string) string {
		return `INSERT INTO toil_revision_sources (toil_item_id, revision, position, procedure_id, version) VALUES ('toil-1', ` + n + `, 0, 'leaf', 1)`
	}
	validation := func(n string) string {
		return `INSERT INTO toil_revision_executions (toil_item_id, revision, role, position, execution_id, note) VALUES ('toil-1', ` + n + `, 'validation', 0, 'run', 'n')`
	}
	for name, statements := range map[string][]string{
		"update an item":                    {`UPDATE toil_items SET canonical_key = 'toil.two'`},
		"delete an item":                    {`DELETE FROM toil_items`},
		"update a revision":                 {`UPDATE toil_revisions SET status_rationale = 'tampered'`},
		"delete a revision":                 {`DELETE FROM toil_revisions`},
		"update a source":                   {`UPDATE toil_revision_sources SET version = 1`},
		"delete a source":                   {`DELETE FROM toil_revision_sources`},
		"add a source to a stored one":      {`INSERT INTO toil_revision_sources (toil_item_id, revision, position, procedure_id, version) VALUES ('toil-1', 1, 1, 'leaf', 2)`},
		"add a citation to a stored one":    {`INSERT INTO toil_revision_executions (toil_item_id, revision, role, position, execution_id, note) VALUES ('toil-1', 1, 'observation', 0, 'run', 'n')`},
		"skip a revision number":            {source("3"), revision("3", "observed", "NULL", "")},
		"a revision without a source":       {revision("2", "observed", "NULL", "")},
		"observed to implemented":           {source("2"), revision("2", "implemented", proposal, `replacement_summary, replacement_references='s', '[{"uri":"u"}]'`)},
		"candidate without a proposal":      {source("2"), revision("2", "candidate", "NULL", "")},
		"a source of a missing version":     {`INSERT INTO toil_revision_sources (toil_item_id, revision, position, procedure_id, version) VALUES ('toil-1', 2, 0, 'leaf', 3)`, revision("2", "observed", "NULL", "")},
		"a citation of a missing run":       {source("2"), `INSERT INTO toil_revision_executions (toil_item_id, revision, role, position, execution_id, note) VALUES ('toil-1', 2, 'observation', 0, 'missing', 'n')`, revision("2", "observed", "NULL", "")},
		"validation evidence, no rationale": {source("2"), validation("2"), revision("2", "retired", "NULL", "")},
		"a link without its revision":       {source("2")},
		"an empty acceptance list":          {source("2"), revision("2", "candidate", `'{"acceptance_criteria":[]}'`, "")},
		"no burden claim":                   {source("2"), strings.Replace(revision("2", "observed", "NULL", ""), `,"burden":{"status":"unknown"}`, "", 1)},
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := raw.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			var failed error
			for _, stmt := range statements {
				if _, failed = tx.ExecContext(ctx, stmt); failed != nil {
					break
				}
			}
			if failed == nil {
				failed = tx.Commit()
			}
			if failed == nil {
				t.Fatal("accepted")
			}
		})
	}
	// Well-formed revisions of the same kinds are accepted, so the
	// rejections above are for their stated reasons.
	for _, statements := range [][]string{
		{source("2"), revision("2", "candidate", proposal, "")},
		{source("3"), revision("3", "implemented", proposal, `replacement_summary, replacement_references='s', '[{"uri":"u"}]'`)},
		{source("4"), validation("4"), revision("4", "validated", proposal, `replacement_summary, replacement_references, validation_rationale='s', '[{"uri":"u"}]', 'checked'`)},
		{source("5"), revision("5", "candidate", proposal, "")},
	} {
		tx, err := raw.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range statements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestConcurrentToilRevisionsConflict: of concurrent appends from one base,
// exactly one is stored; the others get a *RevisionConflictError naming
// the stored revision.
func TestConcurrentToilRevisionsConflict(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	mustPut(t, s, "leaf", 1)
	src := memory.ToilSource{ProcedureID: "leaf", Version: 1}
	mustCreateToil(t, s, "toil-1", "toil.one", toilRevision("toil-1", 1, memory.ToilObserved, src))
	const writers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, writers)
	for i := range writers {
		wg.Go(func() {
			<-start
			errs[i] = s.AppendToilRevision(ctx, 1, toilRevision("toil-1", 2, memory.ToilCandidate, src))
		})
	}
	close(start)
	wg.Wait()
	stored := 0
	for _, err := range errs {
		var conflict *memory.RevisionConflictError
		switch {
		case err == nil:
			stored++
		case errors.As(err, &conflict) && conflict.LatestRevision == 2:
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	h, err := s.ToilHistory(ctx, "toil-1")
	if stored != 1 || err != nil || len(h.Revisions) != 2 {
		t.Fatalf("stored %d, history %+v, %v", stored, h, err)
	}
	if err := s.AppendToilRevision(ctx, 1, toilRevision("missing", 2, memory.ToilObserved, src)); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("append to a missing item: %v", err)
	}
	err = s.CreateToilItem(ctx, memory.ToilItem{ID: "toil-2", CanonicalKey: "toil.one"}, toilRevision("toil-2", 1, memory.ToilObserved, src))
	if !errors.Is(err, memory.ErrCanonicalKeyExists) {
		t.Fatalf("a duplicate key: %v", err)
	}
}

// shaped returns a revision with what status requires (ADR-0034).
func shaped(item string, n int, status memory.ToilStatus, src memory.ToilSource) memory.ToilRevision {
	r := toilRevision(item, n, status, src)
	if status == memory.ToilImplemented || status == memory.ToilValidated {
		r.Replacement = &memory.ToilReplacement{Summary: "s", References: []memory.ExternalReference{{URI: "u"}}}
	}
	if status == memory.ToilValidated {
		r.Validation = &memory.ToilValidation{Rationale: "checked", References: []memory.ExternalReference{{URI: "u"}}}
	}
	return r
}

// TestSchemaTransitionsMatchTheDomain: the schema's lifecycle trigger and
// memory.ToilTransitionAllowed agree on every pair of statuses.
func TestSchemaTransitionsMatchTheDomain(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	mustPut(t, s, "leaf", 1)
	src := memory.ToilSource{ProcedureID: "leaf", Version: 1}
	paths := map[memory.ToilStatus][]memory.ToilStatus{
		memory.ToilObserved:    {memory.ToilObserved},
		memory.ToilCandidate:   {memory.ToilCandidate},
		memory.ToilImplemented: {memory.ToilCandidate, memory.ToilImplemented},
		memory.ToilValidated:   {memory.ToilCandidate, memory.ToilImplemented, memory.ToilValidated},
		memory.ToilRetired:     {memory.ToilObserved, memory.ToilRetired},
	}
	allowed := 0
	for _, from := range memory.ToilStatuses {
		for _, to := range memory.ToilStatuses {
			item := string(from) + "-" + string(to)
			for i, st := range paths[from] {
				r := shaped(item, i+1, st, src)
				var err error
				if i == 0 {
					err = s.CreateToilItem(ctx, memory.ToilItem{ID: item, CanonicalKey: "toil." + item}, r)
				} else {
					err = s.AppendToilRevision(ctx, i, r)
				}
				if err != nil {
					t.Fatalf("reaching %s: %v", from, err)
				}
			}
			n := len(paths[from])
			err := s.AppendToilRevision(ctx, n, shaped(item, n+1, to, src))
			if want := memory.ToilTransitionAllowed(from, to); (err == nil) != want {
				t.Errorf("%s -> %s: schema error %v, domain allows %v", from, to, err, want)
			}
			if err == nil {
				allowed++
			}
		}
	}
	if allowed != 16 {
		t.Errorf("%d allowed transitions, want 16", allowed)
	}
}
