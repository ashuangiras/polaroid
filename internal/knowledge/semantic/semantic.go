// Package semantic is the adapter between typed knowledge records and the
// semantic web standards they are expressed in (ADR-0050): JSON-LD 1.1 with
// json-gold, RDF graphs and SHACL Core with goRDFlib, and the SKOS, RDF and
// Dublin Core vocabularies. It is the only package that imports those
// libraries. It persists nothing: every graph it builds is an in-memory
// projection of one snapshot's records, rebuilt on demand.
package semantic

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/piprate/json-gold/ld"
	"github.com/tggo/goRDFlib/graph"
	"github.com/tggo/goRDFlib/namespace"
	"github.com/tggo/goRDFlib/shacl"
	"github.com/tggo/goRDFlib/term"

	"github.com/ashuangiras/polaroid/internal/knowledge"
)

//go:embed context-v1.jsonld
var contextV1 []byte

//go:embed shapes-v1.ttl
var shapesV1 string

// Vocabulary IRIs.
const (
	RecordNS = "urn:polaroid:k:"
	CorpusNS = "urn:polaroid:corpus:"
	DCTerms  = "http://purl.org/dc/terms/"
)

// Limits bound the work one validation may do.
type Limits struct {
	Depth            int // JSON nesting of one document
	TriplesPerRecord int
	Triples          int // the whole graph
}

// DefaultLimits suit catalogs of up to tens of thousands of records.
var DefaultLimits = Limits{Depth: 8, TriplesPerRecord: 4096, Triples: 1 << 20}

// ErrUnknownContext is returned for any JSON-LD context other than the
// bundled one; it is never fetched.
var ErrUnknownContext = errors.New("unknown JSON-LD context")

// Loader is the offline JSON-LD document loader: it serves the bundled
// context and refuses every other URL without any I/O.
type Loader struct{ context any }

// NewLoader returns the offline loader of the bundled context.
func NewLoader() (*Loader, error) {
	var c any
	if err := json.Unmarshal(contextV1, &c); err != nil {
		return nil, fmt.Errorf("bundled context: %w", err)
	}
	return &Loader{context: c}, nil
}

// LoadDocument implements ld.DocumentLoader.
func (l *Loader) LoadDocument(u string) (*ld.RemoteDocument, error) {
	if u != knowledge.ContextIRI {
		return nil, ld.NewJsonLdError(ld.LoadingDocumentFailed, fmt.Errorf("%w: %q", ErrUnknownContext, u))
	}
	return &ld.RemoteDocument{DocumentURL: u, Document: l.context}, nil
}

// Validator implements knowledge.RecordValidator.
type Validator struct {
	limits Limits
	loader *Loader
	shapes *shacl.Graph
}

var _ knowledge.RecordValidator = (*Validator)(nil)

// New builds a validator with the bundled context and shapes.
func New(limits Limits) (*Validator, error) {
	loader, err := NewLoader()
	if err != nil {
		return nil, err
	}
	shapes, err := shacl.LoadTurtleString(shapesV1, "")
	if err != nil {
		return nil, fmt.Errorf("bundled shapes: %w", err)
	}
	if err := coreOnly(shapes); err != nil {
		return nil, err
	}
	return &Validator{limits: limits, loader: loader, shapes: shapes}, nil
}

// coreOnly refuses shapes that use SHACL-SPARQL or SHACL-AF: validation is
// SHACL Core, and rules are never run by the shapes engine.
func coreOnly(g *shacl.Graph) error {
	for _, t := range g.All(nil, nil, nil) {
		p := t.Predicate.Value()
		for _, banned := range []string{"sparql", "select", "ask", "rule", "construct", "target", "condition"} {
			if p == shacl.SH+banned {
				return fmt.Errorf("bundled shapes use sh:%s, which is outside SHACL Core", banned)
			}
		}
	}
	return nil
}

