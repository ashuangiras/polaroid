package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

// latestSchema is the number of migrations this build applies.
const latestSchema = 8

func conditionalOf(r memory.Reference, condition string) memory.Reference {
	r.Condition = &condition
	return r
}

func decision(reference string, applicable bool, rationale string) memory.ApplicabilityDecision {
	return memory.ApplicabilityDecision{Reference: reference, Applicable: &applicable, Rationale: rationale}
}

// seedConditional stores leaf "c" and "par"@1, which requires c as "checks"
// and runs it as "extra" only when its condition holds.
func seedConditional(t *testing.T, s *sqlite.Store) {
	t.Helper()
	mustPut(t, s, "c", 1)
	mustPut(t, s, "par", 1, pinTo("checks", "c", 1), conditionalOf(latestOf("extra", "c"), "Only when the schema changes."))
}

func TestConditionalReferencesAndDecisionsPersist(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	s := openStore(t, path)
	seedConditional(t, s)
	mustRecord(t, s, runOf("checks-1", "c", 1, 1))
	skip := decision("extra", false, "No migration file changed.")
	skip.Evidence = jsontext.Value(`{"changed":["docs/a.md"]}`)
	docs := runOf("docs", "par", 1, 2, memory.ChildExecution{Reference: "checks", ExecutionID: "checks-1"})
	docs.Decisions = []memory.ApplicabilityDecision{skip}
	mustRecord(t, s, docs)
	mustRecord(t, s, runOf("checks-2", "c", 1, 3))
	mustRecord(t, s, runOf("extra-2", "c", 1, 4))
	code := runOf("code", "par", 1, 5, memory.ChildExecution{Reference: "checks", ExecutionID: "checks-2"}, memory.ChildExecution{Reference: "extra", ExecutionID: "extra-2"})
	code.Decisions = []memory.ApplicabilityDecision{decision("extra", true, "0009_x.sql added.")}
	mustRecord(t, s, code)

	check := func(s *sqlite.Store) {
		t.Helper()
		for _, want := range []memory.Execution{docs, code} {
			if got, err := s.Execution(ctx, want.ID); err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("Execution(%s) = %+v, %v\nwant %+v", want.ID, got, err, want)
			}
		}
		h, err := s.History(ctx, "par")
		if err != nil || h.Versions[0].References[0].Condition != nil || *h.Versions[0].References[1].Condition != "Only when the schema changes." {
			t.Fatalf("History(par) references = %+v, %v", h.Versions[0].References, err)
		}
		v, err := s.ExecutionVerification(ctx, "docs")
		if err != nil || !v.Verified || !reflect.DeepEqual(v.Combination.Children, []memory.ChildVersion{{Reference: "checks", Version: 1}, {Reference: "extra", Skipped: true}}) {
			t.Fatalf("docs verification = %+v, %v", v, err)
		}
		statuses, err := s.Verifications(ctx, memory.VerificationFilter{ProcedureID: "par", Version: 1})
		if err != nil || len(statuses) != 2 || !statuses[0].Verified || !statuses[1].Verified {
			t.Fatalf("an executed and a skipped run must be two verified combinations: %+v, %v", statuses, err)
		}
		g, err := compositionGraph(s, ctx, "par", 1)
		if err != nil || len(g.Edges) != 2 || g.Edges[1].Reference.Condition == nil {
			t.Fatalf("graph = %+v, %v", g, err)
		}
	}
	check(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	check(openStore(t, path))
}

