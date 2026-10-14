package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"slices"
)

// RuleDigest is the SHA-256 of a rule's normative semantics: its key,
// statement, program, protection, waivability and domains, as RFC 8785
// canonical JSON. An exception approval signs it, so an approval never
// covers a rule whose semantics changed after it was given (ADR-0050).
func RuleDigest(r *Record) string {
	sem := struct {
		Key          string   `json:"key"`
		Statement    string   `json:"statement"`
		Program      string   `json:"program"`
		Protected    bool     `json:"protected"`
		Waivable     bool     `json:"waivable"`
		ClassifiedAs []string `json:"classified_as"`
	}{r.Key, r.Statement, r.Program, r.Protected, r.Waivable, slices.Sorted(slices.Values(r.ClassifiedAs))}
	if sem.ClassifiedAs == nil {
		sem.ClassifiedAs = []string{}
	}
	b, _ := json.Marshal(sem)
	v := jsontext.Value(b)
	_ = v.Canonicalize()
	sum := sha256.Sum256(v)
	return hex.EncodeToString(sum[:])
}

func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// checkGovernance checks protected invariants within one catalog: a
// protected rule is never waivable, no exception targets it, and only a
// protected rule may supersede it.
func checkGovernance(p *problems, records map[string]*Record) {
	for _, k := range sortedRecordKeys(records) {
		r := records[k]
		field := "records[" + k + "]"
		switch r.Type {
		case TypeRule:
			if r.Protected && r.Waivable {
				p.add(field+".waivable", "a protected invariant is never waivable; change it by a new revision instead")
			}
		case TypeException:
			if r.RuleDigest != "" && !validDigest(r.RuleDigest) {
				p.add(field+".rule_digest", "must be 64 lowercase hexadecimal digits")
			}
			for _, rk := range r.Excepts {
				if t := records[rk]; t != nil && t.Protected {
					p.add(field+".excepts", "%s is a protected invariant; no exception may target it", rk)
				}
			}
		}
		for _, old := range r.Supersedes {
			if t := records[old]; t != nil && t.Type == TypeRule && t.Protected && !r.Protected {
				p.add(field+".supersedes", "%s is a protected invariant; only a protected rule may supersede it", old)
			}
		}
	}
}

// CheckTransition checks what may change from one published catalog to
// the next (ADR-0050): a protected rule is never removed or unprotected, a
// rule whose semantics change takes a higher revision, and a revision
// never decreases. Nothing is exempt, rollbacks included: a rollback
// restores earlier semantics under a new revision (Kernel.Rollback).
func CheckTransition(prev, next *Catalog) error {
	var p problems
	for _, k := range prev.recordKeys {
		old := prev.records[k]
		if old.Type != TypeRule {
			continue
		}
		cur, ok := next.records[k]
		field := "records[" + k + "]"
		switch {
		case !ok || cur.Type != TypeRule:
			if old.Protected {
				p.add(field, "is a protected invariant and cannot be removed")
			}
		case old.Protected && !cur.Protected:
			p.add(field+".protected", "a protected invariant cannot be unprotected")
		case cur.Revision < old.Revision:
			p.add(field+".revision", "is %d, below the published revision %d", cur.Revision, old.Revision)
		case RuleDigest(cur) != RuleDigest(old) && cur.Revision == old.Revision:
			p.add(field+".revision", "the rule's semantics changed, so its revision must rise above %d", old.Revision)
		}
	}
	return p.err()
}