// ValidateRecords converts every record to RDF, checks that each record's
// projection states exactly its decoded relationships, and validates the
// whole graph against the shapes.
func (v *Validator) ValidateRecords(ctx context.Context, docs []knowledge.RecordDocument) error {
	g, err := v.Graph(ctx, docs)
	if err != nil {
		return err
	}
	var probs []knowledge.Problem
	for _, d := range docs {
		probs = append(probs, projectionProblems(g, d.Record)...)
	}
	if len(probs) > 0 {
		return &knowledge.ValidationError{Problems: probs}
	}
	report, err := shacl.ValidateContext(ctx, shacl.NewGraphFromRDF(g, ""), v.shapes)
	if err != nil {
		return err
	}
	if report.Conforms {
		return nil
	}
	for _, r := range report.Results {
		field := "records"
		if key, ok := strings.CutPrefix(strings.Trim(r.FocusNode.String(), "<>"), RecordNS); ok {
			field = "records[" + key + "]"
		}
		msg := fmt.Sprintf("%s violates %s", strings.Trim(r.ResultPath.String(), "<>"), strings.TrimPrefix(strings.Trim(r.SourceConstraintComponent.String(), "<>"), shacl.SH))
		if v := r.Value.String(); v != "" {
			msg += " with " + v
		}
		probs = append(probs, knowledge.Problem{Field: field, Message: msg})
		if len(probs) == 50 {
			break
		}
	}
	return &knowledge.ValidationError{Problems: probs}
}

// Graph is the RDF projection of the records: one in-memory graph, never
// stored. Each document is processed by json-gold with the offline loader
// in SafeMode, so a term the bundled context does not define is refused.
func (v *Validator) Graph(ctx context.Context, docs []knowledge.RecordDocument) (*graph.Graph, error) {
	g := graph.NewGraph()
	var probs []knowledge.Problem
	for i, d := range docs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		field := "records[" + d.Record.Key + "]"
		if depth(d.Document) > v.limits.Depth {
			probs = append(probs, knowledge.Problem{Field: field, Message: fmt.Sprintf("nests deeper than %d levels", v.limits.Depth)})
			continue
		}
		ds, err := v.toRDF(d.Document)
		if err != nil {
			probs = append(probs, knowledge.Problem{Field: field, Message: "JSON-LD: " + err.Error()})
			continue
		}
		quads := ds.GetQuads("@default")
		if len(ds.Graphs) != 1 || len(quads) > v.limits.TriplesPerRecord {
			probs = append(probs, knowledge.Problem{Field: field, Message: fmt.Sprintf("must be one default graph of at most %d triples", v.limits.TriplesPerRecord)})
			continue
		}
		for _, q := range quads {
			s, p, o := node(q.Subject, i), node(q.Predicate, i), node(q.Object, i)
			subj, okS := s.(term.Subject)
			pred, okP := p.(term.URIRef)
			if !okS || !okP {
				probs = append(probs, knowledge.Problem{Field: field, Message: "produced a statement that is not RDF"})
				break
			}
			g.Add(subj, pred, o)
		}
		if g.Len() > v.limits.Triples {
			return nil, &knowledge.ValidationError{Problems: []knowledge.Problem{{Field: "records", Message: fmt.Sprintf("the graph exceeds %d triples", v.limits.Triples)}}}
		}
	}
	if len(probs) > 0 {
		return nil, &knowledge.ValidationError{Problems: probs}
	}
	return g, nil
}

func (v *Validator) toRDF(doc []byte) (ds *ld.RDFDataset, err error) {
	var in any
	if err := json.Unmarshal(doc, &in); err != nil {
		return nil, err
	}
	opts := ld.NewJsonLdOptions("")
	opts.DocumentLoader = v.loader
	opts.ProcessingMode = ld.JsonLd_1_1
	opts.SafeMode = true
	defer func() {
		if r := recover(); r != nil {
			ds, err = nil, fmt.Errorf("the processor failed: %v", r)
		}
	}()
	out, err := ld.NewJsonLdProcessor().ToRDF(in, opts)
	if err != nil {
		return nil, err
	}
	ds, ok := out.(*ld.RDFDataset)
	if !ok {
		return nil, fmt.Errorf("unexpected result %T", out)
	}
	return ds, nil
}

