// Package knowledge is the storage-neutral core of the living-knowledge
// experiment: the procedure document and its canonical encoding, the store
// contract both adapters implement, and the validation every publication
// must pass, whatever stores it.
package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Reference names a child procedure by stable key. Inputs maps each child
// input to the parent input that supplies it.
type Reference struct {
	Name   string            `json:"name"`
	Key    string            `json:"key"`
	Inputs map[string]string `json:"inputs,omitempty"`
}

// Procedure is one procedure's published knowledge. There is no version
// field: the store's snapshot identifies a state of the whole tree.
type Procedure struct {
	Key           string          `json:"key"`
	Goal          string          `json:"goal"`
	Method        string          `json:"method"`
	Inputs        []string        `json:"inputs,omitempty"`
	Contract      json.RawMessage `json:"contract"`
	Instructions  json.RawMessage `json:"instructions"`
	References    []Reference     `json:"references,omitempty"`
	Applicability []string        `json:"applicability,omitempty"`
}

// Knowledge is a procedure as read at one snapshot.
type Knowledge struct {
	Key       string
	Snapshot  string
	Hash      string // sha256 of the canonical document, equal across stores
	Procedure *Procedure
}

// Change replaces a procedure's document, or deletes it when Procedure is nil.
type Change struct {
	Key       string
	Procedure *Procedure
}

// Note describes a publication for the audit history.
type Note struct {
	Author  string
	Message string
	Time    time.Time
}

// Entry is one publication in the history, newest first.
type Entry struct {
	Snapshot string
	Parents  []string
	Note     Note
	Changed  []string
}

// Store is the whole storage contract. Everything else (resolution,
// revert, transfer policy) is built on it or is adapter-specific.
type Store interface {
	// Head returns the active snapshot, "" when nothing is published.
	Head(ctx context.Context) (string, error)
	ReadActive(ctx context.Context, key string) (Knowledge, error)
	ReadAt(ctx context.Context, key, snapshot string) (Knowledge, error)
	// Keys lists the procedures of a snapshot, sorted.
	Keys(ctx context.Context, snapshot string) ([]string, error)
	// Publish validates the tree that results from applying changes to
	// expected and makes it active, only if expected is still active.
	Publish(ctx context.Context, expected string, changes []Change, note Note) (string, error)
	History(ctx context.Context, limit int) ([]Entry, error)
	Close() error
}

var (
	ErrNotFound = errors.New("not found")
	// ErrConflict: the expected snapshot is no longer active.
	ErrConflict    = errors.New("stale snapshot: active knowledge has changed")
	ErrInvalid     = errors.New("invalid knowledge")
	ErrNonCanonical = errors.New("document is not in canonical form")
)

// ValidationError lists why a tree cannot be published.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "invalid knowledge: " + strings.Join(e.Problems, "; ")
}
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

// Policy bounds what a store accepts.
type Policy struct {
	RequiredGuidance string // every other procedure must reference it
	MaxProcedures    int
	MaxDocBytes      int
	MaxDepth         int
	MaxReferences    int
}

var DefaultPolicy = Policy{RequiredGuidance: "agent.guidance.shared", MaxProcedures: 50000, MaxDocBytes: 64 << 10, MaxDepth: 16, MaxReferences: 32}

