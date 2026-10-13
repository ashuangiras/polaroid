// Package storetest is the behaviour both knowledge stores must share.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ashuangiras/polaroid/experiments/gitknowledge/knowledge"
)

// Opener opens the store kept in dir, creating it when dir is empty.
type Opener func(t *testing.T, dir string) knowledge.Store

func Proc(key, goal string, inputs []string, refs ...knowledge.Reference) *knowledge.Procedure {
	return &knowledge.Procedure{
		Key: key, Goal: goal, Method: "Follow the instructions in order.", Inputs: inputs,
		Contract:     json.RawMessage(`{"expected_outcome":"` + goal + `","verification":"exit status 0"}`),
		Instructions: json.RawMessage(`{"steps":[{"id":"run","action":"` + goal + `"}]}`),
		References:   refs,
	}
}

func Ref(name, key string, inputs map[string]string) knowledge.Reference {
	return knowledge.Reference{Name: name, Key: key, Inputs: inputs}
}

const Guidance = "agent.guidance.shared"

var guide = Ref("guidance", Guidance, nil)

// Initial is the representative tree: verify composes build and checks,
// and every procedure references the shared guidance.
func Initial() []knowledge.Change {
	return []knowledge.Change{
		{Key: Guidance, Procedure: Proc(Guidance, "Rules every procedure follows", nil)},
		{Key: "go.module.build", Procedure: Proc("go.module.build", "Build the module", []string{"module"}, guide)},
		{Key: "go.module.checks", Procedure: Proc("go.module.checks", "Run gofmt and go vet", []string{"gate_commands", "module"}, guide)},
		{Key: "go.module.verify", Procedure: Proc("go.module.verify", "Verify a change", []string{"gate_commands", "module"},
			guide,
			Ref("build", "go.module.build", map[string]string{"module": "module"}),
			Ref("checks", "go.module.checks", map[string]string{"gate_commands": "gate_commands", "module": "module"}))},
	}
}

func note(msg string) knowledge.Note { return knowledge.Note{Author: "test.agent", Message: msg} }

// Run runs the shared behaviour against one adapter.
func Run(t *testing.T, open Opener) {
	t.Run("workflow", func(t *testing.T) { workflow(t, open) })
	t.Run("concurrent", func(t *testing.T) { concurrent(t, open, false) })
	t.Run("two handles", func(t *testing.T) { concurrent(t, open, true) })
	t.Run("reads during publish", func(t *testing.T) { readsDuringPublish(t, open) })
	t.Run("invalid", func(t *testing.T) { invalid(t, open) })
}

