package knowledge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// ErrNotApplicable: the procedure does not apply in the requested repository.
var ErrNotApplicable = errors.New("procedure does not apply in this repository")

// Catalog is the complete, validated knowledge of one snapshot. It never
// changes, so every read of it, and every reference it resolves, sees that
// one snapshot.
type Catalog struct {
	snapshot   Snapshot
	procs      map[string]*Procedure
	keys       []string
	records    map[string]*Record
	recordKeys []string

	indexOnce sync.Once
	index     map[string][]posting
}

// loadCatalog reads and validates every document of a snapshot. Paths that
// are neither procedure documents, record documents nor the manifest,
// documents whose key differs from their path, records without a
// manifest or a validator, and catalogs that fail validation are refused.
func loadCatalog(ctx context.Context, s Snapshot, files Files, vs []RecordValidator) (*Catalog, error) {
	var p problems
	procs := map[string]*Procedure{}
	records := map[string]*Record{}
	hasManifest := false
	for _, path := range files.Paths() {
		if path == ManifestPath {
			b, err := files.Read(path)
			if err != nil {
				return nil, err
			}
			hasManifest = true
			checkManifest(&p, b)
			continue
		}
		if t, key, ok := recordKeyOf(path); ok {
			b, err := files.Read(path)
			if err != nil {
				return nil, err
			}
			r, err := DecodeRecord(b)
			switch {
			case err != nil:
				p.add("records["+key+"]", "%v", err)
			case r.Type != t:
				p.add("records["+key+"].type", "is %s, but the document is stored as a %s", r.Type, t)
			case records[key] != nil:
				p.add("records["+key+"]", "is stored under more than one type")
			default:
				records[key] = r
			}
			continue
		}
		key, ok := keyOf(path)
		if !ok {
			p.add("files["+path+"]", "is not a procedure document, a record document or the manifest")
			continue
		}
		b, err := files.Read(path)
		if err != nil {
			return nil, err
		}
		proc, err := Decode(b)
		if err != nil {
			p.add("procedures["+key+"]", "%v", err)
			continue
		}
		procs[key] = proc
	}
	if len(records) > 0 && !hasManifest {
		p.add(ManifestPath, "is required in a snapshot that holds records")
	}
	if err := p.err(); err != nil {
		return nil, err
	}
	if err := checkCatalog(procs, records); err != nil {
		return nil, err
	}
	if err := validateRecords(ctx, vs, records); err != nil {
		return nil, err
	}
	return newCatalog(s, procs, records), nil
}

func newCatalog(s Snapshot, procs map[string]*Procedure, records map[string]*Record) *Catalog {
	return &Catalog{snapshot: s, procs: procs, keys: sortedKeys(procs), records: records, recordKeys: sortedRecordKeys(records)}
}

// Format is 1 for a catalog of procedures only and FormatRecords when it
// holds typed records.
func (c *Catalog) Format() int {
	if len(c.records) > 0 {
		return FormatRecords
	}
	return 1
}

// RecordKeys lists every typed record, sorted.
func (c *Catalog) RecordKeys() []string { return slices.Clone(c.recordKeys) }

// Record returns a copy of the record at key.
func (c *Catalog) Record(key string) (*Record, error) {
	r, ok := c.records[key]
	if !ok {
		return nil, fmt.Errorf("record %s: %w", key, ErrNotFound)
	}
	return r.Clone(), nil
}

// Records returns copies of every record, sorted by key.
func (c *Catalog) Records() []*Record {
	out := make([]*Record, len(c.recordKeys))
	for i, k := range c.recordKeys {
		out[i] = c.records[k].Clone()
	}
	return out
}

// Snapshot is the token of the state this catalog holds.
func (c *Catalog) Snapshot() Snapshot { return c.snapshot }

// Keys lists every procedure, sorted.
func (c *Catalog) Keys() []string { return slices.Clone(c.keys) }

