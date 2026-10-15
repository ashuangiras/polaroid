package knowledge

// Stats summarizes one snapshot's typed records and legacy procedure
// documents (ADR-0051: discover(kind="stats")). It reads no history and
// writes nothing.
type Stats struct {
	// ByType counts every typed record (Catalog.Records) by RecordType.
	// Every type in RecordTypes is listed, zero included.
	ByType map[string]int
	// ByDomain groups every typed record by the Domain record(s) it is
	// directly classified_as (not transitively through broader); a record
	// with none counts under "unclassified".
	ByDomain map[string]int
	// Procedures counts the legacy procedure documents discover(kind=
	// "procedures") and read serve. A typed Procedure record (counted in
	// ByType) is not the same count: every one names a procedure document,
	// but not every procedure document has a typed record yet.
	Procedures ProcedureStats
	// Completeness counts this snapshot's gaps against ADR-0057's
	// completeness bar (CompletenessGaps), not yet enforced at publish.
	Completeness CompletenessStats
}

// CompletenessStats is Stats.Completeness: how many typed records fall
// short of ADR-0057's completeness bar, a count derived from
// CompletenessGaps, carrying no per-repository applicability (typed
// records have none) and so never filtered by Stats' repository argument.
type CompletenessStats struct {
	MissingDefinition int
	MissingObligation int
}

// ProcedureStats is Stats.Procedures: Total, and ByRepository from each
// procedure's declared Applicability: "shared", "unspecified", or the
// repository identifier of a locally scoped one.
type ProcedureStats struct {
	Total        int
	ByRepository map[string]int
}

const unclassified = "unclassified"

// Stats counts this snapshot's typed records, by type and by direct
// domain classification, and its legacy procedure documents, by
// applicability. With repository, Procedures is limited to what applies
// there (shared, unspecified, and that repository's own); ByType and
// ByDomain carry no per-repository applicability and are never filtered.
func (c *Catalog) Stats(repository string) Stats {
	byType := map[string]int{}
	for _, t := range RecordTypes {
		byType[string(t)] = 0
	}
	byDomain := map[string]int{}
	for _, r := range c.records {
		byType[string(r.Type)]++
		if len(r.ClassifiedAs) == 0 {
			byDomain[unclassified]++
			continue
		}
		for _, d := range r.ClassifiedAs {
			byDomain[d]++
		}
	}
	procs := ProcedureStats{ByRepository: map[string]int{}}
	for _, k := range c.keys {
		p := c.procs[k]
		if repository != "" && !p.Applicability.ApplicableIn(repository) {
			continue
		}
		procs.Total++
		key := string(p.Applicability.Scope)
		if key == "" {
			key = "unspecified"
		}
		if p.Applicability.Scope == ScopeLocal {
			key = p.Applicability.Repository
		}
		procs.ByRepository[key]++
	}
	return Stats{ByType: byType, ByDomain: byDomain, Procedures: procs, Completeness: completenessStats(c.CompletenessGaps())}
}

func completenessStats(g CompletenessGaps) CompletenessStats {
	return CompletenessStats{MissingDefinition: len(g.MissingDefinition), MissingObligation: len(g.MissingObligation)}
}
