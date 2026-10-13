// Package knowledgetest holds the behaviour every knowledge Store must give
// the kernel, as one scenario run against an in-memory store in the domain
// tests and against Git-core in the integration tests.
package knowledgetest

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
)

// Proc builds a procedure with the given references (name=key pairs share
// one input, "module").
func Proc(key, goal string, refs ...string) *knowledge.Procedure {
	p := &knowledge.Procedure{
		Key: key, Goal: goal, Philosophy: "Small, verified steps.", Method: "Run each step in order; stop at the first failure.",
		Applicability: knowledge.Applicability{Scope: knowledge.ScopeShared},
		Contract:      jsontext.Value(`{"inputs":{"module":"path"},"expected_outcome":"` + goal + `"}`),
		Instructions:  jsontext.Value(`{"steps":[{"id":"run","action":"` + goal + `"}]}`),
	}
	for _, r := range refs {
		name, k, _ := strings.Cut(r, "=")
		p.References = append(p.References, knowledge.Reference{Name: name, Key: k, Inputs: jsontext.Value(`{"module":{"input":"module"}}`)})
	}
	return p
}

// Three related procedures: verify composes build and checks.
func Initial() []knowledge.Change {
	return []knowledge.Change{
		{Key: "go.module.build", Procedure: Proc("go.module.build", "Build the Go module")},
		{Key: "go.module.checks", Procedure: Proc("go.module.checks", "Run gofmt and go vet on the module")},
		{Key: "go.module.verify", Procedure: Proc("go.module.verify", "Verify a change to a Go module", "build=go.module.build", "checks=go.module.checks")},
	}
}

func prov(summary string) knowledge.Provenance {
	return knowledge.Provenance{Author: "copilot.agent", Summary: summary, Rationale: "Because " + summary + "."}
}

// Describe renders a resolution as key[name:child ...], with each node's
// snapshot checked against the root's.
func Describe(t *testing.T, n *knowledge.Node) string {
	t.Helper()
	var walk func(n *knowledge.Node) string
	walk = func(c *knowledge.Node) string {
		if c.Snapshot != n.Snapshot {
			t.Fatalf("%s resolved at %s inside a resolution at %s", c.Procedure.Key, c.Snapshot, n.Snapshot)
		}
		s := c.Procedure.Key + "(" + c.Procedure.Goal + ")"
		var parts []string
		for _, e := range c.References {
			parts = append(parts, e.Reference.Name+":"+walk(e.Node))
		}
		if len(parts) > 0 {
			s += "[" + strings.Join(parts, " ") + "]"
		}
		return s
	}
	return walk(n)
}

// Fingerprint is every key with its canonical document, the identity of a
// catalog's content.
func Fingerprint(t *testing.T, c *knowledge.Catalog) string {
	t.Helper()
	var b strings.Builder
	for _, k := range c.Keys() {
		p, err := c.Get(k)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := knowledge.Encode(p)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(k + "=" + string(doc))
	}
	return b.String()
}

