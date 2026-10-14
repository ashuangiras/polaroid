package knowledge

import (
	"bytes"
	"cmp"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"
)

// RecordType is the kind of a typed knowledge record (ADR-0050).
type RecordType string

const (
	TypeDomain    RecordType = "Domain"
	TypeConcept   RecordType = "Concept"
	TypeClaim     RecordType = "Claim"
	TypeRule      RecordType = "Rule"
	TypeProcedure RecordType = "Procedure"
	TypeEvidence  RecordType = "Evidence"
	TypeException RecordType = "Exception"
)

// RecordTypes lists every record type, in path order.
var RecordTypes = []RecordType{TypeClaim, TypeConcept, TypeDomain, TypeEvidence, TypeException, TypeProcedure, TypeRule}

// ContextIRI is the one JSON-LD context a record may name. Its definition is
// bundled with the semantic validator and never fetched.
const ContextIRI = "urn:polaroid:context:v1"

// IRIPrefix is the compact-IRI prefix of record identities in documents:
// "k:<key>" expands to "urn:polaroid:k:<key>".
const IRIPrefix = "k:"

const (
	recordsDir      = "records"
	recordExt       = ".jsonld"
	maxSetMembers   = 256
	maxProgramBytes = 16 << 10
)

// RecordPath is where a record of type t lives in a snapshot.
func RecordPath(t RecordType, key string) string {
	first := key
	if i := strings.IndexAny(key, "._-"); i > 0 {
		first = key[:i]
	}
	return recordsDir + "/" + strings.ToLower(string(t)) + "/" + first + "/" + key + recordExt
}

// recordKeyOf is the inverse of RecordPath; ok is false for any other path.
func recordKeyOf(path string) (RecordType, string, bool) {
	rest, ok := strings.CutPrefix(path, recordsDir+"/")
	if !ok {
		return "", "", false
	}
	dir, rest, ok := strings.Cut(rest, "/")
	if !ok {
		return "", "", false
	}
	var t RecordType
	for _, rt := range RecordTypes {
		if strings.ToLower(string(rt)) == dir {
			t = rt
		}
	}
	file := rest[strings.LastIndex(rest, "/")+1:]
	key, isDoc := strings.CutSuffix(file, recordExt)
	if t == "" || !isDoc || !validKey(key) || RecordPath(t, key) != path {
		return "", "", false
	}
	return t, key, true
}

// Record is one typed knowledge record. Which members a type admits is
// fixed by checkRecord; relationships hold record keys.
type Record struct {
	Type         RecordType
	Key          string
	Label        string
	Definition   string
	Statement    string
	ClassifiedAs []string
	Broader      []string
	Specializes  []string
	Uses         []string
	Composes     []string
	Requires     []string
	Supports     []string
	Contradicts  []string
	Supersedes   []string
	Asserts      []Assertion
	Source       string

	// Rule
	Program   string
	Revision  int
	Protected bool
	Waivable  bool

	// Procedure semantics
	Inputs             []Port
	Outputs            []Port
	OutputsComplete    bool
	Preconditions      []string
	Postconditions     []string
	Invariants         []string
	InformalConditions []string

	// Exception
	Excepts       []string
	RuleDigest    string
	AppliesTo     []string
	Conditions    []Condition
	Waives        string
	Replacement   string
	Justification string
	Authority     *Authority
	Approval      *Approval
}

// Assertion is a claim's formal statement: one domain-independent triple.
type Assertion struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

// Port is a typed procedure input or output; Concept is empty when the port
// is described only informally.
type Port struct {
	Name    string
	Concept string
}

// Condition requires the resolution context's Name to equal Value.
type Condition struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// AuthorityLevel orders who may grant an exception.
type AuthorityLevel string

const (
	AuthorityCorpus  AuthorityLevel = "corpus"
	AuthorityDomain  AuthorityLevel = "domain"
	AuthorityProject AuthorityLevel = "project"
)

// Authority is who governs an exception: the whole corpus, one domain (and
// its narrower domains), or one project repository. A domain authority's
// Path is the chain of domains its approval covers, in order: a domain the
// rule is classified in, each next one broader than the one before, ending
// at Domain. The approval signs it, so a changed hierarchy never silently
// widens or keeps an approval (ADR-0050).
type Authority struct {
	Level      AuthorityLevel
	Domain     string
	Repository string
	Path       []string
}

