package knowledgeops_test

import (
	"context"
	"encoding/json/jsontext"
	"path/filepath"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/knowledgeops"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

func newFeedbackService(t *testing.T) *memory.Service {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "polaroid.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return memory.NewService(store)
}

func publish(t *testing.T, k *knowledge.Kernel, changes ...knowledge.Change) knowledge.Snapshot {
	t.Helper()
	ctx := context.Background()
	base, err := k.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s, err := k.Publish(ctx, knowledgetest.WithFullReview(knowledge.Proposal{Base: base.Snapshot(), Changes: changes,
		Provenance: knowledge.Provenance{Author: "test", Summary: "fixture", Rationale: "test fixture"}}))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAgentDiscoverEmptyCatalog(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore())
	a := knowledgeops.NewAgent(k, newFeedbackService(t))
	d, err := a.Discover(context.Background(), wire.KnowledgeDiscoverRequest{Task: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Empty || d.Snapshot != "" || len(d.Candidates) != 0 {
		t.Fatalf("empty discover = %+v", d)
	}
}

func TestAgentDiscoverAndReadResolveOneLevel(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore())
	a := knowledgeops.NewAgent(k, newFeedbackService(t))
	snap := publish(t, k, knowledgetest.Initial()...)

	d, err := a.Discover(context.Background(), wire.KnowledgeDiscoverRequest{Task: "verify a change to a go module"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Empty || d.Snapshot != string(snap) || len(d.Candidates) == 0 {
		t.Fatalf("discover = %+v", d)
	}
	if d.Candidates[0].Key != "go.module.verify" {
		t.Fatalf("first candidate = %+v", d.Candidates[0])
	}

	g, err := a.Read(context.Background(), wire.KnowledgeReadRequest{Key: "go.module.verify", Snapshot: string(snap), Inputs: jsontext.Value(`{"module":"example.com/m"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if g.Snapshot != string(snap) || g.Key != "go.module.verify" || len(g.References) != 2 {
		t.Fatalf("read = %+v", g)
	}
	for _, ref := range g.References {
		if string(ref.Inputs) != `{"module":"example.com/m"}` {
			t.Fatalf("reference %s inputs = %s", ref.Name, ref.Inputs)
		}
		if ref.Goal == "" {
			t.Fatalf("reference %s: no goal from its own catalog entry", ref.Name)
		}
	}
	// Resolution is one level only: a reference's own references are not
	// expanded; reading the reference's key does that.
	if g.Philosophy != "" {
		t.Fatalf("read without full must not carry philosophy: %+v", g)
	}

	full, err := a.Read(context.Background(), wire.KnowledgeReadRequest{Key: "go.module.verify", Snapshot: string(snap), Full: true})
	if err != nil {
		t.Fatal(err)
	}
	if full.Philosophy == "" {
		t.Fatalf("full read must carry philosophy")
	}
	if full.References[0].InputMapping == nil {
		t.Fatalf("read without inputs must carry input_mapping: %+v", full.References[0])
	}

	if _, err := a.Read(context.Background(), wire.KnowledgeReadRequest{Key: "does.not.exist", Snapshot: string(snap)}); err == nil {
		t.Fatalf("read of an unknown key must fail")
	}
}

func TestAgentFeedback(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore())
	a := knowledgeops.NewAgent(k, newFeedbackService(t))
	snap := publish(t, k, knowledgetest.Initial()...)

	f, err := a.Feedback(context.Background(), wire.KnowledgeFeedbackRequest{
		Key: "go.module.verify", Snapshot: string(snap), Classification: "incorrect_instruction",
		Summary: "the step is wrong", Details: "it does not account for vendoring", Reporter: "test.agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.ID == "" || f.Classification != "incorrect_instruction" {
		t.Fatalf("feedback = %+v", f)
	}

	cases := []struct {
		name string
		in   wire.KnowledgeFeedbackRequest
	}{
		{"non-procedural classification", wire.KnowledgeFeedbackRequest{Key: "go.module.verify", Snapshot: string(snap), Classification: "environment_problem", Summary: "s", Details: "d", Reporter: "r"}},
		{"unknown classification", wire.KnowledgeFeedbackRequest{Key: "go.module.verify", Snapshot: string(snap), Classification: "bogus", Summary: "s", Details: "d", Reporter: "r"}},
		{"missing key", wire.KnowledgeFeedbackRequest{Snapshot: string(snap), Classification: "incorrect_instruction", Summary: "s", Details: "d", Reporter: "r"}},
		{"missing snapshot", wire.KnowledgeFeedbackRequest{Key: "go.module.verify", Classification: "incorrect_instruction", Summary: "s", Details: "d", Reporter: "r"}},
		{"snapshot never served the key", wire.KnowledgeFeedbackRequest{Key: "go.module.verify", Snapshot: "nonexistent", Classification: "incorrect_instruction", Summary: "s", Details: "d", Reporter: "r"}},
	}
	for _, tc := range cases {
		if _, err := a.Feedback(context.Background(), tc.in); err == nil {
			t.Errorf("%s: feedback must be refused", tc.name)
		}
	}
}
