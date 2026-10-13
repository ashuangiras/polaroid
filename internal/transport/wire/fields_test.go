package wire_test

import (
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

func TestParseFields(t *testing.T) {
	allowed := wire.VersionFieldNames
	got, err := wire.ParseFields("instructions, goal ,goal", allowed)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// Canonical order (allowed's), duplicates collapsed.
	if len(got) != 2 || got[0] != "goal" || got[1] != "instructions" {
		t.Fatalf("fields = %v", got)
	}
	if _, err := wire.ParseFields("contract,bogus", allowed); err == nil {
		t.Fatal("want an error naming the unknown field")
	}
	if _, err := wire.ParseFields("", allowed); err == nil {
		t.Fatal("want an error: fields must name at least one field")
	}
	if _, err := wire.ParseFields("  ,  ", allowed); err == nil {
		t.Fatal("blank entries name nothing")
	}
}

// TestVersionProjectionOmitsWhatWasNotAsked: a member not named in fields
// is absent, even when it holds a legitimate empty value (goal), so a
// client can tell "not requested" apart from "requested and empty".
func TestVersionProjectionOmitsWhatWasNotAsked(t *testing.T) {
	v := memory.Version{
		ProcedureID: "p1", Number: 3, Applicability: memory.Applicability{Kind: memory.ScopeShared},
		Philosophy: "why", Method: "how", Goal: "", Contract: []byte(`{"a":1}`), Instructions: []byte(`{"steps":[]}`),
		RevisionReason: "because", CreatedAt: time.Unix(0, 0).UTC(),
	}
	body := wire.NewVersionProjection(v, []string{"goal", "instructions"})
	if body.ProcedureID != "p1" || body.Version != 3 || body.Scope != "shared" {
		t.Fatalf("identity = %+v", body)
	}
	if body.Goal == nil || *body.Goal != "" {
		t.Fatalf("goal = %v, want a pointer to \"\" (requested, legitimately empty)", body.Goal)
	}
	if body.Instructions == nil || string(body.Instructions) != `{"steps":[]}` {
		t.Fatalf("instructions = %s", body.Instructions)
	}
	if body.Philosophy != nil || body.Method != nil || body.Contract != nil || body.Applicability != nil || body.RevisionReason != nil {
		t.Fatalf("unrequested members must be absent: %+v", body)
	}
}
