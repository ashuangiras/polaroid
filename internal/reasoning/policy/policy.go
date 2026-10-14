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
	"maps"
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
	Level      string   `json:"level"`
	Domain     string   `json:"domain,omitzero"`
	Repository string   `json:"repository,omitzero"`
	Path       []string `json:"path,omitzero"`
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
		Authority: payloadAuthority{Level: string(r.Authority.Level), Domain: r.Authority.Domain, Repository: r.Authority.Repository, Path: r.Authority.Path},
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

// Key is one trusted key. It holds exactly one role: an approval key holds
// one Authority over exceptions; an attestation key (Attests set) signs
// context assertions for one repository and nothing else.
type Key struct {
	ID        string
	Public    ed25519.PublicKey
	Authority knowledge.Authority
	Attests   *Attestation
}

// Attestation is what an attestation key may assert: facts about one
// repository, under the listed context names.
type Attestation struct {
	Repository string
	Names      []string
}

// Trust is operator-held authorisation material: never read from the
// knowledge it authorises, and never derived from Git authorship.
type Trust struct {
	Keys map[string]Key
	// Revoked approval and assertion references, and key ids.
	Revoked map[string]bool
}

type trustFile struct {
	Keys []struct {
		ID        string `json:"id"`
		PublicKey string `json:"public_key"`
		Authority *struct {
			Level      string `json:"level"`
			Domain     string `json:"domain,omitzero"`
			Repository string `json:"repository,omitzero"`
		} `json:"authority,omitzero"`
		Attests *struct {
			Repository string   `json:"repository"`
			Names      []string `json:"names"`
		} `json:"attests,omitzero"`
	} `json:"keys"`
	Revoked []string `json:"revoked"`
}

// ParseTrust reads trust material: {"keys":[{"id","public_key":"<base64>",
// "authority":{"level",...}} or {"id","public_key","attests":{"repository",
// "names":[...]}}],"revoked":[...]}.
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
		key := Key{ID: k.ID, Public: pub}
		switch {
		case (k.Authority == nil) == (k.Attests == nil):
			return Trust{}, fmt.Errorf("trust material: keys[%d] must hold exactly one of authority and attests", i)
		case k.Attests != nil:
			if k.Attests.Repository == "" || slices.Contains(k.Attests.Names, "repository") || slices.Contains(k.Attests.Names, "") {
				return Trust{}, fmt.Errorf("trust material: keys[%d].attests names a repository and context names other than repository", i)
			}
			key.Attests = &Attestation{Repository: k.Attests.Repository, Names: slices.Clone(k.Attests.Names)}
		default:
			lvl := knowledge.AuthorityLevel(k.Authority.Level)
			if !slices.Contains([]knowledge.AuthorityLevel{knowledge.AuthorityCorpus, knowledge.AuthorityDomain, knowledge.AuthorityProject}, lvl) {
				return Trust{}, fmt.Errorf("trust material: keys[%d].authority.level must be corpus, domain or project", i)
			}
			key.Authority = knowledge.Authority{Level: lvl, Domain: k.Authority.Domain, Repository: k.Authority.Repository}
		}
		t.Keys[k.ID] = key
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
	// Classified are the domains the rule is classified in directly.
	Classified []string
}

// Derivation is one way an obligation was established.
type Derivation struct {
	Subject     string
	Requirement string
	Source      string // the rule, or the record that declares the requirement
	Via         string // the record the obligation is inherited from; Subject when its own
}

