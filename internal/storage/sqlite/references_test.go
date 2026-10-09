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
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
)

func reference(name, target string, policy memory.VersionPolicy, inputs string) memory.Reference {
	return memory.Reference{Name: name, ProcedureID: target, VersionPolicy: policy, Inputs: jsontext.Value(inputs)}
}

// seedParent creates procedure "parent" whose version 1 references "p1"
// (which must have at least two versions) twice.
func seedParent(t *testing.T, s interface {
	CreateProcedure(context.Context, memory.Procedure, memory.Version) error
}) memory.History {
	t.Helper()
	v1 := version("parent", 1, `{"steps":["compose"]}`)
	v1.References = []memory.Reference{
		reference("pinned-child", "p1", pin2, `{"module":{"input":"driver"},"strict":{"value":true}}`),
		reference("latest-child", "p1", contextual, `{}`),
	}
	h := memory.History{Procedure: procedure("parent", "compose.parent"), Versions: []memory.Version{v1}}
	if err := s.CreateProcedure(context.Background(), h.Procedure, v1); err != nil {
		t.Fatalf("CreateProcedure(parent): %v", err)
	}
	return h
}

func TestVersionsStoreAndReturnReferencesInOrder(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	child := seed(t, s, 2)
	want := seedParent(t, s)

	v2 := version("parent", 2, `{"steps":["compose","verify"]}`)
	v2.References = []memory.Reference{reference("only-child", "p1", memory.VersionPolicy{Kind: memory.PolicyPin, Pin: 1}, `{"x":{"value":[1,"two"]}}`)}
	if err := s.AppendVersion(ctx, 1, v2); err != nil {
		t.Fatalf("AppendVersion with references: %v", err)
	}
	v3 := version("parent", 3, `{"steps":["no children"]}`)
	if err := s.AppendVersion(ctx, 2, v3); err != nil {
		t.Fatalf("AppendVersion without references: %v", err)
	}
	want.Versions = append(want.Versions, v2, v3)
	want.Procedure.LatestVersion = 3

	if got := history(t, s, "parent"); !reflect.DeepEqual(got, want) {
		t.Fatalf("History =\n %+v\nwant\n %+v", got, want)
	}
	for _, v := range want.Versions {
		got, err := s.Version(ctx, "parent", v.Number)
		if err != nil || !reflect.DeepEqual(got, v) {
			t.Fatalf("Version(%d) = %+v, %v\nwant %+v", v.Number, got, err, v)
		}
	}
	if got := history(t, s, "p1"); !reflect.DeepEqual(got, child) {
		t.Fatalf("referencing a procedure changed it:\n got %+v\nwant %+v", got, child)
	}
}

func TestReferencesToMissingTargetsAreRejected(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	seed(t, s, 2)

	v1 := version("parent", 1, `{}`)
	v1.References = []memory.Reference{
		reference("ok", "p1", pin2, `{}`),
		reference("no-procedure", "missing", contextual, `{}`),
		reference("no-version", "p1", memory.VersionPolicy{Kind: memory.PolicyPin, Pin: 3}, `{}`),
	}
	err := s.CreateProcedure(ctx, procedure("parent", "compose.parent"), v1)
	var missing *memory.MissingTargetsError
	if !errors.As(err, &missing) || !reflect.DeepEqual(missing.Missing, []memory.MissingTarget{{Index: 1, Procedure: true}, {Index: 2}}) {
		t.Fatalf("CreateProcedure = %v, want missing targets 1 (procedure) and 2 (version)", err)
	}
	if _, err := s.History(ctx, "parent"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("a rejected procedure was partially stored: %v", err)
	}

	want := seedParent(t, s)
	v2 := version("parent", 2, `{}`)
	v2.References = []memory.Reference{reference("self-future", "parent", memory.VersionPolicy{Kind: memory.PolicyPin, Pin: 2}, `{}`)}
	if err := s.AppendVersion(ctx, 1, v2); !errors.As(err, &missing) || !reflect.DeepEqual(missing.Missing, []memory.MissingTarget{{Index: 0}}) {
		t.Fatalf("AppendVersion pinning a version that does not exist yet = %v", err)
	}
	if got := history(t, s, "parent"); !reflect.DeepEqual(got, want) {
		t.Fatalf("history changed by a rejected revision: %+v", got)
	}
}

