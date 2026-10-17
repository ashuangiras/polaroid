package knowledge

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// relation is one key-valued member of a record: which record types may
// carry it, and which types its targets must have.
type relation struct {
	name    string
	get     func(*Record) []string
	sources []RecordType
	targets []RecordType // nil: the source's own type
	acyclic bool
}

var anyType = RecordTypes

func one(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func portConcepts(ps []Port) []string {
	var out []string
	for _, p := range ps {
		if p.Concept != "" {
			out = append(out, p.Concept)
		}
	}
	return out
}

// Relations are the typed relationships of ADR-0050, in member order.
var relations = []relation{
	{"classified_as", func(r *Record) []string { return r.ClassifiedAs }, anyType, []RecordType{TypeDomain}, false},
	{"broader", func(r *Record) []string { return r.Broader }, []RecordType{TypeDomain, TypeConcept}, nil, true},
	{"specializes", func(r *Record) []string { return r.Specializes }, []RecordType{TypeConcept, TypeProcedure}, nil, true},
	{"uses", func(r *Record) []string { return r.Uses }, []RecordType{TypeConcept, TypeClaim, TypeRule, TypeProcedure},
		[]RecordType{TypeConcept, TypeClaim, TypeRule, TypeProcedure}, false},
	{"composes", func(r *Record) []string { return r.Composes }, []RecordType{TypeProcedure}, []RecordType{TypeProcedure}, true},
	{"precedes", func(r *Record) []string { return r.Precedes }, []RecordType{TypeProcedure}, []RecordType{TypeProcedure}, true},
	{"requires", func(r *Record) []string { return r.Requires }, []RecordType{TypeProcedure}, []RecordType{TypeConcept}, false},
	{"supports", func(r *Record) []string { return r.Supports }, []RecordType{TypeClaim, TypeEvidence}, []RecordType{TypeClaim}, false},
	{"contradicts", func(r *Record) []string { return r.Contradicts }, []RecordType{TypeClaim, TypeEvidence, TypeConcept},
		[]RecordType{TypeClaim, TypeConcept}, false},
	{"supersedes", func(r *Record) []string { return r.Supersedes }, anyType, nil, true},
	{"preconditions", func(r *Record) []string { return r.Preconditions }, []RecordType{TypeProcedure}, []RecordType{TypeConcept}, false},
	{"postconditions", func(r *Record) []string { return r.Postconditions }, []RecordType{TypeProcedure}, []RecordType{TypeConcept}, false},
	{"invariants", func(r *Record) []string { return r.Invariants }, []RecordType{TypeProcedure}, []RecordType{TypeConcept}, false},
	{"inputs", func(r *Record) []string { return portConcepts(r.Inputs) }, []RecordType{TypeProcedure}, []RecordType{TypeConcept}, false},
	{"outputs", func(r *Record) []string { return portConcepts(r.Outputs) }, []RecordType{TypeProcedure}, []RecordType{TypeConcept}, false},
	{"excepts", func(r *Record) []string { return r.Excepts }, []RecordType{TypeException}, []RecordType{TypeRule}, false},
	{"applies_to", func(r *Record) []string { return r.AppliesTo }, []RecordType{TypeException}, []RecordType{TypeProcedure, TypeConcept}, false},
	{"waives", func(r *Record) []string { return one(r.Waives) }, []RecordType{TypeException}, []RecordType{TypeConcept}, false},
	{"replacement", func(r *Record) []string { return one(r.Replacement) }, []RecordType{TypeException}, []RecordType{TypeConcept}, false},
	{"authority.domain", func(r *Record) []string {
		if r.Authority == nil {
			return nil
		}
		return one(r.Authority.Domain)
	}, []RecordType{TypeException}, []RecordType{TypeDomain}, false},
	{"authority.path", func(r *Record) []string {
		if r.Authority == nil {
			return nil
		}
		return r.Authority.Path
	}, []RecordType{TypeException}, []RecordType{TypeDomain}, false},
}

// scalar is one non-relationship member: who may carry it, and whether it
// is required for them.
type scalar struct {
	name     string
	set      func(*Record) bool
	sources  []RecordType
	required []RecordType
}

var scalars = []scalar{
	{"definition", func(r *Record) bool { return r.Definition != "" }, []RecordType{TypeDomain, TypeConcept, TypeProcedure}, nil},
	{"statement", func(r *Record) bool { return r.Statement != "" }, []RecordType{TypeClaim, TypeEvidence, TypeRule},
		[]RecordType{TypeClaim, TypeEvidence, TypeRule}},
	{"asserts", func(r *Record) bool { return len(r.Asserts) > 0 }, []RecordType{TypeClaim}, nil},
	{"source", func(r *Record) bool { return r.Source != "" }, []RecordType{TypeEvidence}, nil},
	{"program", func(r *Record) bool { return r.Program != "" }, []RecordType{TypeRule}, []RecordType{TypeRule}},
	{"revision", func(r *Record) bool { return r.Revision != 0 }, []RecordType{TypeRule, TypeException}, []RecordType{TypeRule, TypeException}},
	{"protected", func(r *Record) bool { return r.Protected }, []RecordType{TypeRule}, nil},
	{"waivable", func(r *Record) bool { return r.Waivable }, []RecordType{TypeRule}, nil},
	{"inputs", func(r *Record) bool { return len(r.Inputs) > 0 }, []RecordType{TypeProcedure}, nil},
	{"outputs", func(r *Record) bool { return len(r.Outputs) > 0 }, []RecordType{TypeProcedure}, nil},
	{"outputs_complete", func(r *Record) bool { return r.OutputsComplete }, []RecordType{TypeProcedure}, nil},
	{"preconditions_complete", func(r *Record) bool { return r.PreconditionsComplete }, []RecordType{TypeProcedure}, nil},
	{"postconditions_complete", func(r *Record) bool { return r.PostconditionsComplete }, []RecordType{TypeProcedure}, nil},
	{"invariants_complete", func(r *Record) bool { return r.InvariantsComplete }, []RecordType{TypeProcedure}, nil},
	{"informal_conditions_complete", func(r *Record) bool { return r.InformalConditionsComplete }, []RecordType{TypeProcedure}, nil},
	{"informal_conditions", func(r *Record) bool { return len(r.InformalConditions) > 0 }, []RecordType{TypeProcedure}, nil},
	{"excepts", func(r *Record) bool { return len(r.Excepts) > 0 }, []RecordType{TypeException}, []RecordType{TypeException}},
	{"applies_to", func(r *Record) bool { return len(r.AppliesTo) > 0 }, []RecordType{TypeException}, []RecordType{TypeException}},
	{"conditions", func(r *Record) bool { return len(r.Conditions) > 0 }, []RecordType{TypeException}, nil},
	{"waives", func(r *Record) bool { return r.Waives != "" }, []RecordType{TypeException}, []RecordType{TypeException}},
	{"replacement", func(r *Record) bool { return r.Replacement != "" }, []RecordType{TypeException}, nil},
	{"justification", func(r *Record) bool { return r.Justification != "" }, []RecordType{TypeException}, []RecordType{TypeException}},
	{"rule_digest", func(r *Record) bool { return r.RuleDigest != "" }, []RecordType{TypeException}, []RecordType{TypeException}},
	{"authority", func(r *Record) bool { return r.Authority != nil }, []RecordType{TypeException}, []RecordType{TypeException}},
	{"approval", func(r *Record) bool { return r.Approval != nil }, []RecordType{TypeException}, []RecordType{TypeException}},
}

func checkLine(p *problems, field, s string, max int) {
	checkText(p, field, s, max)
	if strings.ContainsAny(s, "\r\n") {
		p.add(field, "must be one line")
	}
}

// checkRecord checks one record on its own: its type, the members its type
// admits and requires, and their values. Relationship targets are checked
// against the catalog by checkRecords.
func checkRecord(p *problems, field string, r *Record) {
	checkKey(p, field+".id", r.Key)
	if !slices.Contains(RecordTypes, r.Type) {
		p.add(field+".type", "must be one of %v", RecordTypes)
		return
	}
	checkLine(p, field+".label", r.Label, maxLineBytes)
	for _, s := range scalars {
		switch {
		case s.set(r) && !slices.Contains(s.sources, r.Type):
			p.add(field+"."+s.name, "is not admitted for a %s", r.Type)
		case !s.set(r) && slices.Contains(s.required, r.Type):
			p.add(field+"."+s.name, "is required for a %s", r.Type)
		}
	}
	for _, rel := range relations {
		keys := rel.get(r)
		if len(keys) == 0 {
			continue
		}
		if !slices.Contains(rel.sources, r.Type) {
			if !slices.ContainsFunc(scalars, func(s scalar) bool { return s.name == rel.name }) {
				p.add(field+"."+rel.name, "is not admitted for a %s", r.Type)
			}
			continue
		}
		if len(keys) > maxSetMembers {
			p.add(field+"."+rel.name, "has more than %d members", maxSetMembers)
		}
		for i, k := range keys {
			checkKey(p, fmt.Sprintf("%s.%s[%d]", field, rel.name, i), k)
			if k == r.Key {
				p.add(fmt.Sprintf("%s.%s[%d]", field, rel.name, i), "a record cannot relate to itself")
			}
		}
	}
	for name, v := range map[string]string{"definition": r.Definition, "statement": r.Statement, "justification": r.Justification} {
		if v != "" {
			checkText(p, field+"."+name, v, maxTextBytes)
		}
	}
	if r.Source != "" {
		checkLine(p, field+".source", r.Source, maxLineBytes)
	}
	if r.Program != "" && (len(r.Program) > maxProgramBytes || !utf8.ValidString(r.Program)) {
		p.add(field+".program", "must be valid UTF-8 of at most %d bytes", maxProgramBytes)
	}
	if r.Revision < 0 {
		p.add(field+".revision", "must be positive")
	}
	for i, a := range r.Asserts {
		for name, v := range map[string]string{"subject": a.Subject, "predicate": a.Predicate, "object": a.Object} {
			checkLine(p, fmt.Sprintf("%s.asserts[%d].%s", field, i, name), v, maxLineBytes)
		}
	}
	for name, ps := range map[string][]Port{"inputs": r.Inputs, "outputs": r.Outputs} {
		seen := map[string]bool{}
		for i, port := range ps {
			if !validKey(port.Name) {
				p.add(fmt.Sprintf("%s.%s[%d].name", field, name, i), "must be a key-like name")
			}
			if seen[port.Name] {
				p.add(fmt.Sprintf("%s.%s[%d].name", field, name, i), "duplicates an earlier %s name", name)
			}
			seen[port.Name] = true
		}
	}
	for i, c := range r.InformalConditions {
		checkLine(p, fmt.Sprintf("%s.informal_conditions[%d]", field, i), c, maxLineBytes)
	}
	seen := map[string]bool{}
	for i, c := range r.Conditions {
		cf := fmt.Sprintf("%s.conditions[%d]", field, i)
		if !validKey(c.Name) || seen[c.Name] {
			p.add(cf+".name", "must be a unique key-like name")
		}
		seen[c.Name] = true
		checkLine(p, cf+".value", c.Value, maxLineBytes)
	}
	if len(r.Excepts) > 1 {
		p.add(field+".excepts", "names exactly one rule")
	}
	if a := r.Authority; a != nil {
		if a.Level != AuthorityDomain && len(a.Path) > 0 {
			p.add(field+".authority.path", "is admitted only for domain authority")
		}
		switch a.Level {
		case AuthorityCorpus:
			if a.Domain != "" || a.Repository != "" {
				p.add(field+".authority", "corpus authority names neither a domain nor a repository")
			}
		case AuthorityDomain:
			if a.Domain == "" || a.Repository != "" {
				p.add(field+".authority", "domain authority names a domain and no repository")
			}
			switch {
			case len(a.Path) == 0 || a.Path[len(a.Path)-1] != a.Domain:
				p.add(field+".authority.path", "must list the domains the approval covers, from one the rule is classified in to %s", a.Domain)
			case len(slices.Compact(slices.Sorted(slices.Values(a.Path)))) != len(a.Path):
				p.add(field+".authority.path", "must not repeat a domain")
			}
		case AuthorityProject:
			if a.Domain != "" {
				p.add(field+".authority", "project authority names a repository and no domain")
			}
			checkRepository(p, field+".authority.repository", a.Repository)
		default:
			p.add(field+".authority.level", "must be corpus, domain or project")
		}
	}
	if a := r.Approval; a != nil {
		checkLine(p, field+".approval.reference", a.Reference, maxKeyBytes)
		checkKey(p, field+".approval.key", a.Key)
		if !a.ValidUntil.After(a.ValidFrom) {
			p.add(field+".approval", "valid_until must be after valid_from")
		}
		if len(a.Signature) != 64 {
			p.add(field+".approval.signature", "must be a 64-byte Ed25519 signature")
		}
	}
}

// checkRecords checks every record against the whole catalog: the key
// namespace, relationship targets and their types, acyclic relationships,
// procedure semantics naming a published procedure, and specialization
// applicability.
func checkRecords(p *problems, procs map[string]*Procedure, records map[string]*Record) {
	keys := sortedRecordKeys(records)
	for _, k := range keys {
		r := records[k]
		field := "records[" + k + "]"
		if r.Key != k {
			p.add(field+".id", "is %q, but the document is stored as %q", r.Key, k)
		}
		checkRecord(p, field, r)
		if _, isProc := procs[k]; isProc && r.Type != TypeProcedure {
			p.add(field+".id", "%s is a procedure's key; only a Procedure record may share it", k)
		}
		if r.Type == TypeProcedure && procs[k] == nil {
			p.add(field+".id", "a Procedure record describes a published procedure, and %s is not one", k)
		}
		for _, rel := range relations {
			for i, target := range rel.get(r) {
				t, ok := records[target]
				switch {
				case !ok:
					if validKey(target) && target != k {
						p.add(fmt.Sprintf("%s.%s[%d]", field, rel.name, i), "names %s, which is not a record in the catalog", target)
					}
				case rel.targets == nil && t.Type != r.Type:
					p.add(fmt.Sprintf("%s.%s[%d]", field, rel.name, i), "names the %s %s; it must name a %s", t.Type, target, r.Type)
				case rel.targets != nil && !slices.Contains(rel.targets, t.Type):
					p.add(fmt.Sprintf("%s.%s[%d]", field, rel.name, i), "names the %s %s; it must name one of %v", t.Type, target, rel.targets)
				}
			}
		}
		if r.Type == TypeProcedure && procs[k] != nil {
			for i, parent := range r.Specializes {
				if pp := procs[parent]; pp != nil && !within(procs[k].Applicability, pp.Applicability) {
					p.add(fmt.Sprintf("%s.specializes[%d]", field, i), "%s applies where its parent %s does not", k, parent)
				}
			}
		}
	}
	if len(*p) > 0 {
		return
	}
	for _, rel := range relations {
		if rel.acyclic {
			checkAcyclic(p, rel, records, keys)
		}
	}
	checkPrecedesSiblings(p, records, keys)
	checkGovernance(p, records)
}

// within reports whether a child's applicability lies within its parent's.
func within(child, parent Applicability) bool {
	if parent.Scope == ScopeLocal {
		return child.Scope == ScopeLocal && child.Repository == parent.Repository
	}
	return true
}

func checkAcyclic(p *problems, rel relation, records map[string]*Record, keys []string) {
	const visiting, done = 1, 2
	state := map[string]int{}
	var visit func(k string, path []string) bool
	visit = func(k string, path []string) bool {
		switch state[k] {
		case visiting:
			i := slices.Index(path, k)
			p.add("records["+k+"]."+rel.name, "cycle: %s", strings.Join(append(path[i:], k), " -> "))
			return false
		case done:
			return true
		}
		state[k] = visiting
		for _, next := range rel.get(records[k]) {
			if records[next] != nil && !visit(next, append(path, k)) {
				return false
			}
		}
		state[k] = done
		return true
	}
	for _, k := range keys {
		if !visit(k, nil) {
			return
		}
	}
}

// checkPrecedesSiblings requires every precedes target to be a sibling of
// its source (ADR-0052): another key in the same parent's composes set.
// Scoped per composing parent, not relation-wide: two keys composed by
// different parents are never siblings of each other, even if each
// appears in some other procedure's composes set.
func checkPrecedesSiblings(p *problems, records map[string]*Record, keys []string) {
	siblings := map[string]map[string]bool{}
	for _, r := range records {
		if r.Type != TypeProcedure {
			continue
		}
		for _, a := range r.Composes {
			for _, b := range r.Composes {
				if a == b {
					continue
				}
				if siblings[a] == nil {
					siblings[a] = map[string]bool{}
				}
				siblings[a][b] = true
			}
		}
	}
	for _, k := range keys {
		r := records[k]
		if r.Type != TypeProcedure {
			continue
		}
		for i, t := range r.Precedes {
			if !siblings[k][t] {
				p.add(fmt.Sprintf("records[%s].precedes[%d]", k, i), "%s must be a sibling of %s: another key in the same parent's composes set", t, k)
			}
		}
	}
}

func sortedRecordKeys(records map[string]*Record) []string {
	keys := make([]string, 0, len(records))
	for k := range records {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