// Run is the shared scenario. open opens the store of dir; called again on
// the same dir it is a fresh process's view.
func Run(t *testing.T, open func(t *testing.T, dir string) knowledge.Store) {
	ctx := context.Background()
	dir := t.TempDir()
	k := knowledge.New(open(t, dir))

	empty, err := k.Active(ctx)
	if err != nil || empty.Snapshot() != "" || empty.Len() != 0 {
		t.Fatalf("empty catalog: %v", err)
	}
	// 1. Publish three related procedures.
	first := prov("Initial Go module procedures")
	first.Finding = "feedback:01a1-initial"
	s1, err := k.Publish(ctx, knowledge.Proposal{Changes: Initial(), Provenance: first})
	if err != nil {
		t.Fatal(err)
	}
	// 2. Discover and read the active definitions.
	cur, err := k.Active(ctx)
	if err != nil || cur.Snapshot() != s1 || !slices.Equal(cur.Keys(), []string{"go.module.build", "go.module.checks", "go.module.verify"}) {
		t.Fatalf("active catalog: %v", err)
	}
	if found := cur.Discover("verify a change to my go module", "", 5); len(found) != 3 || found[0].Key != "go.module.verify" {
		t.Fatalf("discover = %+v", found)
	}
	got, err := cur.Get("go.module.checks")
	if err != nil || string(got.Contract) != `{"inputs":{"module":"path"},"expected_outcome":"Run gofmt and go vet on the module"}` ||
		got.Applicability.Scope != knowledge.ScopeShared {
		t.Fatalf("read definition: %+v, %v", got, err)
	}
	root, err := cur.Resolve("go.module.verify", "github.com/example/service")
	if err != nil {
		t.Fatal(err)
	}
	original := Describe(t, root)
	if original != "go.module.verify(Verify a change to a Go module)[build:go.module.build(Build the Go module) checks:go.module.checks(Run gofmt and go vet on the module)]" {
		t.Fatalf("resolution = %s", original)
	}
	// 3. Improve a referenced child.
	better := Proc("go.module.checks", "Run gofmt, go vet and staticcheck on the module")
	improve := prov("checks: add staticcheck")
	improve.Finding, improve.Validation = "feedback:01a2-missed-lint", "gate passed at 1fb9ef2"
	s2, err := k.Publish(ctx, knowledge.Proposal{Base: s1, Changes: []knowledge.Change{{Key: better.Key, Procedure: better}}, Provenance: improve})
	if err != nil {
		t.Fatal(err)
	}
	// 4. New reads see the improvement, in a resolution of one snapshot.
	cur, _ = k.Active(ctx)
	root2, err := cur.Resolve("go.module.verify", "")
	if err != nil || root2.Snapshot != s2 || !strings.Contains(Describe(t, root2), "checks:go.module.checks(Run gofmt, go vet and staticcheck on the module)") {
		t.Fatalf("after the improvement: %v", err)
	}
	// 5. The earlier snapshot still returns the original complete composition.
	old, err := k.At(ctx, s1)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := old.Resolve("go.module.verify", ""); err != nil || Describe(t, again) != original || again.Snapshot != s1 {
		t.Fatalf("snapshot %s no longer resolves as before: %v", s1, err)
	}
	s1Print := Fingerprint(t, old)

	// 6. Broken references, cycles, invalid contracts and stale updates.
	cycle := Proc("go.module.build", "Build", "back=go.module.verify")
	noContract := Proc("go.module.build", "Build")
	noContract.Contract = jsontext.Value(`["not an object"]`)
	local := Proc("go.module.local", "Only in one repository")
	local.Applicability = knowledge.Applicability{Scope: knowledge.ScopeLocal, Repository: "github.com/example/one"}
	for name, changes := range map[string][]knowledge.Change{
		"broken reference": {{Key: "go.module.verify", Procedure: Proc("go.module.verify", "Verify", "build=go.module.gone")}},
		"cycle":            {{Key: "go.module.build", Procedure: cycle}},
		"invalid contract": {{Key: "go.module.build", Procedure: noContract}},
		"removed child":    {{Key: "go.module.build"}},
		"shared uses local": {{Key: local.Key, Procedure: local},
			{Key: "go.module.verify", Procedure: Proc("go.module.verify", "Verify", "build=go.module.build", "checks=go.module.checks", "local=go.module.local")}},
		"key mismatch": {{Key: "go.module.other", Procedure: Proc("go.module.build", "Build")}},
		"unchanged":    {{Key: "go.module.build", Procedure: Proc("go.module.build", "Build the Go module")}},
	} {
		if _, err := k.Publish(ctx, knowledge.Proposal{Base: s2, Changes: changes, Provenance: prov(name)}); !errors.Is(err, knowledge.ErrInvalid) {
			t.Fatalf("%s: published or wrong error: %v", name, err)
		} else {
			t.Logf("%s: %v", name, err)
		}
	}
	if _, err := k.Publish(ctx, knowledge.Proposal{Base: s2, Changes: Initial()[:1], Provenance: knowledge.Provenance{Author: "a"}}); !errors.Is(err, knowledge.ErrInvalid) {
		t.Fatalf("publication without rationale: %v", err)
	}
	stale := Proc("go.module.build", "Build, stale edit")
	if _, err := k.Publish(ctx, knowledge.Proposal{Base: s1, Changes: []knowledge.Change{{Key: stale.Key, Procedure: stale}}, Provenance: prov("stale")}); !errors.Is(err, knowledge.ErrStale) {
		t.Fatalf("stale publication: %v", err)
	}
	if a, _ := k.Active(ctx); a.Snapshot() != s2 {
		t.Fatal("a refused publication moved the active knowledge")
	}
	// Racing publishers from one base: exactly one wins, through two kernels.
	k2 := knowledge.New(open(t, dir))
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			kk := k
			if i%2 == 1 {
				kk = k2
			}
			p := Proc("go.module.build", fmt.Sprintf("Build the Go module, variant %d", i))
			_, err := kk.Publish(ctx, knowledge.Proposal{Base: s2, Changes: []knowledge.Change{{Key: p.Key, Procedure: p}}, Provenance: prov("race")})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	won := 0
	for err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, knowledge.ErrStale):
			t.Fatalf("racing publisher: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d racing publishers won", won)
	}
	cur, _ = k.Active(ctx)
	s3 := cur.Snapshot()

	// 7. Roll back to the first snapshot by a new publication.
	back := prov("Roll back to the initial procedures")
	s4, err := k.Rollback(ctx, s3, s1, back)
	if err != nil {
		t.Fatal(err)
	}
	cur, _ = k.Active(ctx)
	if s4 == s1 || cur.Snapshot() != s4 || Fingerprint(t, cur) != s1Print {
		t.Fatalf("rollback: %s, active %s", s4, cur.Snapshot())
	}
	if _, err := k.Rollback(ctx, s4, s1, back); !errors.Is(err, knowledge.ErrInvalid) {
		t.Fatalf("rollback to equal knowledge: %v", err)
	}
	if mid, err := k.At(ctx, s2); err != nil || !strings.Contains(Fingerprint(t, mid), "staticcheck") {
		t.Fatalf("the rolled-back snapshot is gone: %v", err)
	}

	// 8. Authorship and rationale are part of the history.
	h, err := k.History(ctx, s4, 10)
	if err != nil || len(h) != 4 {
		t.Fatalf("history: %d, %v", len(h), err)
	}
	if h[0].RollbackTo != s1 || h[0].Provenance.Summary != back.Summary || h[0].Provenance.Rationale != back.Rationale ||
		!slices.Equal(h[0].Changed, []knowledge.KeyChange{{Key: "go.module.build", Action: "modified"}, {Key: "go.module.checks", Action: "modified"}}) {
		t.Fatalf("rollback entry = %+v", h[0])
	}
	if e := h[2]; e.Snapshot != s2 || e.Provenance != improve || !strings.HasPrefix(e.Validated, "catalog of 3 procedures") ||
		!slices.Equal(e.Changed, []knowledge.KeyChange{{Key: "go.module.checks", Action: "modified"}}) {
		t.Fatalf("improvement entry = %+v", e)
	}
	if e := h[3]; e.Provenance != first || len(e.Changed) != 3 || e.Changed[0].Action != "added" {
		t.Fatalf("first entry = %+v", e)
	}

	// 9. A fresh kernel on the same storage reconstructs everything.
	fresh := knowledge.New(open(t, dir))
	cur, err = fresh.Active(ctx)
	if err != nil || cur.Snapshot() != s4 || Fingerprint(t, cur) != s1Print {
		t.Fatalf("fresh kernel: %v", err)
	}
	if old, err := fresh.At(ctx, s1); err != nil || Fingerprint(t, old) != s1Print {
		t.Fatalf("fresh kernel at %s: %v", s1, err)
	}
	if _, err := fresh.At(ctx, "no-such-snapshot"); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("unknown snapshot: %v", err)
	}
}
