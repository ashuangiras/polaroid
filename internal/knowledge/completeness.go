package knowledge

// CompletenessGaps is Catalog.CompletenessGaps' result: typed records that
// do not meet ADR-0057's completeness bar. Used by discover(kind="stats")'s
// completeness counts, and by CheckCompleteness, which Kernel.publish calls
// against every new publish candidate (ADR-0057, activated by #164) — never
// against an already-committed snapshot: see publish's own comment for why.
type CompletenessGaps struct {
	// MissingDefinition lists every Domain, Concept or Procedure record
	// with an empty Definition, sorted by key.
	MissingDefinition []string
	// MissingObligation lists every Procedure record with none of
	// Requires, Preconditions or Postconditions, own or inherited
	// (transitively, via Specializes), sorted by key.
	MissingObligation []string
}

// CompletenessGaps reports this snapshot's gaps against ADR-0057's bar.
// Only typed records (Catalog.Records) are considered: legacy procedure
// documents (Catalog.Get) have no Definition field and are not part of
// Corpus Logic's obligation reasoning.
func (c *Catalog) CompletenessGaps() CompletenessGaps {
	var g CompletenessGaps
	for _, k := range c.recordKeys {
		r := c.records[k]
		if (r.Type == TypeDomain || r.Type == TypeConcept || r.Type == TypeProcedure) && r.Definition == "" {
			g.MissingDefinition = append(g.MissingDefinition, k)
		}
		if r.Type == TypeProcedure && !c.hasObligationSource(k, map[string]bool{}) {
			g.MissingObligation = append(g.MissingObligation, k)
		}
	}
	return g
}

// hasObligationSource reports whether key's own Procedure record, or any
// ancestor it (transitively) specializes, has Requires, Preconditions or
// Postconditions. seen guards against a cycle; Specializes is already
// validated acyclic, but this does not assume that.
func (c *Catalog) hasObligationSource(key string, seen map[string]bool) bool {
	if seen[key] {
		return false
	}
	seen[key] = true
	r, ok := c.records[key]
	if !ok || r.Type != TypeProcedure {
		return false
	}
	if len(r.Requires) > 0 || len(r.Preconditions) > 0 || len(r.Postconditions) > 0 {
		return true
	}
	for _, parent := range r.Specializes {
		if c.hasObligationSource(parent, seen) {
			return true
		}
	}
	return false
}

// CheckCompleteness turns CompletenessGaps into validation Problems, in
// ADR-0057's shape: naming every offending record and field. Kernel.publish
// calls this against the publish candidate only (#164): never against an
// already-committed snapshot, so existing history stays readable exactly
// as before activation, whatever its own completeness.
func (c *Catalog) CheckCompleteness() []Problem {
	var problems []Problem
	gaps := c.CompletenessGaps()
	for _, k := range gaps.MissingDefinition {
		problems = append(problems, Problem{Field: "records[" + k + "].definition", Message: "is required for a " + string(c.records[k].Type)})
	}
	for _, k := range gaps.MissingObligation {
		problems = append(problems, Problem{Field: "records[" + k + "].requires",
			Message: "at least one of requires, preconditions, postconditions, or an inherited one via specializes, is required for a Procedure"})
	}
	return problems
}