func TestReopenPreservesReferences(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	seed(t, s, 2)
	want := seedParent(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := history(t, openStore(t, path), "parent"); !reflect.DeepEqual(got, want) {
		t.Fatalf("after reopen:\n got %+v\nwant %+v", got, want)
	}
}

// References belong to their version: nothing can add, change or remove one
// after the version is stored, even with raw SQL.
func TestSchemaRejectsChangesToReferences(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	seed(t, s, 2)
	want := seedParent(t, s)

	raw, err := sql.Open("sqlite", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()

	const insert = `INSERT INTO procedure_version_references
		(procedure_id, version, position, name, target_procedure_id, policy, pinned_version, inputs)
		VALUES ('%s', %d, %d, '%s', '%s', '%s', %s, '{}')`
	const insertVersion2 = `INSERT INTO procedure_versions
		(procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at)
		VALUES ('parent', 2, 'p', 'm', '{}', '{}', 'r', '2026-10-09T12:00:00.000000000Z')`
	// Each case runs in its own transaction. Unless noted, the transaction
	// also writes version 2, so only the rule under test can reject it.
	cases := []struct {
		name, stmt   string
		withoutVer2  bool
		wantAccepted bool
	}{
		{name: "control: a valid reference written with its version", stmt: fmt.Sprintf(insert, "parent", 2, 0, "ok", "p1", "pin", "1"), wantAccepted: true},
		{name: "update reference", stmt: `UPDATE procedure_version_references SET pinned_version = 1 WHERE name = 'pinned-child'`, withoutVer2: true},
		{name: "delete reference", stmt: `DELETE FROM procedure_version_references WHERE name = 'latest-child'`, withoutVer2: true},
		{name: "add to an existing version", stmt: fmt.Sprintf(insert, "parent", 1, 2, "late", "p1", "contextual", "NULL"), withoutVer2: true},
		{name: "reference whose version never arrives", stmt: fmt.Sprintf(insert, "parent", 2, 0, "orphan", "p1", "contextual", "NULL"), withoutVer2: true},
		{name: "pin a missing version", stmt: fmt.Sprintf(insert, "parent", 2, 0, "bad-pin", "p1", "pin", "9")},
		{name: "unknown target", stmt: fmt.Sprintf(insert, "parent", 2, 0, "nowhere", "missing", "contextual", "NULL")},
		{name: "contextual with a version", stmt: fmt.Sprintf(insert, "parent", 2, 0, "mixed", "p1", "contextual", "1")},
		{name: "invalid name", stmt: fmt.Sprintf(insert, "parent", 2, 0, "Bad Name", "p1", "contextual", "NULL")},
		{name: "non-object inputs", stmt: strings.Replace(fmt.Sprintf(insert, "parent", 2, 0, "list", "p1", "contextual", "NULL"), "'{}')", "'[]')", 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tx, err := raw.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			_, err = tx.ExecContext(ctx, tc.stmt)
			if err == nil && !tc.withoutVer2 {
				_, err = tx.ExecContext(ctx, insertVersion2)
			}
			if err == nil && tc.withoutVer2 {
				err = tx.Commit()
			}
			if accepted := err == nil; accepted != tc.wantAccepted {
				t.Fatalf("accepted = %v, want %v (err %v): %s", accepted, tc.wantAccepted, err, tc.stmt)
			}
		})
	}
	if got := history(t, s, "parent"); !reflect.DeepEqual(got, want) {
		t.Fatalf("history changed:\n got %+v\nwant %+v", got, want)
	}
}

// Versions stored before migration 3 read back exactly as before.
func TestOpenMigratesSchemaVersion2(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"migrations/0001_procedures.sql", "migrations/0002_bindings.sql"} {
		script, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, string(script)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	for _, stmt := range []string{
		`PRAGMA user_version = 2`,
		`INSERT INTO procedures (id, canonical_key, created_at) VALUES ('p1', 'go.dependency.add', '2026-10-09T12:00:00.123456789Z')`,
		`INSERT INTO procedure_versions (procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at)
		 VALUES ('p1', 1, 'p', 'm', '{"inputs":{}}', '{"steps":["one"]}', 'r', '2026-10-09T12:00:00.123456789Z')`,
	} {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	_ = raw.Close()

	s := openStore(t, path)
	got, err := s.Version(ctx, "p1", 1)
	want := memory.Version{ProcedureID: "p1", Number: 1, CreatedAt: created, Definition: memory.Definition{
		Philosophy: "p", Method: "m", Contract: jsontext.Value(`{"inputs":{}}`), Instructions: jsontext.Value(`{"steps":["one"]}`), RevisionReason: "r",
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Version after migration = %+v, %v\nwant %+v", got, err, want)
	}
}