var (
	keyPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*){1,7}$`)
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
)

// ValidKey reports whether key is a well-formed procedure identity. A key
// maps to a storage path by its segments, so it can never contain "..", "/"
// or anything a filesystem or Git tree would interpret.
func ValidKey(key string) bool { return keyPattern.MatchString(key) }

// Encode returns the canonical bytes of p: compact JSON, object members of
// contract and instructions sorted, numbers kept exactly, one trailing newline.
func Encode(p *Procedure) ([]byte, error) {
	c := *p
	var err error
	if c.Contract, err = canonical(p.Contract); err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	if c.Instructions, err = canonical(p.Instructions); err != nil {
		return nil, fmt.Errorf("instructions: %w", err)
	}
	b, err := json.Marshal(&c)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func canonical(raw json.RawMessage) (json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, errors.New("must be a JSON object")
	}
	return json.Marshal(v)
}

// Decode parses canonical bytes strictly: unknown members, trailing data,
// oversize or non-canonical input are refused. Imported knowledge passes
// through here before anything else looks at it.
func Decode(b []byte, maxBytes int) (*Procedure, error) {
	if maxBytes > 0 && len(b) > maxBytes {
		return nil, fmt.Errorf("document of %d bytes exceeds %d", len(b), maxBytes)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var p Procedure
	if err := d.Decode(&p); err != nil {
		return nil, err
	}
	if d.More() {
		return nil, errors.New("trailing data after the document")
	}
	again, err := Encode(&p)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(again, b) {
		return nil, ErrNonCanonical
	}
	return &p, nil
}

// Hash is the store-independent identity of a canonical document.
func Hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Cached is a decoded document and its Hash.
type Cached struct {
	Procedure *Procedure
	Hash      string
}

// Cache holds decoded documents by a storage content address. Content is
// immutable, so an entry never needs invalidation; the bound only limits memory.
type Cache struct {
	mu  sync.Mutex
	max int
	m   map[string]Cached
}

func NewCache(max int) *Cache { return &Cache{max: max, m: map[string]Cached{}} }

func (c *Cache) Get(h string) (Cached, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.m[h]
	return p, ok
}

func (c *Cache) Put(h string, p Cached) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.max {
		clear(c.m)
	}
	c.m[h] = p
}

// View is a complete proposed tree, as Validate sees it.
type View interface {
	Keys() []string
	Get(key string) (*Procedure, error)
}

// Validate checks a whole tree: identities, documents, references,
// input compatibility, applicability, required guidance, cycles and depth.
// It never executes or interprets instructions.
func Validate(v View, pol Policy) error {
	var probs []string
	add := func(format string, args ...any) {
		if len(probs) < 20 {
			probs = append(probs, fmt.Sprintf(format, args...))
		}
	}
	keys := v.Keys()
	if pol.MaxProcedures > 0 && len(keys) > pol.MaxProcedures {
		return &ValidationError{Problems: []string{fmt.Sprintf("%d procedures exceed %d", len(keys), pol.MaxProcedures)}}
	}
	docs := make(map[string]*Procedure, len(keys))
	for _, k := range keys {
		p, err := v.Get(k)
		if err != nil {
			add("%s: %v", k, err)
			continue
		}
		docs[k] = p
	}
	if pol.RequiredGuidance != "" && len(docs) > 0 && docs[pol.RequiredGuidance] == nil {
		add("required guidance %s is missing", pol.RequiredGuidance)
	}
	for _, k := range keys {
		p := docs[k]
		if p == nil {
			continue
		}
		if !ValidKey(k) || p.Key != k {
			add("%s: identity %q does not match its key", k, p.Key)
		}
		if strings.TrimSpace(p.Goal) == "" || strings.TrimSpace(p.Method) == "" {
			add("%s: goal and method are required", k)
		}
		if !sortedUnique(p.Inputs, namePattern) {
			add("%s: inputs must be sorted, unique names", k)
		}
		if !sortedUnique(p.Applicability, nil) {
			add("%s: applicability must be sorted and unique", k)
		}
		if pol.MaxReferences > 0 && len(p.References) > pol.MaxReferences {
			add("%s: %d references exceed %d", k, len(p.References), pol.MaxReferences)
		}
		names := map[string]bool{}
		guided := false
		for _, r := range p.References {
			if !namePattern.MatchString(r.Name) || names[r.Name] {
				add("%s: reference name %q is invalid or repeated", k, r.Name)
			}
			names[r.Name] = true
			guided = guided || r.Key == pol.RequiredGuidance
			c := docs[r.Key]
			if c == nil {
				add("%s: reference %s names missing procedure %s", k, r.Name, r.Key)
				continue
			}
			for child, parent := range r.Inputs {
				if !slices.Contains(c.Inputs, child) {
					add("%s: reference %s maps input %s that %s does not declare", k, r.Name, child, r.Key)
				}
				if !slices.Contains(p.Inputs, parent) {
					add("%s: reference %s maps from undeclared input %s", k, r.Name, parent)
				}
			}
			if len(c.Applicability) > 0 && (len(p.Applicability) == 0 || !subset(p.Applicability, c.Applicability)) {
				add("%s: reference %s applies in fewer repositories than its parent", k, r.Name)
			}
		}
		if pol.RequiredGuidance != "" && k != pol.RequiredGuidance && !guided {
			add("%s: does not reference required guidance %s", k, pol.RequiredGuidance)
		}
	}
	if len(probs) == 0 {
		depthCheck(docs, pol.MaxDepth, add)
	}
	if len(probs) > 0 {
		return &ValidationError{Problems: probs}
	}
	return nil
}

func depthCheck(docs map[string]*Procedure, max int, add func(string, ...any)) {
	const visiting = -1
	depth := map[string]int{}
	var visit func(k string) int
	visit = func(k string) int {
		switch d, ok := depth[k]; {
		case ok && d == visiting:
			add("reference cycle through %s", k)
			return 1 << 20
		case ok:
			return d
		}
		depth[k] = visiting
		d := 1
		for _, r := range docs[k].References {
			d = max2(d, 1+visit(r.Key))
		}
		depth[k] = d
		return d
	}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if d := visit(k); max > 0 && d > max && d < 1<<20 {
			add("%s: reference depth %d exceeds %d", k, d, max)
		}
	}
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func sortedUnique(s []string, pattern *regexp.Regexp) bool {
	for i, v := range s {
		if v == "" || (pattern != nil && !pattern.MatchString(v)) || (i > 0 && s[i-1] >= v) {
			return false
		}
	}
	return true
}

func subset(a, b []string) bool {
	for _, v := range a {
		if !slices.Contains(b, v) {
			return false
		}
	}
	return true
}

// Overlay is a View of a base snapshot with changes applied.
type Overlay struct {
	Base    map[string]string // key -> content hash at the base snapshot
	Load    func(key, hash string) (*Procedure, error)
	Changes []Change
	keys    []string
	changed map[string]*Procedure
}

func (o *Overlay) init() {
	if o.changed != nil {
		return
	}
	o.changed = map[string]*Procedure{}
	for _, c := range o.Changes {
		o.changed[c.Key] = c.Procedure
	}
	for k := range o.Base {
		if p, ok := o.changed[k]; !ok || p != nil {
			o.keys = append(o.keys, k)
		}
	}
	for k, p := range o.changed {
		if _, ok := o.Base[k]; !ok && p != nil {
			o.keys = append(o.keys, k)
		}
	}
	sort.Strings(o.keys)
}

func (o *Overlay) Keys() []string { o.init(); return o.keys }

func (o *Overlay) Get(key string) (*Procedure, error) {
	o.init()
	if p, ok := o.changed[key]; ok {
		if p == nil {
			return nil, ErrNotFound
		}
		return p, nil
	}
	h, ok := o.Base[key]
	if !ok {
		return nil, ErrNotFound
	}
	return o.Load(key, h)
}

// CheckChanges refuses malformed change lists before any storage work.
func CheckChanges(changes []Change, pol Policy) ([][]byte, error) {
	if len(changes) == 0 {
		return nil, &ValidationError{Problems: []string{"no changes"}}
	}
	seen := map[string]bool{}
	out := make([][]byte, len(changes))
	for i, c := range changes {
		if !ValidKey(c.Key) || seen[c.Key] {
			return nil, &ValidationError{Problems: []string{fmt.Sprintf("change %d: key %q is invalid or repeated", i, c.Key)}}
		}
		seen[c.Key] = true
		if c.Procedure == nil {
			continue
		}
		b, err := Encode(c.Procedure)
		if err != nil {
			return nil, &ValidationError{Problems: []string{fmt.Sprintf("%s: %v", c.Key, err)}}
		}
		if pol.MaxDocBytes > 0 && len(b) > pol.MaxDocBytes {
			return nil, &ValidationError{Problems: []string{fmt.Sprintf("%s: %d bytes exceed %d", c.Key, len(b), pol.MaxDocBytes)}}
		}
		out[i] = b
	}
	return out, nil
}

// Node is a procedure with its references resolved at one snapshot.
type Node struct {
	Knowledge
	Children []Child
}

type Child struct {
	Name string
	Node *Node
}

// Resolve reads key and its whole reference graph at one snapshot, so a
// task never mixes procedure states from different publications.
func Resolve(ctx context.Context, s Store, key, snapshot string) (*Node, error) {
	seen := map[string]*Node{}
	var visit func(k string, depth int) (*Node, error)
	visit = func(k string, depth int) (*Node, error) {
		if n := seen[k]; n != nil {
			return n, nil
		}
		if depth > 64 {
			return nil, errors.New("reference depth exceeded")
		}
		kn, err := s.ReadAt(ctx, k, snapshot)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		n := &Node{Knowledge: kn}
		seen[k] = n
		for _, r := range kn.Procedure.References {
			c, err := visit(r.Key, depth+1)
			if err != nil {
				return nil, err
			}
			n.Children = append(n.Children, Child{Name: r.Name, Node: c})
		}
		return n, nil
	}
	return visit(key, 0)
}

// Manifest maps every key of a snapshot to its content hash.
func Manifest(ctx context.Context, s Store, snapshot string) (map[string]string, error) {
	keys, err := s.Keys(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(keys))
	for _, k := range keys {
		kn, err := s.ReadAt(ctx, k, snapshot)
		if err != nil {
			return nil, err
		}
		m[k] = kn.Hash
	}
	return m, nil
}

// Revert publishes, on top of expected, the content of snapshot to: a new
// publication, never a rewrite of history.
func Revert(ctx context.Context, s Store, expected, to string, note Note) (string, error) {
	cur, err := Manifest(ctx, s, expected)
	if err != nil {
		return "", err
	}
	old, err := Manifest(ctx, s, to)
	if err != nil {
		return "", err
	}
	var changes []Change
	for k, h := range old {
		if cur[k] != h {
			kn, err := s.ReadAt(ctx, k, to)
			if err != nil {
				return "", err
			}
			changes = append(changes, Change{Key: k, Procedure: kn.Procedure})
		}
	}
	for k := range cur {
		if _, ok := old[k]; !ok {
			changes = append(changes, Change{Key: k})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Key < changes[j].Key })
	return s.Publish(ctx, expected, changes, note)
}