// Approval is the signed approval of an exception (ADR-0050): an Ed25519
// signature by the key named Key over the exception's approval payload.
type Approval struct {
	Reference  string
	Key        string
	ValidFrom  time.Time
	ValidUntil time.Time
	Signature  []byte
}

type recordDoc struct {
	Context            string        `json:"@context"`
	ID                 string        `json:"id"`
	Type               string        `json:"type"`
	Label              string        `json:"label"`
	Definition         string        `json:"definition,omitzero"`
	Statement          string        `json:"statement,omitzero"`
	ClassifiedAs       []string      `json:"classified_as,omitzero"`
	Broader            []string      `json:"broader,omitzero"`
	Specializes        []string      `json:"specializes,omitzero"`
	Uses               []string      `json:"uses,omitzero"`
	Composes           []string      `json:"composes,omitzero"`
	Requires           []string      `json:"requires,omitzero"`
	Supports           []string      `json:"supports,omitzero"`
	Contradicts        []string      `json:"contradicts,omitzero"`
	Supersedes         []string      `json:"supersedes,omitzero"`
	Asserts            []Assertion   `json:"asserts,omitzero"`
	Source             string        `json:"source,omitzero"`
	Program            string        `json:"program,omitzero"`
	Revision           int           `json:"revision,omitzero"`
	Protected          bool          `json:"protected,omitzero"`
	Waivable           bool          `json:"waivable,omitzero"`
	Inputs             []portDoc     `json:"inputs,omitzero"`
	Outputs            []portDoc     `json:"outputs,omitzero"`
	OutputsComplete    bool          `json:"outputs_complete,omitzero"`
	Preconditions      []string      `json:"preconditions,omitzero"`
	Postconditions     []string      `json:"postconditions,omitzero"`
	Invariants         []string      `json:"invariants,omitzero"`
	InformalConditions []string      `json:"informal_conditions,omitzero"`
	Excepts            []string      `json:"excepts,omitzero"`
	RuleDigest         string        `json:"rule_digest,omitzero"`
	AppliesTo          []string      `json:"applies_to,omitzero"`
	Conditions         []Condition   `json:"conditions,omitzero"`
	Waives             string        `json:"waives,omitzero"`
	Replacement        string        `json:"replacement,omitzero"`
	Justification      string        `json:"justification,omitzero"`
	Authority          *authorityDoc `json:"authority,omitzero"`
	Approval           *approvalDoc  `json:"approval,omitzero"`
}

type portDoc struct {
	Name    string `json:"name"`
	Concept string `json:"concept,omitzero"`
}

type authorityDoc struct {
	Level      string   `json:"level"`
	Domain     string   `json:"domain,omitzero"`
	Repository string   `json:"repository,omitzero"`
	Path       []string `json:"path,omitzero"`
}

type approvalDoc struct {
	Reference  string `json:"reference"`
	Key        string `json:"key"`
	ValidFrom  string `json:"valid_from"`
	ValidUntil string `json:"valid_until"`
	Signature  string `json:"signature"`
}

func ids(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = IRIPrefix + k
	}
	return out
}

func id(key string) string {
	if key == "" {
		return ""
	}
	return IRIPrefix + key
}

func ports(ps []Port) []portDoc {
	var out []portDoc
	for _, p := range ps {
		out = append(out, portDoc{Name: p.Name, Concept: id(p.Concept)})
	}
	return out
}

