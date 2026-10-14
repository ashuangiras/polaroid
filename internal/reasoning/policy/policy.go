// Package policy decides which normative exceptions apply (ADR-0050). It is
// pure: it verifies Ed25519 approvals with the standard library against
// operator-held trust material, checks authority, scope, validity and
// revocation, and reports conflicts. It never infers anything and never
// changes a fact: an exception only changes an obligation's derivation.
package policy

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ashuangiras/polaroid/internal/knowledge"
)

// Purpose binds a signature to this use, so it cannot be replayed as the
// approval of anything else.
const Purpose = "polaroid.corpus.exception.approval/v1"

type payloadCondition struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type payloadAuthority struct {
	Level      string `json:"level"`
	Domain     string `json:"domain,omitzero"`
	Repository string `json:"repository,omitzero"`
}

type payload struct {
	Purpose             string             `json:"purpose"`
	Exception           string             `json:"exception"`
	Rule                string             `json:"rule"`
	Revision            int                `json:"revision"`
	RuleDigest          string             `json:"rule_digest"`
	AppliesTo           []string           `json:"applies_to"`
	Conditions          []payloadCondition `json:"conditions"`
	Waives              string             `json:"waives"`
	Replacement         string             `json:"replacement,omitzero"`
	JustificationSHA256 string             `json:"justification_sha256"`
	Authority           payloadAuthority   `json:"authority"`
	Reference           string             `json:"reference"`
	Key                 string             `json:"key"`
	ValidFrom           string             `json:"valid_from"`
	ValidUntil          string             `json:"valid_until"`
}

// Payload returns the exact bytes an exception's approval signs: the RFC
// 8785 canonical JSON of its purpose, identity, target rule, revision and
// rule digest (the SHA-256 of the rule's semantics, knowledge.RuleDigest),
// scope, conditions, effect, the SHA-256 of its justification, authority,
// approval reference, key and validity period. Every member that decides
// whether or how the exception applies is signed.
func Payload(r *knowledge.Record) ([]byte, error) {
	if r.Type != knowledge.TypeException || r.Authority == nil || r.Approval == nil || len(r.Excepts) != 1 {
		return nil, errors.New("not a complete exception record")
	}
	sum := sha256.Sum256([]byte(r.Justification))
	p := payload{
		Purpose: Purpose, Exception: r.Key, Rule: r.Excepts[0], Revision: r.Revision, RuleDigest: r.RuleDigest,
		AppliesTo: slices.Sorted(slices.Values(r.AppliesTo)), Conditions: []payloadCondition{},
		Waives: r.Waives, Replacement: r.Replacement, JustificationSHA256: hex.EncodeToString(sum[:]),
		Authority: payloadAuthority{Level: string(r.Authority.Level), Domain: r.Authority.Domain, Repository: r.Authority.Repository},
		Reference: r.Approval.Reference, Key: r.Approval.Key,
		ValidFrom: r.Approval.ValidFrom.UTC().Format(time.RFC3339), ValidUntil: r.Approval.ValidUntil.UTC().Format(time.RFC3339),
	}
	for _, c := range r.Conditions {
		p.Conditions = append(p.Conditions, payloadCondition(c))
	}
	slices.SortFunc(p.Conditions, func(a, b payloadCondition) int { return strings.Compare(a.Name, b.Name) })
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	v := jsontext.Value(b)
	if err := v.Canonicalize(); err != nil {
		return nil, err
	}
	return v, nil
}

// Sign returns the approval signature of r by priv; operators use it to
// approve an exception, tests to build fixtures.
func Sign(priv ed25519.PrivateKey, r *knowledge.Record) ([]byte, error) {
	p, err := Payload(r)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(priv, p), nil
}

// Key is one trusted approval key and the single authority it holds.
type Key struct {
	ID        string
	Public    ed25519.PublicKey
	Authority knowledge.Authority
}

// Trust is operator-held authorisation material: never read from the
// knowledge it authorises, and never derived from Git authorship.
type Trust struct {
	Keys map[string]Key
	// Revoked approval references and key ids.
	Revoked map[string]bool
}

type trustFile struct {
	Keys []struct {
		ID        string `json:"id"`
		PublicKey string `json:"public_key"`
		Authority struct {
			Level      string `json:"level"`
			Domain     string `json:"domain,omitzero"`
			Repository string `json:"repository,omitzero"`
		} `json:"authority"`
	} `json:"keys"`
	Revoked []string `json:"revoked"`
}

