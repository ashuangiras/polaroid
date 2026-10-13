package wire

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"encoding/json/jsontext"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// VersionFieldNames are the top-level version members a fields projection
// may name (GET .../versions/{n}?fields=…, MCP get_version's fields),
// in their canonical response order (#131). contract and instructions are
// each a whole, opaque document: a fields projection selects one of them in
// full or not at all, never a member inside either (AGENTS.md: Polaroid
// never interprets contract or instructions).
var VersionFieldNames = []string{"philosophy", "method", "goal", "applicability", "contract", "instructions", "references", "revision_reason"}

// ParseFields splits raw on commas, validates each name against allowed,
// and returns the distinct names given, in allowed's order. It is an error,
// naming the first unknown name, if any name is not in allowed, or if raw
// names nothing.
func ParseFields(raw string, allowed []string) ([]string, error) {
	want := map[string]bool{}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !slices.Contains(allowed, name) {
			return nil, fmt.Errorf("unknown field %q", name)
		}
		want[name] = true
	}
	if len(want) == 0 {
		return nil, fmt.Errorf("must name at least one of %s", strings.Join(allowed, ", "))
	}
	fields := make([]string, 0, len(want))
	for _, name := range allowed {
		if want[name] {
			fields = append(fields, name)
		}
	}
	return fields, nil
}

// VersionProjection is a version with only the requested top-level members
// (fields) present, alongside its identity (procedure_id, version, scope,
// created_at), which is always present, exactly as the complete version
// would report them (#131). A member not named in fields is absent, never
// null or empty: a client that already holds it is not told it is empty,
// and one that does not can tell the difference from GET without fields.
type VersionProjection struct {
	ProcedureID    string         `json:"procedure_id"`
	Version        int            `json:"version"`
	Scope          string         `json:"scope"`
	CreatedAt      time.Time      `json:"created_at"`
	Philosophy     *string        `json:"philosophy,omitzero"`
	Method         *string        `json:"method,omitzero"`
	Goal           *string        `json:"goal,omitzero"`
	Applicability  *Applicability `json:"applicability,omitzero"`
	Contract       jsontext.Value `json:"contract,omitzero"`
	Instructions   jsontext.Value `json:"instructions,omitzero"`
	References     []Reference    `json:"references,omitzero"`
	RevisionReason *string        `json:"revision_reason,omitzero"`
}

// NewVersionProjection builds the projection of v named by fields (each a
// name from VersionFieldNames; an unknown name is the caller's error to
// catch with ParseFields first).
func NewVersionProjection(v memory.Version, fields []string) VersionProjection {
	body := VersionProjection{ProcedureID: v.ProcedureID, Version: v.Number, Scope: v.Applicability.Name(), CreatedAt: v.CreatedAt}
	for _, f := range fields {
		switch f {
		case "philosophy":
			body.Philosophy = &v.Philosophy
		case "method":
			body.Method = &v.Method
		case "goal":
			body.Goal = &v.Goal
		case "applicability":
			body.Applicability = NewApplicability(v.Applicability)
		case "contract":
			body.Contract = v.Contract
		case "instructions":
			body.Instructions = v.Instructions
		case "references":
			body.References = newReferences(v.References)
		case "revision_reason":
			body.RevisionReason = &v.RevisionReason
		}
	}
	return body
}