func workflow(t *testing.T, open Opener) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, dir)

	// 1. empty
	if h, err := s.Head(ctx); err != nil || h != "" {
		t.Fatalf("empty head = %q, %v", h, err)
	}
	if _, err := s.ReadActive(ctx, "go.module.verify"); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("read from empty store: %v", err)
	}
	// 2. publish initial tree
	s1, err := s.Publish(ctx, "", Initial(), note("initial"))
	if err != nil {
		t.Fatal(err)
	}
	// 3. read active root
	root, err := s.ReadActive(ctx, "go.module.verify")
	if err != nil || root.Snapshot != s1 || root.Procedure.Goal != "Verify a change" {
		t.Fatalf("active root = %+v, %v", root, err)
	}
	// 4. resolve against that snapshot
	tree, err := knowledge.Resolve(ctx, s, "go.module.verify", root.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := describe(tree); got != "go.module.verify[guidance:agent.guidance.shared build:go.module.build[guidance:agent.guidance.shared] checks:go.module.checks[guidance:agent.guidance.shared]]" {
		t.Fatalf("resolved = %s", got)
	}
	oldChecks := child(tree, "checks").Hash
	// 5-6. improve a child and publish it
	better := Proc("go.module.checks", "Run gofmt, go vet and staticcheck", []string{"gate_commands", "module"}, guide)
	s2, err := s.Publish(ctx, s1, []knowledge.Change{{Key: "go.module.checks", Procedure: better}}, note("checks: add staticcheck"))
	if err != nil {
		t.Fatal(err)
	}
	// 7. new tasks see the improved child
	root2, _ := s.ReadActive(ctx, "go.module.verify")
	tree2, err := knowledge.Resolve(ctx, s, "go.module.verify", root2.Snapshot)
	if err != nil || root2.Snapshot != s2 || child(tree2, "checks").Procedure.Goal != better.Goal {
		t.Fatalf("new task sees %+v at %s, %v", child(tree2, "checks").Procedure, root2.Snapshot, err)
	}
	if child(tree2, "build").Hash != child(tree, "build").Hash {
		t.Fatal("an unchanged procedure changed")
	}
	// 8. a task holding the old snapshot still sees the old child
	again, err := knowledge.Resolve(ctx, s, "go.module.verify", s1)
	if err != nil || child(again, "checks").Hash != oldChecks || describe(again) != describe(tree) {
		t.Fatalf("old snapshot resolves to %s, %v", describe(again), err)
	}
	// 9. a stale concurrent update is refused and changes nothing
	if _, err := s.Publish(ctx, s1, []knowledge.Change{{Key: "go.module.build", Procedure: Proc("go.module.build", "Stale", []string{"module"}, guide)}}, note("stale")); !errors.Is(err, knowledge.ErrConflict) {
		t.Fatalf("stale publish: %v", err)
	}
	if h, _ := s.Head(ctx); h != s2 {
		t.Fatalf("head moved to %s after a refused publish", h)
	}
	// 10. a bad change is reverted by a new publication
	bad := Proc("go.module.checks", "Skip the checks", []string{"gate_commands", "module"}, guide)
	s3, err := s.Publish(ctx, s2, []knowledge.Change{{Key: "go.module.checks", Procedure: bad}}, note("checks: bad"))
	if err != nil {
		t.Fatal(err)
	}
	s4, err := knowledge.Revert(ctx, s, s3, s2, note("revert bad checks"))
	if err != nil {
		t.Fatal(err)
	}
	reverted, _ := s.ReadActive(ctx, "go.module.checks")
	atS2, _ := s.ReadAt(ctx, "go.module.checks", s2)
	if s4 == s2 || reverted.Snapshot != s4 || reverted.Hash != atS2.Hash {
		t.Fatalf("revert: %s (s2 %s), %s vs %s", s4, s2, reverted.Hash, atS2.Hash)
	}
	if b, _ := s.ReadAt(ctx, "go.module.checks", s3); b.Procedure.Goal != bad.Goal {
		t.Fatal("the bad snapshot is no longer auditable")
	}
	// 11. full history for audit
	want := []string{"revert bad checks:go.module.checks", "checks: bad:go.module.checks", "checks: add staticcheck:go.module.checks",
		"initial:agent.guidance.shared,go.module.build,go.module.checks,go.module.verify"}
	if got := history(t, s); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history = %q", got)
	}
	keys, err := s.Keys(ctx, s4)
	if err != nil || len(keys) != 4 {
		t.Fatalf("keys = %v, %v", keys, err)
	}
	// 12. restart: everything survives
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	defer s.Close()
	if h, _ := s.Head(ctx); h != s4 {
		t.Fatalf("head after restart = %s, want %s", h, s4)
	}
	if old, err := knowledge.Resolve(ctx, s, "go.module.verify", s1); err != nil || child(old, "checks").Hash != oldChecks {
		t.Fatalf("old snapshot after restart: %v", err)
	}
	if got := history(t, s); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history after restart = %q", got)
	}
	if _, err := s.ReadAt(ctx, "go.module.checks", strings.Repeat("ab", 20)); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("unknown snapshot: %v", err)
	}
	if _, err := s.Publish(ctx, s4, []knowledge.Change{{Key: "go.module.checks", Procedure: better}}, note("after restart")); err != nil {
		t.Fatalf("publish after restart: %v", err)
	}
}

// concurrent races writers from one snapshot; with twoHandles they use two
// independently opened stores on the same data, as two processes would.
func concurrent(t *testing.T, open Opener, twoHandles bool) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, dir)
	defer s.Close()
	other := s
	if twoHandles {
		other = open(t, dir)
		defer other.Close()
	}
	base, err := s.Publish(ctx, "", Initial(), note("initial"))
	if err != nil {
		t.Fatal(err)
	}
	const writers = 8
	var wg sync.WaitGroup
	results := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := Proc("go.module.build", fmt.Sprintf("Build, variant %d", i), []string{"module"}, guide)
			w := s
			if i%2 == 1 {
				w = other
			}
			_, err := w.Publish(ctx, base, []knowledge.Change{{Key: "go.module.build", Procedure: p}}, note("race"))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, knowledge.ErrConflict):
			t.Fatalf("writer failed with %v, not a conflict", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d writers won from the same snapshot", won)
	}
	if h := history(t, s); len(h) != 2 {
		t.Fatalf("history = %q", h)
	}
}