// ParseTrust reads trust material:
// {"keys":[{"id","public_key":"<base64>","authority":{"level",...}}],"revoked":[...]}.
func ParseTrust(b []byte) (Trust, error) {
	var f trustFile
	if err := json.Unmarshal(b, &f, json.RejectUnknownMembers(true)); err != nil {
		return Trust{}, fmt.Errorf("trust material: %w", err)
	}
	t := Trust{Keys: map[string]Key{}, Revoked: map[string]bool{}}
	for i, k := range f.Keys {
		pub, err := base64.StdEncoding.DecodeString(k.PublicKey)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return Trust{}, fmt.Errorf("trust material: keys[%d].public_key must be a base64 Ed25519 public key", i)
		}
		if k.ID == "" || t.Keys[k.ID].ID != "" {
			return Trust{}, fmt.Errorf("trust material: keys[%d].id must be unique and non-empty", i)
		}
		lvl := knowledge.AuthorityLevel(k.Authority.Level)
		if !slices.Contains([]knowledge.AuthorityLevel{knowledge.AuthorityCorpus, knowledge.AuthorityDomain, knowledge.AuthorityProject}, lvl) {
			return Trust{}, fmt.Errorf("trust material: keys[%d].authority.level must be corpus, domain or project", i)
		}
		t.Keys[k.ID] = Key{ID: k.ID, Public: pub, Authority: knowledge.Authority{Level: lvl, Domain: k.Authority.Domain, Repository: k.Authority.Repository}}
	}
	for _, r := range f.Revoked {
		t.Revoked[r] = true
	}
	return t, nil
}

// Rule is what policy needs to know about an excepted rule.
type Rule struct {
	Key       string
	Revision  int
	Digest    string // knowledge.RuleDigest
	Protected bool
	Waivable  bool
	// Domains are the rule's domains and every broader domain.
	Domains []string
}

// Derivation is one way an obligation was established.
type Derivation struct {
	Subject     string
	Requirement string
	Source      string // the rule, or the record that declares the requirement
	Via         string // the record the obligation is inherited from; Subject when its own
}

// Request is the resolution context the exceptions are judged in. Context
// holds what the caller asserts; Trusted holds only what the server
// established itself. An exception's project scope and conditions are
// judged on Trusted alone: an assertion never authorises a waiver.
type Request struct {
	Context map[string]string
	Trusted map[string]string
	At      time.Time
}

// Status of one exception in a resolution.
const (
	Applied       = "applied"
	NotApplicable = "not_applicable"
	Rejected      = "rejected"
	Conflicting   = "conflicting"
)

// Verdict is the outcome for one exception.
type Verdict struct {
	Exception   string
	Status      string
	Reason      string
	Derivations []int // indexes of the derivations it targets
}

// Effect is what applies to one derivation.
type Effect struct {
	Exception   string
	Replacement string // "" waives
}

// Outcome is the policy decision for one resolution.
type Outcome struct {
	Verdicts []Verdict
	Effects  map[int]Effect
}

// Decide judges every exception against the derivations. An exception that
// fails any check never applies; two valid exceptions with different
// effects on one derivation both become conflicting and neither applies.
func Decide(trust Trust, exceptions []*knowledge.Record, rules map[string]Rule, derivs []Derivation, req Request) Outcome {
	out := Outcome{Effects: map[int]Effect{}}
	candidates := map[int][]int{} // derivation -> verdict indexes
	for _, e := range exceptions {
		v := judge(trust, e, rules, derivs, req)
		out.Verdicts = append(out.Verdicts, v)
		if v.Status == Applied {
			for _, d := range v.Derivations {
				candidates[d] = append(candidates[d], len(out.Verdicts)-1)
			}
		}
	}
	for _, vs := range candidates {
		effects := map[string]bool{}
		for _, i := range vs {
			effects[exceptionByKey(exceptions, out.Verdicts[i].Exception).Replacement] = true
		}
		if len(effects) > 1 {
			for _, i := range vs {
				out.Verdicts[i].Status = Conflicting
				out.Verdicts[i].Reason = "another valid exception has a different effect on the same derivation"
			}
		}
	}
	for d, vs := range candidates {
		var keys []string
		for _, i := range vs {
			if out.Verdicts[i].Status == Applied {
				keys = append(keys, out.Verdicts[i].Exception)
			}
		}
		if len(keys) > 0 {
			slices.Sort(keys)
			out.Effects[d] = Effect{Exception: strings.Join(keys, ","), Replacement: exceptionByKey(exceptions, keys[0]).Replacement}
		}
	}
	return out
}

