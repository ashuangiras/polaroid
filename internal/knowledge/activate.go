package knowledge

import (
	"context"
	"slices"
	"time"
)

// Load validates the files of a complete snapshot as a catalog, exactly as
// every read does. It is how knowledge that arrives from elsewhere is
// checked before it is accepted. Without validators, a snapshot holding
// typed records is refused.
func Load(ctx context.Context, s Snapshot, f Files, vs ...RecordValidator) (*Catalog, error) {
	return loadCatalog(ctx, s, f, vs)
}

// Check validates the files of a complete snapshot as this kernel reads
// them, with its record validators.
func (k *Kernel) Check(ctx context.Context, s Snapshot, f Files) (*Catalog, error) {
	return loadCatalog(ctx, s, f, k.validators)
}

// Activation makes knowledge that arrived from elsewhere the active
// knowledge, as a fast-forward: nothing published here is lost.
type Activation struct {
	Base       Snapshot   // the active snapshot the decision was made against
	Target     Snapshot   // the accepted snapshot, which descends from Base
	Provenance Provenance // who accepted it, and why
	Origin     []Trailer  // where it came from, recorded with the decision
}

// TrailerAccepted records the catalog validation an activation passed.
const TrailerAccepted = "Polaroid-Accepted"

// Activate validates the target's complete catalog under the store's
// writer lock and makes it active, or returns ErrStale when Base is no
// longer active and ErrDiverged when the target does not descend from it.
// The decision is recorded with its provenance; the target's publications
// keep their own authors, rationale and identities.
func (k *Kernel) Activate(ctx context.Context, a Activation) error {
	var p problems
	a.Provenance.check(&p)
	if a.Target == "" {
		p.add("target", "is required")
	}
	if err := p.err(); err != nil {
		return err
	}
	pv := a.Provenance
	trailers := slices.Clone(a.Origin)
	if pv.Finding != "" {
		trailers = append(trailers, Trailer{TrailerFinding, pv.Finding})
	}
	if pv.Validation != "" {
		trailers = append(trailers, Trailer{TrailerValidation, pv.Validation})
	}
	trailers = append(trailers, Trailer{TrailerAccepted, "complete catalog: documents, references, applicability, cycles, limits"})
	var cat *Catalog
	err := k.store.Advance(ctx, Advance{Expected: a.Base, Target: a.Target, Author: pv.Author, Subject: pv.Summary, Body: pv.Rationale, Trailers: trailers,
		Check: func(f Files) error {
			c, err := loadCatalog(ctx, a.Target, f, k.validators)
			cat = c
			return err
		}})
	if err != nil {
		return err
	}
	k.remember(cat)
	return nil
}

// ActivationRecord is one accepted activation, newest first in Activations.
type ActivationRecord struct {
	Target     Snapshot
	Previous   Snapshot
	When       time.Time
	Provenance Provenance
	Accepted   string
	Origin     []Trailer
}

func (k *Kernel) Activations(ctx context.Context, limit int) ([]ActivationRecord, error) {
	entries, err := k.store.Activations(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ActivationRecord, 0, len(entries))
	for _, e := range entries {
		rec := ActivationRecord{Target: e.Target, Previous: e.Previous, When: e.When,
			Provenance: Provenance{Author: e.Author, Summary: e.Subject, Rationale: e.Body}}
		for _, t := range e.Trailers {
			switch t.Key {
			case TrailerFinding:
				rec.Provenance.Finding = t.Value
			case TrailerValidation:
				rec.Provenance.Validation = t.Value
			case TrailerAccepted:
				rec.Accepted = t.Value
			default:
				rec.Origin = append(rec.Origin, t)
			}
		}
		out = append(out, rec)
	}
	return out, nil
}
