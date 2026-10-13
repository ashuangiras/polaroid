package knowledge_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
)

// The domain semantics, without storage.
func TestScenarioInMemory(t *testing.T) {
	stores := map[string]*knowledgetest.MemoryStore{}
	knowledgetest.Run(t, func(t *testing.T, dir string) knowledge.Store {
		if stores[dir] == nil {
			stores[dir] = knowledgetest.NewMemoryStore()
		}
		return stores[dir]
	})
}

func prov() knowledge.Provenance {
	return knowledge.Provenance{Author: "test.agent", Summary: "change", Rationale: "test"}
}

func publish(t *testing.T, k *knowledge.Kernel, base knowledge.Snapshot, changes ...knowledge.Change) (knowledge.Snapshot, error) {
	t.Helper()
	return k.Publish(context.Background(), knowledge.Proposal{Base: base, Changes: changes, Provenance: prov()})
}

func put(p *knowledge.Procedure) knowledge.Change { return knowledge.Change{Key: p.Key, Procedure: p} }

func TestDocumentsAreCanonical(t *testing.T) {
	p := knowledgetest.Proc("go.module.verify", "Verify", "build=go.module.build")
	p.Contract = jsontext.Value(`{ "z": 1, "a": [ 2 ] }`)
	cond := "only for code changes"
	p.References[0].Condition = &cond
	doc, err := knowledge.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"key":"go.module.verify","goal":"Verify","philosophy":"Small, verified steps.","method":"Run each step in order; stop at the first failure.",` +
		`"applicability":{"shared":{}},"contract":{"z":1,"a":[2]},"instructions":{"steps":[{"id":"run","action":"Verify"}]},` +
		`"references":[{"name":"build","key":"go.module.build","condition":"only for code changes","inputs":{"module":{"input":"module"}}}]}` + "\n"
	if string(doc) != want {
		t.Fatalf("encoding:\n%s\nwant\n%s", doc, want)
	}
	back, err := knowledge.Decode(doc)
	if err != nil || *back.References[0].Condition != cond || string(back.Contract) != `{"z":1,"a":[2]}` {
		t.Fatalf("decode: %+v, %v", back, err)
	}
	for name, bad := range map[string]string{
		"unknown member":    strings.Replace(want, `{"key"`, `{"run":"rm -rf /","key"`, 1),
		"duplicate member":  strings.Replace(want, `{"key":"go.module.verify"`, `{"key":"go.module.verify","key":"x"`, 1),
		"not canonical":     " " + want,
		"reordered members": `{"goal":"Verify","key":"go.module.verify","philosophy":"p","method":"m","contract":{},"instructions":{}}` + "\n",
		"both scopes":       strings.Replace(want, `{"shared":{}}`, `{"shared":{},"repository":"github.com/a/b"}`, 1),
		"trailing data":     want + "{}",
	} {
		if _, err := knowledge.Decode([]byte(bad)); err == nil {
			t.Errorf("%s: decoded", name)
		}
	}
	for key, path := range map[string]string{
		"go.module.verify": "procedures/go/go.module.verify.json",
		"go":               "procedures/go/go.json",
		"dev-change_check": "procedures/dev/dev-change_check.json",
	} {
		if got := knowledge.Path(key); got != path {
			t.Errorf("Path(%s) = %s, want %s", key, got, path)
		}
	}
}

// What storage holds is validated when read: a tree with a stray file, a
// document under another key, or a broken one is refused, never served.
func TestStoredCatalogIsValidatedOnRead(t *testing.T) {
	ctx := context.Background()
	good, _ := knowledge.Encode(knowledgetest.Proc("go.module.build", "Build"))
	for name, files := range map[string][]knowledge.FileChange{
		"stray file":        {{Path: "README.md", Content: []byte("hi")}},
		"conflicting key":   {{Path: knowledge.Path("go.module.other"), Content: good}},
		"wrong directory":   {{Path: "procedures/dev/go.module.build.json", Content: good}},
		"not canonical":     {{Path: knowledge.Path("go.module.build"), Content: append([]byte(" "), good...)}},
		"missing reference": {{Path: knowledge.Path("go.module.verify"), Content: mustEncode(t, knowledgetest.Proc("go.module.verify", "Verify", "b=go.module.gone"))}},
	} {
		t.Run(name, func(t *testing.T) {
			store := knowledgetest.NewMemoryStore()
			s, err := store.Commit(ctx, knowledge.Commit{Changes: files, Author: "someone", Subject: name})
			if err != nil {
				t.Fatal(err)
			}
			_, err = knowledge.New(store).At(ctx, s)
			if !errors.Is(err, knowledge.ErrInvalid) {
				t.Fatalf("served: %v", err)
			}
			t.Log(err)
		})
	}
}

func mustEncode(t *testing.T, p *knowledge.Procedure) []byte {
	t.Helper()
	b, err := knowledge.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// lossyStore is a faulty adapter that silently drops a publication's last
// change.
type lossyStore struct{ *knowledgetest.MemoryStore }

func (s lossyStore) Commit(ctx context.Context, c knowledge.Commit) (knowledge.Snapshot, error) {
	c.Changes = c.Changes[:len(c.Changes)-1]
	return s.MemoryStore.Commit(ctx, c)
}

// The kernel confirms that the tree a store is about to publish is exactly
// the catalog it validated.
func TestKernelConfirmsTheStoredTree(t *testing.T) {
	store := lossyStore{knowledgetest.NewMemoryStore()}
	_, err := publish(t, knowledge.New(store), "", knowledgetest.Initial()...)
	if err == nil {
		t.Fatal("published a tree that is not the validated catalog")
	}
	if s, _ := store.Active(context.Background()); s != "" {
		t.Fatalf("active = %s", s)
	}
	t.Log(err)
}

func TestCompositionLimitsAndRules(t *testing.T) {
	ctx := context.Background()
	newKernel := func() *knowledge.Kernel { return knowledge.New(knowledgetest.NewMemoryStore()) }
	refuse := func(t *testing.T, changes ...knowledge.Change) {
		t.Helper()
		_, err := publish(t, newKernel(), "", changes...)
		if !errors.Is(err, knowledge.ErrInvalid) {
			t.Fatalf("published: %v", err)
		}
		t.Log(err)
	}

	t.Run("depth", func(t *testing.T) {
		var changes []knowledge.Change
		for i := range knowledge.MaxGraphDepth + 1 {
			p := knowledgetest.Proc(fmt.Sprintf("chain.p%02d", i), "step")
			if i+1 <= knowledge.MaxGraphDepth {
				p.References = knowledgetest.Proc("x", "x", fmt.Sprintf("next=chain.p%02d", i+1)).References
			}
			changes = append(changes, put(p))
		}
		refuse(t, changes...)
		if _, err := publish(t, newKernel(), "", changes[1:]...); err != nil {
			t.Fatalf("a chain of exactly %d: %v", knowledge.MaxGraphDepth, err)
		}
	})
	t.Run("expanded size", func(t *testing.T) {
		var changes []knowledge.Change
		for i := range 12 {
			p := knowledgetest.Proc(fmt.Sprintf("fan.p%02d", i), "step")
			if i < 11 {
				next := fmt.Sprintf("fan.p%02d", i+1)
				p.References = knowledgetest.Proc("x", "x", "a="+next, "b="+next).References
			}
			changes = append(changes, put(p))
		}
		refuse(t, changes...)
	})
	t.Run("inputs mapping", func(t *testing.T) {
		p := knowledgetest.Proc("go.module.verify", "Verify", "build=go.module.build")
		p.References[0].Inputs = jsontext.Value(`{"module":{"input":""},"x":{"value":1,"input":"y"}}`)
		refuse(t, put(knowledgetest.Proc("go.module.build", "Build")), put(p))
	})
	t.Run("applicability", func(t *testing.T) {
		one := knowledge.Applicability{Scope: knowledge.ScopeLocal, Repository: "github.com/example/one"}
		child := knowledgetest.Proc("team.child", "Child")
		child.Applicability = one
		parent := knowledgetest.Proc("team.parent", "Parent", "child=team.child")
		parent.Applicability = knowledge.Applicability{Scope: knowledge.ScopeLocal, Repository: "github.com/example/two"}
		refuse(t, put(child), put(parent))
		parent.Applicability = one
		k := newKernel()
		if _, err := publish(t, k, "", put(child), put(parent)); err != nil {
			t.Fatalf("local parent of a local child in the same repository: %v", err)
		}
		c, _ := k.Active(ctx)
		if _, err := c.Resolve("team.parent", "github.com/example/two"); !errors.Is(err, knowledge.ErrNotApplicable) {
			t.Fatalf("resolve elsewhere: %v", err)
		}
		if _, err := c.Resolve("team.parent", "github.com/example/one"); err != nil {
			t.Fatal(err)
		}
		if got := c.Discover("parent child", "github.com/example/two", 5); len(got) != 0 {
			t.Fatalf("discovery outside the repository = %+v", got)
		}
	})
	t.Run("bad fields", func(t *testing.T) {
		bad := knowledgetest.Proc("go.module.build", "two\nlines")
		bad.Philosophy = ""
		bad.Instructions = nil
		bad.Applicability = knowledge.Applicability{Scope: knowledge.ScopeLocal, Repository: "../etc"}
		refuse(t, put(bad))
		refuse(t, put(knowledgetest.Proc("Go.Module", "Build")))
	})
}

func TestDiscoveryIsDeterministic(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore())
	var changes []knowledge.Change
	for _, key := range []string{"b.lint", "a.lint", "c.build"} {
		changes = append(changes, put(knowledgetest.Proc(key, "Lint the code")))
	}
	if _, err := publish(t, k, "", changes...); err != nil {
		t.Fatal(err)
	}
	c, _ := k.Active(context.Background())
	for range 3 {
		got := c.Discover("lint", "", 0)
		if len(got) != 3 || got[0].Key != "a.lint" || got[1].Key != "b.lint" || got[2].Key != "c.build" || got[0].Score != 3 || got[2].Score != 2 {
			t.Fatalf("discover = %+v", got)
		}
	}
	if got := c.Discover("", "", 0); len(got) != 0 {
		t.Fatalf("empty query = %+v", got)
	}
}