// Request is the resolution context the exceptions are judged in. Context
// holds what the caller asserts; Trusted holds only what was established
// independently (Establish). An exception's project scope and conditions
// are judged on Trusted alone: an assertion never authorises a waiver.
// Broader is the snapshot's domain hierarchy, domain to broader domains.
type Request struct {
	Context map[string]string
	Trusted map[string]string
	Broader map[string][]string
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
	case key.Attests != nil || key.Authority.Level != e.Authority.Level || key.Authority.Domain != e.Authority.Domain ||
		key.Authority.Repository != e.Authority.Repository:
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
	case e.Authority.Level == knowledge.AuthorityDomain:
		if why := pathProblem(e.Authority, rule, req.Broader); why != "" {
			return reject("approved by domain %s through the path %v, which this snapshot does not hold (%s); a changed hierarchy needs a new approval",
				e.Authority.Domain, e.Authority.Path, why)
		}
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

// pathProblem says why a domain approval's signed path does not hold for
// rule in this snapshot's hierarchy, or "" when it holds: it starts at a
// domain the rule is classified in, each next domain is directly broader
// than the one before, and it ends at the approving domain.
func pathProblem(a *knowledge.Authority, rule Rule, broader map[string][]string) string {
	path := a.Path
	switch {
	case len(path) == 0:
		return "no path was approved"
	case path[len(path)-1] != a.Domain:
		return "the path does not end at the approving domain"
	case !slices.Contains(rule.Classified, path[0]):
		return fmt.Sprintf("%s is not classified in %s", rule.Key, path[0])
	}
	for i := 1; i < len(path); i++ {
		if !slices.Contains(broader[path[i-1]], path[i]) {
			return fmt.Sprintf("%s is not directly broader than %s", path[i], path[i-1])
		}
	}
	return ""
}

// AssertionPurpose binds an assertion signature to this use.
const AssertionPurpose = "polaroid.corpus.context.assertion/v1"

// ErrAssertion is a context assertion that cannot be trusted.
var ErrAssertion = errors.New("context assertion refused")

// Assertion is a signed statement, by an attestation key, of facts about
// one repository during a validity period. It is how trusted context is
// established without trusting the caller (ADR-0050).
type Assertion struct {
	Reference  string
	Key        string
	Repository string
	Facts      map[string]string
	ValidFrom  time.Time
	ValidUntil time.Time
	Signature  []byte
}

type assertionPayload struct {
	Purpose    string             `json:"purpose"`
	Reference  string             `json:"reference"`
	Key        string             `json:"key"`
	Repository string             `json:"repository"`
	Facts      []payloadCondition `json:"facts"`
	ValidFrom  string             `json:"valid_from"`
	ValidUntil string             `json:"valid_until"`
}

// AssertionPayload returns the exact bytes an assertion's signature signs:
// the RFC 8785 canonical JSON of its purpose, reference, key, repository,
// facts sorted by name, and validity period (RFC 3339 UTC, to the second).
func AssertionPayload(a Assertion) ([]byte, error) {
	p := assertionPayload{Purpose: AssertionPurpose, Reference: a.Reference, Key: a.Key, Repository: a.Repository, Facts: []payloadCondition{},
		ValidFrom: a.ValidFrom.UTC().Format(time.RFC3339), ValidUntil: a.ValidUntil.UTC().Format(time.RFC3339)}
	for _, n := range slices.Sorted(maps.Keys(a.Facts)) {
		p.Facts = append(p.Facts, payloadCondition{Name: n, Value: a.Facts[n]})
	}
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

// SignAssertion returns the signature of a by priv.
func SignAssertion(priv ed25519.PrivateKey, a Assertion) ([]byte, error) {
	p, err := AssertionPayload(a)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(priv, p), nil
}

// Establish verifies context assertions at time at and returns the trusted
// context they establish: their facts and "repository". Every assertion
// must verify with a trusted, unrevoked attestation key for its repository
// and names, be valid at at, and agree with the others on the repository
// and on every fact; otherwise nothing is established (ErrAssertion).
func Establish(trust Trust, as []Assertion, at time.Time) (map[string]string, error) {
	if len(as) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for i, a := range as {
		fail := func(format string, args ...any) (map[string]string, error) {
			return nil, fmt.Errorf("%w: assertions[%d]: %s", ErrAssertion, i, fmt.Sprintf(format, args...))
		}
		key, ok := trust.Keys[a.Key]
		switch {
		case !ok:
			return fail("key %q is not trusted", a.Key)
		case key.Attests == nil:
			return fail("key %q is not an attestation key", a.Key)
		case trust.Revoked[key.ID] || trust.Revoked[a.Reference]:
			return fail("key %q or reference %q is revoked", a.Key, a.Reference)
		case a.Reference == "" || a.Repository == "":
			return fail("names no reference or no repository")
		case a.Repository != key.Attests.Repository:
			return fail("key %q attests only for %s, not %s", a.Key, key.Attests.Repository, a.Repository)
		}
		for n := range a.Facts {
			if !slices.Contains(key.Attests.Names, n) {
				return fail("key %q may not attest %q", a.Key, n)
			}
		}
		p, err := AssertionPayload(a)
		if err != nil || !ed25519.Verify(key.Public, p, a.Signature) {
			return fail("the signature does not verify")
		}
		switch {
		case !a.ValidFrom.Before(a.ValidUntil):
			return fail("its validity period is empty")
		case at.Before(a.ValidFrom):
			return fail("not valid before %s", a.ValidFrom.UTC().Format(time.RFC3339))
		case !at.Before(a.ValidUntil):
			return fail("expired at %s", a.ValidUntil.UTC().Format(time.RFC3339))
		}
		facts := maps.Clone(a.Facts)
		if facts == nil {
			facts = map[string]string{}
		}
		facts["repository"] = a.Repository
		for n, v := range facts {
			if old, seen := out[n]; seen && old != v {
				return fail("%s = %q contradicts %q from an earlier assertion", n, v, old)
			}
			out[n] = v
		}
	}
	return out, nil
}