// node converts a json-gold RDF node to a goRDFlib term; blank nodes are
// scoped to their document so documents never share one by accident.
func node(n ld.Node, doc int) term.Term {
	switch v := n.(type) {
	case ld.IRI:
		return term.NewURIRefUnsafe(v.Value)
	case ld.BlankNode:
		return term.NewBNode(fmt.Sprintf("d%d%s", doc, strings.TrimPrefix(v.Attribute, "_:")))
	case ld.Literal:
		if v.Language != "" {
			return term.NewLiteral(v.Value, term.WithLang(v.Language))
		}
		return term.NewLiteral(v.Value, term.WithDatatype(term.NewURIRefUnsafe(v.Datatype)))
	}
	return nil
}

func depth(doc []byte) int {
	dec := jsontext.NewDecoder(bytes.NewReader(doc))
	deepest := 0
	for {
		if _, err := dec.ReadToken(); err != nil {
			return deepest
		}
		deepest = max(deepest, dec.StackDepth())
	}
}

// Predicates maps each record relationship to the RDF predicate its
// projection must state.
var Predicates = map[string]string{
	"classified_as":  DCTerms + "subject",
	"broader":        namespace.SKOS.Term("broader").Value(),
	"specializes":    CorpusNS + "specializes",
	"uses":           CorpusNS + "uses",
	"composes":       CorpusNS + "composes",
	"requires":       CorpusNS + "requires",
	"supports":       CorpusNS + "supports",
	"contradicts":    CorpusNS + "contradicts",
	"supersedes":     DCTerms + "replaces",
	"preconditions":  CorpusNS + "precondition",
	"postconditions": CorpusNS + "postcondition",
	"invariants":     CorpusNS + "invariant",
	"excepts":        CorpusNS + "excepts",
	"applies_to":     CorpusNS + "appliesTo",
	"waives":         CorpusNS + "waives",
	"replacement":    CorpusNS + "replacement",
}

// Classes maps each record type to its RDF class.
var Classes = map[knowledge.RecordType]string{
	knowledge.TypeDomain:    CorpusNS + "Domain",
	knowledge.TypeConcept:   namespace.SKOS.Term("Concept").Value(),
	knowledge.TypeClaim:     CorpusNS + "Claim",
	knowledge.TypeRule:      CorpusNS + "Rule",
	knowledge.TypeProcedure: CorpusNS + "Procedure",
	knowledge.TypeEvidence:  CorpusNS + "Evidence",
	knowledge.TypeException: CorpusNS + "Exception",
}

func relationships(r *knowledge.Record) map[string][]string {
	one := func(s string) []string {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	return map[string][]string{
		"classified_as": r.ClassifiedAs, "broader": r.Broader, "specializes": r.Specializes, "uses": r.Uses,
		"composes": r.Composes, "requires": r.Requires, "supports": r.Supports, "contradicts": r.Contradicts,
		"supersedes": r.Supersedes, "preconditions": r.Preconditions, "postconditions": r.Postconditions,
		"invariants": r.Invariants, "excepts": r.Excepts, "applies_to": r.AppliesTo,
		"waives": one(r.Waives), "replacement": one(r.Replacement),
	}
}

// projectionProblems compares what the graph states about a record with
// what the record model decoded: its type and every relationship.
func projectionProblems(g *graph.Graph, r *knowledge.Record) []knowledge.Problem {
	field := "records[" + r.Key + "]"
	subj := term.NewURIRefUnsafe(RecordNS + r.Key)
	var probs []knowledge.Problem
	typ := namespace.RDF.Type
	var types []string
	for t := range g.Objects(subj, &typ) {
		types = append(types, t.String())
	}
	if !slices.Equal(types, []string{Classes[r.Type]}) {
		probs = append(probs, knowledge.Problem{Field: field + ".type", Message: fmt.Sprintf("the RDF projection states types %v, not %s", types, Classes[r.Type])})
	}
	for name, keys := range relationships(r) {
		pred := term.NewURIRefUnsafe(Predicates[name])
		var got []string
		for o := range g.Objects(subj, &pred) {
			got = append(got, o.String())
		}
		want := make([]string, len(keys))
		for i, k := range keys {
			want[i] = RecordNS + k
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			probs = append(probs, knowledge.Problem{Field: field + "." + name, Message: fmt.Sprintf("the RDF projection states %v, not %v", got, want)})
		}
	}
	return probs
}