func (c *Catalog) Len() int { return len(c.keys) }

// Get returns a copy of the procedure's definition.
func (c *Catalog) Get(key string) (*Procedure, error) {
	p, ok := c.procs[key]
	if !ok {
		return nil, fmt.Errorf("procedure %s: %w", key, ErrNotFound)
	}
	return p.clone(), nil
}

func (p *Procedure) clone() *Procedure {
	c := *p
	c.Contract, c.Instructions = p.Contract.Clone(), p.Instructions.Clone()
	c.References = make([]Reference, len(p.References))
	for i, r := range p.References {
		c.References[i] = Reference{Name: r.Name, Key: r.Key, Inputs: r.Inputs.Clone()}
		if r.Condition != nil {
			cond := *r.Condition
			c.References[i].Condition = &cond
		}
	}
	if len(c.References) == 0 {
		c.References = nil
	}
	return &c
}

// Node is a procedure with its references resolved in the same snapshot.
// A procedure referenced twice is one shared Node.
type Node struct {
	Snapshot   Snapshot
	Procedure  *Procedure
	References []Edge
}

type Edge struct {
	Reference Reference
	Node      *Node
}

// Resolve composes key with everything it references, all from this
// catalog's snapshot. With a repository, key must apply there; validation
// already guarantees that every referenced procedure then applies too.
func (c *Catalog) Resolve(key, repository string) (*Node, error) {
	root, ok := c.procs[key]
	if !ok {
		return nil, fmt.Errorf("procedure %s: %w", key, ErrNotFound)
	}
	if repository != "" && !root.Applicability.ApplicableIn(repository) {
		return nil, fmt.Errorf("%s in %s: %w", key, repository, ErrNotApplicable)
	}
	nodes := map[string]*Node{}
	var build func(k string) *Node
	build = func(k string) *Node {
		if n := nodes[k]; n != nil {
			return n
		}
		p := c.procs[k].clone()
		n := &Node{Snapshot: c.snapshot, Procedure: p}
		nodes[k] = n
		for _, r := range p.References {
			n.References = append(n.References, Edge{Reference: r, Node: build(r.Key)})
		}
		return n
	}
	return build(key), nil
}

type posting struct {
	key    string
	weight int
}

// Candidate is one discovery result.
type Candidate struct {
	Key     string
	Goal    string
	Score   int
	Matched []string // query terms found
}

// Discover ranks this snapshot's procedures by the words they share with
// task: a term in the key weighs 3, in the goal 2, in the method or
// philosophy 1. It is deterministic (ties by key) and reads no history.
// With a repository, only procedures that apply there are returned.
func (c *Catalog) Discover(task, repository string, limit int) []Candidate {
	c.indexOnce.Do(c.buildIndex)
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	scores := map[string]int{}
	matched := map[string][]string{}
	terms := terms(task)
	for _, t := range terms {
		for _, ps := range c.index[t] {
			scores[ps.key] += ps.weight
			matched[ps.key] = append(matched[ps.key], t)
		}
	}
	var out []Candidate
	for k, s := range scores {
		p := c.procs[k]
		if repository != "" && !p.Applicability.ApplicableIn(repository) {
			continue
		}
		out = append(out, Candidate{Key: k, Goal: p.Goal, Score: s, Matched: matched[k]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (c *Catalog) buildIndex() {
	c.index = map[string][]posting{}
	for _, k := range c.keys {
		p := c.procs[k]
		best := map[string]int{}
		for _, f := range []struct {
			text   string
			weight int
		}{{k, 3}, {p.Goal, 2}, {p.Method, 1}, {p.Philosophy, 1}} {
			for _, t := range terms(f.text) {
				best[t] = max(best[t], f.weight)
			}
		}
		for t, w := range best {
			c.index[t] = append(c.index[t], posting{key: k, weight: w})
		}
	}
}

// terms are the distinct lowercase words of s of at least two letters or digits.
func terms(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) >= 2 && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}