// The schema repeats the structural rules for clients that write SQL
// directly (ADR-0029).
func TestSchemaEnforcesDecisionRules(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	s := openStore(t, path)
	seedConditional(t, s)
	mustRecord(t, s, runOf("checks-1", "c", 1, 1))
	mustRecord(t, s, runOf("extra-1", "c", 1, 2))
	docs := runOf("docs", "par", 1, 3, memory.ChildExecution{Reference: "checks", ExecutionID: "checks-1"})
	docs.Decisions = []memory.ApplicabilityDecision{decision("extra", false, "no schema change")}
	mustRecord(t, s, docs)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()

	parent := func(id string) string {
		return `INSERT INTO executions (id, procedure_id, version, repository, commit_hash, environment_name, environment_attributes, inputs, outcome, evidence, created_at)
			VALUES ('` + id + `', 'par', 1, 'github.com/ashuangiras/polaroid', '` + commitA + `', 'ci', '{}', '{"module":"modernc.org/sqlite"}', 'succeeded', '{"x":1}', '2030-01-01T00:00:00.000000000Z')`
	}
	decisionRow := func(id, reference string, applicable int, rationale string) string {
		return `INSERT INTO execution_decisions (parent_execution_id, parent_procedure_id, parent_version, parent_repository, parent_commit_hash, position, reference, applicable, rationale)
			VALUES ('` + id + `', 'par', 1, 'github.com/ashuangiras/polaroid', '` + commitA + `', 0, '` + reference + `', ` + string(rune('0'+applicable)) + `, '` + rationale + `')`
	}
	childRow := func(id, reference, child string) string {
		return `INSERT INTO execution_children (parent_execution_id, parent_procedure_id, parent_version, parent_repository, parent_commit_hash, position, reference, child_execution_id)
			VALUES ('` + id + `', 'par', 1, 'github.com/ashuangiras/polaroid', '` + commitA + `', 1, '` + reference + `', '` + child + `')`
	}
	for name, statements := range map[string][]string{
		"update a decision":               {`UPDATE execution_decisions SET applicable = 1`},
		"delete a decision":               {`DELETE FROM execution_decisions`},
		"add a decision to a stored run":  {decisionRow("docs", "extra", 1, "later")},
		"decide a required reference":     {decisionRow("n1", "checks", 0, "skip it"), parent("n1")},
		"decide an unknown reference":     {decisionRow("n2", "lint", 0, "skip it"), parent("n2")},
		"blank rationale":                 {decisionRow("n3", "extra", 0, "   "), parent("n3")},
		"not applicable, then a child":    {decisionRow("n4", "extra", 0, "skip"), childRow("n4", "extra", "extra-1"), parent("n4")},
		"a child, then not applicable":    {childRow("n5", "extra", "extra-1"), decisionRow("n5", "extra", 0, "skip"), parent("n5")},
		"two decisions for one reference": {decisionRow("n6", "extra", 1, "a"), strings.Replace(decisionRow("n6", "extra", 0, "b"), ", 0, 'extra'", ", 1, 'extra'", 1), parent("n6")},
		"a decision without its parent":   {decisionRow("n7", "extra", 0, "skip")},
		"change a reference's condition":  {`UPDATE procedure_version_references SET condition = NULL WHERE name = 'extra'`},
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
	var n int
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM execution_decisions`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("decisions = %d, %v; want only the recorded one", n, err)
	}
	// The same statements succeed for a well-formed applicable decision, so
	// the rejections above are for their stated reasons.
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{decisionRow("ok", "extra", 1, "schema changed"), childRow("ok", "extra", "extra-1"), parent("ok")} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestOpenMigratesSchemaVersion7 shows that references and executions stored
// before migration 8 read back unchanged: every reference stays required,
// executions have no decisions, and verification is unchanged.
func TestOpenMigratesSchemaVersion7(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	raw, err := sql.Open("sqlite", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	for i, file := range []string{"0001_procedures", "0002_bindings", "0003_procedure_references", "0004_executions", "0005_execution_children", "0006_feedback", "0007_repositories_scope_feedback"} {
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
		`PRAGMA user_version = 7`,
		`INSERT INTO procedures (id, canonical_key, created_at) VALUES ('leaf', 'go.module.build', '` + at + `')`,
		`INSERT INTO procedure_versions (procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at)
		 VALUES ('leaf', 1, 'p', 'm', '{}', '{}', 'r', '` + at + `')`,
		`INSERT INTO procedures (id, canonical_key, created_at) VALUES ('root', 'dev.change.verify', '` + at + `')`,
		`INSERT INTO procedure_version_references (procedure_id, version, position, name, target_procedure_id, policy, pinned_version, inputs)
		 VALUES ('root', 1, 0, 'build', 'leaf', 'pin', 1, '{}')`,
		`INSERT INTO procedure_versions (procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at)
		 VALUES ('root', 1, 'p', 'm', '{}', '{}', 'r', '` + at + `')`,
		`INSERT INTO executions (id, procedure_id, version, repository, commit_hash, environment_name, environment_attributes, inputs, outcome, evidence, created_at)
		 VALUES ('child', 'leaf', 1, 'github.com/o/a', '` + commit + `', 'ci', '{}', '{}', 'succeeded', '{"exit":0}', '2026-10-10T12:00:01.000000000Z')`,
		`INSERT INTO execution_children (parent_execution_id, parent_procedure_id, parent_version, parent_repository, parent_commit_hash, position, reference, child_execution_id)
		 VALUES ('parent', 'root', 1, 'github.com/o/a', '` + commit + `', 0, 'build', 'child')`,
		`INSERT INTO executions (id, procedure_id, version, repository, commit_hash, environment_name, environment_attributes, inputs, outcome, evidence, created_at)
		 VALUES ('parent', 'root', 1, 'github.com/o/a', '` + commit + `', 'ci', '{}', '{"m":1}', 'succeeded', '{"run":1}', '2026-10-10T12:00:02.000000000Z')`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	check := func(s *sqlite.Store) {
		t.Helper()
		h, err := s.History(ctx, "root")
		if err != nil || len(h.Versions[0].References) != 1 || h.Versions[0].References[0].Condition != nil || h.Versions[0].References[0].Conditional() {
			t.Fatalf("History(root) = %+v, %v; want the reference required", h, err)
		}
		e, err := s.Execution(ctx, "parent")
		if err != nil || e.Decisions != nil || len(e.Children) != 1 || string(e.Evidence) != `{"run":1}` {
			t.Fatalf("Execution(parent) = %+v, %v", e, err)
		}
		v, err := s.ExecutionVerification(ctx, "parent")
		if err != nil || !v.Verified || !reflect.DeepEqual(v.Combination.Children, []memory.ChildVersion{{Reference: "build", Version: 1}}) {
			t.Fatalf("verification = %+v, %v", v, err)
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
	if err := raw.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil || schema != latestSchema {
		t.Fatalf("schema version = %d, %v; want %d", schema, err, latestSchema)
	}
}