func exceptionByKey(es []*knowledge.Record, key string) *knowledge.Record {
	for _, e := range es {
		if e.Key == key {
			return e
		}
	}
	return nil
}

func judge(trust Trust, e *knowledge.Record, rules map[string]Rule, derivs []Derivation, req Request) Verdict {
	v := Verdict{Exception: e.Key, Status: Rejected}
	reject := func(format string, args ...any) Verdict { v.Reason = fmt.Sprintf(format, args...); return v }
	if e.Approval == nil || e.Authority == nil || len(e.Excepts) != 1 {
		return reject("incomplete exception")
	}
	key, ok := trust.Keys[e.Approval.Key]
	switch {
	case !ok:
		return reject("key %q is not trusted", e.Approval.Key)
	case trust.Revoked[key.ID]:
		return reject("key %q is revoked", key.ID)
	case trust.Revoked[e.Approval.Reference]:
		return reject("approval %q is revoked", e.Approval.Reference)
	case key.Authority != *e.Authority:
		return reject("key %q holds %s authority, not the %s authority the exception declares", key.ID, describe(key.Authority), describe(*e.Authority))
	}
	p, err := Payload(e)
	if err != nil || !ed25519.Verify(key.Public, p, e.Approval.Signature) {
		return reject("the approval signature does not verify")
	}
	switch {
	case req.At.Before(e.Approval.ValidFrom):
		return reject("not valid before %s", e.Approval.ValidFrom.UTC().Format(time.RFC3339))
	case !req.At.Before(e.Approval.ValidUntil):
		return reject("expired at %s", e.Approval.ValidUntil.UTC().Format(time.RFC3339))
	}
	rule, ok := rules[e.Excepts[0]]
	switch {
	case !ok:
		return reject("rule %s is not in the snapshot", e.Excepts[0])
	case rule.Revision != e.Revision:
		return reject("approved for revision %d of %s, which is now at revision %d", e.Revision, rule.Key, rule.Revision)
	case rule.Digest != e.RuleDigest:
		return reject("approved for different semantics of %s (rule digest %s, now %s)", rule.Key, short(e.RuleDigest), short(rule.Digest))
	case rule.Protected:
		return reject("%s is a protected invariant: no exception may waive it, at any authority; change it by a governed revision", rule.Key)
	case !rule.Waivable:
		return reject("%s is not waivable", rule.Key)
	case e.Authority.Level == knowledge.AuthorityDomain && !slices.Contains(rule.Domains, e.Authority.Domain):
		return reject("%s is not in domain %s", rule.Key, e.Authority.Domain)
	}
	v.Status = NotApplicable
	if e.Authority.Level == knowledge.AuthorityProject && req.Trusted["repository"] != e.Authority.Repository {
		v.Reason = fmt.Sprintf("granted for repository %s, which this request has not established by trusted context", e.Authority.Repository)
		if req.Context["repository"] == e.Authority.Repository {
			v.Reason += "; a caller's assertion of its repository does not authorise a waiver"
		}
		return v
	}
	for _, c := range e.Conditions {
		if got, ok := req.Trusted[c.Name]; !ok || got != c.Value {
			v.Reason = fmt.Sprintf("requires trusted context %s = %q", c.Name, c.Value)
			if req.Context[c.Name] == c.Value {
				v.Reason += "; a caller's assertion does not authorise a waiver"
			}
			return v
		}
	}
	for i, d := range derivs {
		if d.Source == rule.Key && d.Requirement == e.Waives && (slices.Contains(e.AppliesTo, d.Subject) || slices.Contains(e.AppliesTo, d.Via)) {
			v.Derivations = append(v.Derivations, i)
		}
	}
	if len(v.Derivations) == 0 {
		v.Reason = fmt.Sprintf("%s derives no %s obligation for %v here", rule.Key, e.Waives, e.AppliesTo)
		return v
	}
	v.Status, v.Reason = Applied, ""
	return v
}

func short(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

func describe(a knowledge.Authority) string {
	var b bytes.Buffer
	b.WriteString(string(a.Level))
	if a.Domain != "" {
		b.WriteString(" " + a.Domain)
	}
	if a.Repository != "" {
		b.WriteString(" " + a.Repository)
	}
	return b.String()
}
