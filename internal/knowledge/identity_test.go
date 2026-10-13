package knowledge_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
)

func TestFeedbackRefIdentity(t *testing.T) {
	ctx := context.Background()
	m := knowledgetest.NewMemoryStore()
	k := knowledge.New(m)
	s1, err := k.Publish(ctx, knowledge.Proposal{Changes: knowledgetest.Initial(), Provenance: prov()})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := k.Publish(ctx, knowledge.Proposal{Base: s1, Changes: []knowledge.Change{
		put(knowledgetest.Proc("go.module.verify", "Verify, without checks", "build=go.module.build")),
		{Key: "go.module.checks"},
	}, Provenance: prov()})
	if err != nil {
		t.Fatal(err)
	}

	ref := knowledge.FeedbackRef{Key: "go.module.checks", Snapshot: s1}
	enc, err := ref.Encode()
	if err != nil || string(enc) != `{"key":"go.module.checks","snapshot":"`+string(s1)+`"}` {
		t.Fatalf("encode = %s, %v", enc, err)
	}
	back, err := knowledge.ParseFeedbackRef(enc)
	if err != nil || back != ref {
		t.Fatalf("parse = %+v, %v", back, err)
	}
	// The snapshot that produced the finding still serves the procedure,
	// although a later publication removed it.
	if err := k.CheckFeedbackRef(ctx, ref); err != nil {
		t.Fatalf("finding against an earlier snapshot: %v", err)
	}
	if err := k.CheckFeedbackRef(ctx, knowledge.FeedbackRef{Key: "go.module.checks", Snapshot: s2}); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("key absent from its snapshot: %v", err)
	}
	staged := stage(t, m, s2, knowledgetest.Proc("go.module.lint", "Lint"))
	for _, r := range []knowledge.FeedbackRef{{Key: "go.module.lint", Snapshot: staged}, {Key: "go.module.build", Snapshot: "mem-404"}} {
		if err := k.CheckFeedbackRef(ctx, r); !errors.Is(err, knowledge.ErrNotFound) {
			t.Fatalf("%+v: %v", r, err)
		}
	}

	for name, raw := range map[string]string{
		"unknown member":    `{"key":"go.module.build","snapshot":"mem-1","version":2}`,
		"missing snapshot":  `{"key":"go.module.build"}`,
		"missing key":       `{"snapshot":"mem-1"}`,
		"duplicate member":  `{"key":"go.module.build","key":"go.module.checks","snapshot":"mem-1"}`,
		"key not a string":  `{"key":7,"snapshot":"mem-1"}`,
		"not canonical key": `{"key":"Go.Module","snapshot":"mem-1"}`,
		"empty snapshot":    `{"key":"go.module.build","snapshot":""}`,
		"spaced snapshot":   `{"key":"go.module.build","snapshot":"mem 1"}`,
		"long snapshot":     `{"key":"go.module.build","snapshot":"` + strings.Repeat("a", 129) + `"}`,
		"not an object":     `["go.module.build","mem-1"]`,
		"procedure id":      `{"procedure_id":"01a1","version":1}`,
	} {
		if _, err := knowledge.ParseFeedbackRef(jsontext.Value(raw)); !errors.Is(err, knowledge.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := (knowledge.FeedbackRef{Key: "go.module.build"}).Encode(); !errors.Is(err, knowledge.ErrInvalid) {
		t.Fatalf("encoding without a snapshot: %v", err)
	}
}

func TestPublicationRefIdentity(t *testing.T) {
	ref := knowledge.PublicationRef{Instance: "pi-0123456789abcdef0123456789abcdef", Snapshot: knowledge.Snapshot(strings.Repeat("ab", 20)), Key: "go.module.build"}
	s := ref.String()
	if s != "polaroid-knowledge:pi-0123456789abcdef0123456789abcdef/"+strings.Repeat("ab", 20)+"#go.module.build" {
		t.Fatalf("string = %s", s)
	}
	if back, err := knowledge.ParsePublicationRef(s); err != nil || back != ref {
		t.Fatalf("parse = %+v, %v", back, err)
	}
	for _, bad := range []string{
		"", "polaroid-knowledge:", "https://example.com/x#go.module.build",
		"polaroid-knowledge:pi-1/abc", "polaroid-knowledge:pi-1#go.module.build",
		"polaroid-knowledge:PI-1/abc#go.module.build", "polaroid-knowledge:pi-1/abc#Go.Module",
		"polaroid-knowledge:pi-1/a b#go.module.build", "polaroid-knowledge:pi-1/abc#go.module.build#x",
	} {
		if _, err := knowledge.ParsePublicationRef(bad); !errors.Is(err, knowledge.ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestMapInputs(t *testing.T) {
	parent := jsontext.Value(`{"module":"./svc","race":true,"unused":1}`)
	for mapping, want := range map[string]string{
		`{"module":{"input":"module"}}`: `{"module":"./svc"}`,
		`{"target":{"input":"module"},"flags":{"value":["-v"]},"race":{"input":"race"}}`: `{"target":"./svc","flags":["-v"],"race":true}`,
		`{"missing":{"input":"absent"},"module":{"input":"module"}}`:                     `{"module":"./svc"}`,
		`{"nothing":{"value":null}}`:                                                     `{"nothing":null}`,
		`{}`:                                                                             `{}`,
	} {
		got, err := knowledge.MapInputs(jsontext.Value(mapping), parent)
		if err != nil || string(got) != want {
			t.Errorf("%s: %s, %v; want %s", mapping, got, err, want)
		}
	}
	if got, err := knowledge.MapInputs(jsontext.Value(`{"module":{"input":"module"}}`), nil); err != nil || string(got) != `{}` {
		t.Errorf("no parent inputs: %s, %v", got, err)
	}
	for _, bad := range []string{`{"a":{"input":"x","value":1}}`, `{"a":{}}`, `{"a":{"from":"x"}}`, `{"a":"x"}`, `[]`} {
		if _, err := knowledge.MapInputs(jsontext.Value(bad), parent); !errors.Is(err, knowledge.ErrInvalid) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if _, err := knowledge.MapInputs(jsontext.Value(`{}`), jsontext.Value(`[1]`)); !errors.Is(err, knowledge.ErrInvalid) {
		t.Error("parent inputs that are not an object")
	}
}

// Origin follows how knowledge arrived: an activation imports, every
// publication here (a rollback included) is local.
func TestOriginFollowsArrival(t *testing.T) {
	ctx := context.Background()
	m := knowledgetest.NewMemoryStore()
	k := knowledge.New(m)
	s1, err := k.Publish(ctx, knowledge.Proposal{Changes: knowledgetest.Initial(), Provenance: prov()})
	if err != nil {
		t.Fatal(err)
	}
	a := stage(t, m, s1, knowledgetest.Proc("go.module.build", "Build, from elsewhere"))
	b := stage(t, m, a, knowledgetest.Proc("go.module.checks", "Checks, from elsewhere"))
	if err := k.Activate(ctx, knowledge.Activation{Base: s1, Target: b, Provenance: decision()}); err != nil {
		t.Fatal(err)
	}
	back, err := k.Rollback(ctx, b, s1, prov())
	if err != nil {
		t.Fatal(err)
	}
	for s, want := range map[knowledge.Snapshot]knowledge.Origin{s1: knowledge.OriginLocal, a: knowledge.OriginImported, b: knowledge.OriginImported, back: knowledge.OriginLocal} {
		if got, err := k.Origin(ctx, s); err != nil || got != want {
			t.Errorf("%s: %s, %v; want %s", s, got, err, want)
		}
	}
	if _, err := k.Origin(ctx, "mem-404"); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("origin of an unknown snapshot: %v", err)
	}
	if id, err := k.Instance(ctx); err != nil || id != "memory-instance" {
		t.Fatalf("instance = %q, %v", id, err)
	}
}

// Every child read with a discovery's snapshot token sees that snapshot's
// knowledge, however much is published meanwhile.
func TestChildReadsUseTheDiscoveryToken(t *testing.T) {
	ctx := context.Background()
	k := knowledge.New(knowledgetest.NewMemoryStore())
	if _, err := k.Publish(ctx, knowledge.Proposal{Changes: knowledgetest.Initial(), Provenance: prov()}); err != nil {
		t.Fatal(err)
	}
	found, err := k.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token := found.Snapshot()
	root, err := found.Get(found.Discover("verify a go module change", "", 1)[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Publish(ctx, knowledge.Proposal{Base: token, Changes: []knowledge.Change{put(knowledgetest.Proc("go.module.build", "Build, changed meanwhile"))}, Provenance: prov()}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range root.References {
		c, err := k.At(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		child, err := c.Get(ref.Key)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(child.Goal, "meanwhile") {
			t.Fatalf("child %s read from a later snapshot", ref.Key)
		}
		inputs, err := knowledge.MapInputs(ref.Inputs, jsontext.Value(`{"module":"./svc"}`))
		if err != nil || string(inputs) != `{"module":"./svc"}` {
			t.Fatalf("child inputs = %s, %v", inputs, err)
		}
	}
}