// readsDuringPublish: readers of the first snapshot and of active knowledge
// run while another handle publishes; the old snapshot never changes and
// every read is of some complete snapshot.
func readsDuringPublish(t *testing.T, open Opener) {
	ctx := context.Background()
	dir := t.TempDir()
	w := open(t, dir)
	defer w.Close()
	r := open(t, dir)
	defer r.Close()
	s1, err := w.Publish(ctx, "", Initial(), note("initial"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := knowledge.Resolve(ctx, r, "go.module.verify", s1)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	errs := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				got, err := knowledge.Resolve(ctx, r, "go.module.verify", s1)
				if err == nil && describe(got) != describe(want) {
					err = fmt.Errorf("old snapshot changed: %s", describe(got))
				}
				if err == nil {
					var kn knowledge.Knowledge
					if kn, err = r.ReadActive(ctx, "go.module.checks"); err == nil {
						_, err = knowledge.Resolve(ctx, r, "go.module.verify", kn.Snapshot)
					}
				}
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	head := s1
	for i := range 20 {
		p := Proc("go.module.checks", fmt.Sprintf("Checks, revision %d", i), []string{"gate_commands", "module"}, guide)
		if head, err = w.Publish(ctx, head, []knowledge.Change{{Key: "go.module.checks", Procedure: p}}, note("rev")); err != nil {
			t.Fatal(err)
		}
	}
	close(done)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func invalid(t *testing.T, open Opener) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	defer s.Close()
	base, err := s.Publish(ctx, "", Initial(), note("initial"))
	if err != nil {
		t.Fatal(err)
	}
	local := Proc("go.module.local", "Only here", []string{"module"}, guide)
	local.Applicability = []string{"github.com/example/one"}
	cases := map[string][]knowledge.Change{
		"missing reference": {{Key: "go.module.verify", Procedure: Proc("go.module.verify", "v", nil, guide, Ref("x", "go.module.nope", nil))}},
		"cycle": {{Key: "go.module.build", Procedure: Proc("go.module.build", "b", []string{"gate_commands", "module"}, guide,
			Ref("back", "go.module.verify", map[string]string{"module": "module"}))}},
		"incompatible child": {{Key: "go.module.checks", Procedure: Proc("go.module.checks", "c", []string{"module"}, guide)}},
		"no guidance":        {{Key: "go.module.build", Procedure: Proc("go.module.build", "b", []string{"module"})}},
		"delete referenced":  {{Key: "go.module.build"}},
		"delete guidance":    {{Key: Guidance}},
		"identity mismatch":  {{Key: "go.module.other", Procedure: Proc("go.module.build", "b", []string{"module"}, guide)}},
		"bad key":            {{Key: "../escape", Procedure: Proc("../escape", "x", nil, guide)}},
		"contract not object": {{Key: "go.module.extra", Procedure: &knowledge.Procedure{Key: "go.module.extra", Goal: "g", Method: "m",
			Contract: json.RawMessage(`[1]`), Instructions: json.RawMessage(`{}`), References: []knowledge.Reference{guide}}}},
		"oversize": {{Key: "go.module.big", Procedure: &knowledge.Procedure{Key: "go.module.big", Goal: "g", Method: "m",
			Contract: json.RawMessage(`{"x":"` + strings.Repeat("a", 70<<10) + `"}`), Instructions: json.RawMessage(`{}`), References: []knowledge.Reference{guide}}}},
		"shared uses local": {{Key: "go.module.local", Procedure: local},
			{Key: "go.module.verify", Procedure: Proc("go.module.verify", "v", []string{"module"}, guide, Ref("local", "go.module.local", nil))}},
		"no changes": nil,
	}
	for name, changes := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := s.Publish(ctx, base, changes, note(name))
			if !errors.Is(err, knowledge.ErrInvalid) {
				t.Fatalf("published: %v", err)
			}
			t.Log(err)
			if h, _ := s.Head(ctx); h != base {
				t.Fatalf("head moved to %s", h)
			}
		})
	}
	// A local procedure used by an equally local parent is fine.
	parent := Proc("go.module.localparent", "Uses local", []string{"module"}, guide, Ref("local", "go.module.local", map[string]string{"module": "module"}))
	parent.Applicability = []string{"github.com/example/one"}
	if _, err := s.Publish(ctx, base, []knowledge.Change{{Key: "go.module.local", Procedure: local}, {Key: "go.module.localparent", Procedure: parent}}, note("local")); err != nil {
		t.Fatalf("valid local composition: %v", err)
	}
}

func child(n *knowledge.Node, name string) *knowledge.Node {
	for _, c := range n.Children {
		if c.Name == name {
			return c.Node
		}
	}
	return &knowledge.Node{}
}

func describe(n *knowledge.Node) string {
	if len(n.Children) == 0 {
		return n.Key
	}
	var parts []string
	for _, c := range n.Children {
		parts = append(parts, c.Name+":"+describe(c.Node))
	}
	return n.Key + "[" + strings.Join(parts, " ") + "]"
}

func history(t *testing.T, s knowledge.Store) []string {
	t.Helper()
	h, err := s.History(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range h {
		out = append(out, strings.TrimSpace(e.Note.Message)+":"+strings.Join(e.Changed, ","))
	}
	return out
}