// EncodeRecord returns the canonical document of r: compact JSON-LD naming
// ContextIRI, members in a fixed order, every set sorted, one trailing
// newline. Equal records encode to equal bytes.
func EncodeRecord(r *Record) ([]byte, error) {
	n := normalized(r)
	d := recordDoc{
		Context: ContextIRI, ID: id(n.Key), Type: string(n.Type), Label: n.Label, Definition: n.Definition, Statement: n.Statement,
		ClassifiedAs: ids(n.ClassifiedAs), Broader: ids(n.Broader), Specializes: ids(n.Specializes), Uses: ids(n.Uses),
		Composes: ids(n.Composes), Requires: ids(n.Requires), Supports: ids(n.Supports), Contradicts: ids(n.Contradicts),
		Supersedes: ids(n.Supersedes), Asserts: n.Asserts, Source: n.Source,
		Program: n.Program, Revision: n.Revision, Protected: n.Protected, Waivable: n.Waivable,
		Inputs: ports(n.Inputs), Outputs: ports(n.Outputs), OutputsComplete: n.OutputsComplete,
		Preconditions: ids(n.Preconditions), Postconditions: ids(n.Postconditions), Invariants: ids(n.Invariants),
		InformalConditions: n.InformalConditions,
		Excepts:            ids(n.Excepts), RuleDigest: n.RuleDigest, AppliesTo: ids(n.AppliesTo), Conditions: n.Conditions,
		Waives: id(n.Waives), Replacement: id(n.Replacement), Justification: n.Justification,
	}
	if a := n.Authority; a != nil {
		d.Authority = &authorityDoc{Level: string(a.Level), Domain: id(a.Domain), Repository: a.Repository, Path: ids(a.Path)}
	}
	if a := n.Approval; a != nil {
		d.Approval = &approvalDoc{Reference: a.Reference, Key: a.Key,
			ValidFrom: a.ValidFrom.UTC().Format(time.RFC3339), ValidUntil: a.ValidUntil.UTC().Format(time.RFC3339),
			Signature: base64.StdEncoding.EncodeToString(a.Signature)}
	}
	b, err := json.Marshal(&d)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// normalized returns a copy of r with every set sorted and deduplicated, so
// that the canonical encoding does not depend on the order a caller chose.
func normalized(r *Record) *Record {
	n := r.Clone()
	for _, s := range []*[]string{&n.ClassifiedAs, &n.Broader, &n.Specializes, &n.Uses, &n.Composes, &n.Requires,
		&n.Supports, &n.Contradicts, &n.Supersedes, &n.Preconditions, &n.Postconditions, &n.Invariants,
		&n.InformalConditions, &n.Excepts, &n.AppliesTo} {
		slices.Sort(*s)
		*s = slices.Compact(*s)
	}
	slices.SortFunc(n.Asserts, func(a, b Assertion) int {
		return cmp.Or(cmp.Compare(a.Subject, b.Subject), cmp.Compare(a.Predicate, b.Predicate), cmp.Compare(a.Object, b.Object))
	})
	n.Asserts = slices.Compact(n.Asserts)
	byName := func(a, b Port) int { return cmp.Compare(a.Name, b.Name) }
	slices.SortStableFunc(n.Inputs, byName)
	slices.SortStableFunc(n.Outputs, byName)
	slices.SortStableFunc(n.Conditions, func(a, b Condition) int { return cmp.Compare(a.Name, b.Name) })
	return n
}

// Clone returns a deep copy of r.
func (r *Record) Clone() *Record {
	c := *r
	for _, s := range []*[]string{&c.ClassifiedAs, &c.Broader, &c.Specializes, &c.Uses, &c.Composes, &c.Requires,
		&c.Supports, &c.Contradicts, &c.Supersedes, &c.Preconditions, &c.Postconditions, &c.Invariants,
		&c.InformalConditions, &c.Excepts, &c.AppliesTo} {
		*s = slices.Clone(*s)
	}
	c.Asserts, c.Inputs, c.Outputs, c.Conditions = slices.Clone(r.Asserts), slices.Clone(r.Inputs), slices.Clone(r.Outputs), slices.Clone(r.Conditions)
	if r.Authority != nil {
		a := *r.Authority
		a.Path = slices.Clone(r.Authority.Path)
		c.Authority = &a
	}
	if r.Approval != nil {
		a := *r.Approval
		a.Signature = slices.Clone(r.Approval.Signature)
		c.Approval = &a
	}
	return &c
}

func unid(field string, v string, p *problems) string {
	if v == "" {
		return ""
	}
	key, ok := strings.CutPrefix(v, IRIPrefix)
	if !ok || !validKey(key) {
		p.add(field, "must be %q followed by a key, not %q", IRIPrefix, v)
		return ""
	}
	return key
}

func unids(field string, vs []string, p *problems) []string {
	var out []string
	for i, v := range vs {
		out = append(out, unid(fmt.Sprintf("%s[%d]", field, i), v, p))
	}
	return out
}

// DecodeRecord parses a stored record strictly: unknown or duplicate
// members, trailing data and non-canonical bytes are refused, so a stored
// record has exactly one spelling. It does not check the record's content;
// checkRecord and the semantic validator do.
func DecodeRecord(b []byte) (*Record, error) {
	r, err := ParseRecord(b)
	if err != nil {
		return nil, err
	}
	again, err := EncodeRecord(r)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(again, b) {
		return nil, fmt.Errorf("document is not in canonical form")
	}
	return r, nil
}

// ParseRecord parses a record document as an operator submits it: unknown
// or duplicate members are refused, but its layout and set order are free;
// publication stores its canonical encoding.
func ParseRecord(b []byte) (*Record, error) {
	if len(b) > maxDocumentBytes {
		return nil, fmt.Errorf("document of %d bytes exceeds %d", len(b), maxDocumentBytes)
	}
	var d recordDoc
	if err := json.Unmarshal(b, &d, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if d.Context != ContextIRI {
		return nil, fmt.Errorf("@context must be %q", ContextIRI)
	}
	var p problems
	r := &Record{
		Type: RecordType(d.Type), Key: unid("id", d.ID, &p), Label: d.Label, Definition: d.Definition, Statement: d.Statement,
		ClassifiedAs: unids("classified_as", d.ClassifiedAs, &p), Broader: unids("broader", d.Broader, &p),
		Specializes: unids("specializes", d.Specializes, &p), Uses: unids("uses", d.Uses, &p),
		Composes: unids("composes", d.Composes, &p), Requires: unids("requires", d.Requires, &p),
		Supports: unids("supports", d.Supports, &p), Contradicts: unids("contradicts", d.Contradicts, &p),
		Supersedes: unids("supersedes", d.Supersedes, &p), Asserts: d.Asserts, Source: d.Source,
		Program: d.Program, Revision: d.Revision, Protected: d.Protected, Waivable: d.Waivable,
		OutputsComplete: d.OutputsComplete, Preconditions: unids("preconditions", d.Preconditions, &p),
		Postconditions: unids("postconditions", d.Postconditions, &p), Invariants: unids("invariants", d.Invariants, &p),
		InformalConditions: d.InformalConditions, Excepts: unids("excepts", d.Excepts, &p), RuleDigest: d.RuleDigest,
		AppliesTo: unids("applies_to", d.AppliesTo, &p), Conditions: d.Conditions,
		Waives: unid("waives", d.Waives, &p), Replacement: unid("replacement", d.Replacement, &p), Justification: d.Justification,
	}
	if d.ID == "" {
		p.add("id", "is required")
	}
	for i, port := range d.Inputs {
		r.Inputs = append(r.Inputs, Port{Name: port.Name, Concept: unid(fmt.Sprintf("inputs[%d].concept", i), port.Concept, &p)})
	}
	for i, port := range d.Outputs {
		r.Outputs = append(r.Outputs, Port{Name: port.Name, Concept: unid(fmt.Sprintf("outputs[%d].concept", i), port.Concept, &p)})
	}
	if a := d.Authority; a != nil {
		r.Authority = &Authority{Level: AuthorityLevel(a.Level), Domain: unid("authority.domain", a.Domain, &p), Repository: a.Repository,
			Path: unids("authority.path", a.Path, &p)}
	}
	if a := d.Approval; a != nil {
		ap := &Approval{Reference: a.Reference, Key: a.Key}
		var err error
		if ap.ValidFrom, err = time.Parse(time.RFC3339, a.ValidFrom); err != nil {
			p.add("approval.valid_from", "must be an RFC 3339 time")
		}
		if ap.ValidUntil, err = time.Parse(time.RFC3339, a.ValidUntil); err != nil {
			p.add("approval.valid_until", "must be an RFC 3339 time")
		}
		if ap.Signature, err = base64.StdEncoding.DecodeString(a.Signature); err != nil {
			p.add("approval.signature", "must be standard base64")
		}
		r.Approval = ap
	}
	if err := p.err(); err != nil {
		return nil, err
	}
	return r, nil
}
