package knowledge

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Kernel reads and publishes living knowledge through a Store.
type Kernel struct {
	store      Store
	validators []RecordValidator

	mu       sync.Mutex
	catalogs map[Snapshot]*Catalog // immutable; bounded
}

func New(store Store, opts ...Option) *Kernel {
	k := &Kernel{store: store, catalogs: map[Snapshot]*Catalog{}}
	for _, o := range opts {
		o(k)
	}
	return k
}

// Active returns the catalog of the active snapshot; an empty catalog with
// the empty snapshot when nothing is published.
func (k *Kernel) Active(ctx context.Context) (*Catalog, error) {
	s, err := k.store.Active(ctx)
	if err != nil {
		return nil, err
	}
	return k.At(ctx, s)
}

// At returns the catalog of a published snapshot. A task that keeps the
// token reads exactly that knowledge however much is published later.
func (k *Kernel) At(ctx context.Context, s Snapshot) (*Catalog, error) {
	if s == "" {
		return newCatalog("", map[string]*Procedure{}, map[string]*Record{}), nil
	}
	k.mu.Lock()
	c, ok := k.catalogs[s]
	k.mu.Unlock()
	if ok {
		return c, nil
	}
	files, err := k.store.Files(ctx, s)
	if err != nil {
		return nil, err
	}
	c, err = loadCatalog(ctx, s, files, k.validators)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", s, err)
	}
	k.remember(c)
	return c, nil
}

func (k *Kernel) remember(c *Catalog) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.catalogs) >= 8 {
		clear(k.catalogs)
	}
	k.catalogs[c.snapshot] = c
}

// Change replaces the procedure at Key, or removes it when Procedure is nil.
type Change struct {
	Key       string
	Procedure *Procedure
}

// RecordChange replaces the typed record at Key, or removes it when Record
// is nil.
type RecordChange struct {
	Key    string
	Record *Record
}

// Provenance says who authorised a publication and why; it is stored with
// the publication and travels with the knowledge's history.
type Provenance struct {
	Author    string // who authorised it, e.g. "copilot.agent" or a person
	Summary   string // one line: what changed
	Rationale string // why the change was accepted
	// Finding names what motivated the change, e.g. a feedback report id
	// in the operational store. Optional, one line.
	Finding string
	// Validation says what was checked beyond the catalog validation the
	// kernel always performs, e.g. a verified run. Optional, one line.
	Validation string
}

// Proposal is a set of changes to the knowledge active at Base.
type Proposal struct {
	Base       Snapshot
	Changes    []Change
	Records    []RecordChange
	Provenance Provenance
}

// Trailer keys of a publication's structured provenance.
const (
	TrailerFinding    = "Polaroid-Finding"
	TrailerValidation = "Polaroid-Validation"
	TrailerValidated  = "Polaroid-Validated"
	TrailerRollback   = "Polaroid-Rollback-To"
)

var badAuthor = regexp.MustCompile(`[<>\x00-\x1f\x7f]`)

func (pv Provenance) check(p *problems) {
	switch {
	case strings.TrimSpace(pv.Author) == "", len(pv.Author) > 128, badAuthor.MatchString(pv.Author):
		p.add("provenance.author", "is required: at most 128 bytes, without <, > or control characters")
	}
	checkText(p, "provenance.summary", pv.Summary, maxLineBytes)
	if strings.ContainsAny(pv.Summary, "\r\n") {
		p.add("provenance.summary", "must be one line")
	}
	checkText(p, "provenance.rationale", pv.Rationale, maxTextBytes)
	for field, v := range map[string]string{"provenance.finding": pv.Finding, "provenance.validation": pv.Validation} {
		if v != "" && (strings.ContainsAny(v, "\r\n") || strings.TrimSpace(v) != v || len(v) > maxLineBytes) {
			p.add(field, "must be one trimmed line of at most %d bytes", maxLineBytes)
		}
	}
}

// Publish validates the catalog that results from applying the proposal to
// its base, completely, and publishes it atomically. It returns ErrStale if
// the base is no longer active, and a ValidationError if the resulting
// catalog is not valid; nothing is published in either case.
func (k *Kernel) Publish(ctx context.Context, prop Proposal) (Snapshot, error) {
	return k.publish(ctx, prop, nil, nil)
}

func (k *Kernel) publish(ctx context.Context, prop Proposal, extra []Trailer, restoring *Catalog) (Snapshot, error) {
	var p problems
	prop.Provenance.check(&p)
	if len(prop.Changes)+len(prop.Records) == 0 {
		p.add("changes", "at least one change is required")
	}
	seen := map[string]bool{}
	for i, c := range prop.Changes {
		field := fmt.Sprintf("changes[%d]", i)
		checkKey(&p, field+".key", c.Key)
		if seen[c.Key] {
			p.add(field+".key", "%s is changed twice", c.Key)
		}
		seen[c.Key] = true
		if c.Procedure != nil && c.Procedure.Key != c.Key {
			p.add(field+".procedure.key", "is %q, not the change's key %q", c.Procedure.Key, c.Key)
		}
	}
	if err := p.err(); err != nil {
		return "", err
	}
	base, err := k.At(ctx, prop.Base)
	if err != nil {
		return "", err
	}
	procs := make(map[string]*Procedure, len(base.procs)+len(prop.Changes))
	for key, proc := range base.procs {
		procs[key] = proc
	}
	var files []FileChange
	for i, c := range prop.Changes {
		if c.Procedure == nil {
			if _, ok := procs[c.Key]; !ok {
				p.add(fmt.Sprintf("changes[%d].key", i), "%s is not in the catalog", c.Key)
				continue
			}
			delete(procs, c.Key)
			files = append(files, FileChange{Path: Path(c.Key), Delete: true})
			continue
		}
		proc := c.Procedure.clone()
		var pp problems
		checkProcedure(&pp, "procedures["+c.Key+"]", proc)
		if len(pp) > 0 {
			p = append(p, pp...)
			continue
		}
		doc, err := Encode(proc)
		if err != nil {
			p.add("procedures["+c.Key+"]", "%v", err)
			continue
		}
		if len(doc) > maxDocumentBytes {
			p.add("procedures["+c.Key+"]", "document of %d bytes exceeds %d", len(doc), maxDocumentBytes)
			continue
		}
		if old, ok := procs[c.Key]; ok && equalDocs(old, proc) {
			p.add(fmt.Sprintf("changes[%d]", i), "%s is unchanged", c.Key)
			continue
		}
		procs[c.Key] = proc
		files = append(files, FileChange{Path: Path(c.Key), Content: doc})
	}
	records := make(map[string]*Record, len(base.records)+len(prop.Records))
	for key, r := range base.records {
		records[key] = r
	}
	seen = map[string]bool{}
	for i, c := range prop.Records {
		field := fmt.Sprintf("records_changes[%d]", i)
		checkKey(&p, field+".key", c.Key)
		if seen[c.Key] {
			p.add(field+".key", "%s is changed twice", c.Key)
		}
		seen[c.Key] = true
		old := records[c.Key]
		if c.Record == nil {
			if old == nil {
				p.add(field+".key", "%s is not a record in the catalog", c.Key)
				continue
			}
			delete(records, c.Key)
			files = append(files, FileChange{Path: RecordPath(old.Type, c.Key), Delete: true})
			continue
		}
		r := normalized(c.Record)
		if r.Key != c.Key {
			p.add(field+".record.id", "is %q, not the change's key %q", r.Key, c.Key)
			continue
		}
		var pp problems
		checkRecord(&pp, "records["+c.Key+"]", r)
		if len(pp) > 0 {
			p = append(p, pp...)
			continue
		}
		doc, err := EncodeRecord(r)
		if err != nil {
			p.add("records["+c.Key+"]", "%v", err)
			continue
		}
		if len(doc) > maxDocumentBytes {
			p.add("records["+c.Key+"]", "document of %d bytes exceeds %d", len(doc), maxDocumentBytes)
			continue
		}
		if old != nil && equalRecords(old, r) {
			p.add(field, "%s is unchanged", c.Key)
			continue
		}
		if old != nil && old.Type != r.Type {
			files = append(files, FileChange{Path: RecordPath(old.Type, c.Key), Delete: true})
		}
		records[c.Key] = r
		files = append(files, FileChange{Path: RecordPath(r.Type, c.Key), Content: doc})
	}
	if err := p.err(); err != nil {
		return "", err
	}
	switch hadManifest := len(base.records) > 0; {
	case len(records) > 0 && !hadManifest:
		files = append(files, FileChange{Path: ManifestPath, Content: encodeManifest()})
	case len(records) == 0 && hadManifest:
		files = append(files, FileChange{Path: ManifestPath, Delete: true})
	}
	if err := checkCatalog(procs, records); err != nil {
		return "", err
	}
	if err := CheckTransition(base, newCatalog("", procs, records), restoring); err != nil {
		return "", err
	}
	if err := validateRecords(ctx, k.validators, records); err != nil {
		return "", err
	}

	pv := prop.Provenance
	trailers := append([]Trailer(nil), extra...)
	if pv.Finding != "" {
		trailers = append(trailers, Trailer{TrailerFinding, pv.Finding})
	}
	if pv.Validation != "" {
		trailers = append(trailers, Trailer{TrailerValidation, pv.Validation})
	}
	validated := fmt.Sprintf("catalog of %d procedures: documents, references, applicability, cycles, limits", len(procs))
	if len(records) > 0 {
		validated += fmt.Sprintf("; %d records: types, relationships, JSON-LD, SHACL", len(records))
	}
	trailers = append(trailers, Trailer{TrailerValidated, validated})
	s, err := k.store.Commit(ctx, Commit{
		Expected: prop.Base, Changes: files, Author: pv.Author, Subject: pv.Summary, Body: pv.Rationale, Trailers: trailers,
		Check: func(f Files) error { return sameLayout(f, procs, records) },
	})
	if err != nil {
		return "", err
	}
	k.remember(newCatalog(s, procs, records))
	return s, nil
}

// sameLayout confirms that the store's proposed tree holds exactly the
// documents of the validated catalog, and the manifest when it has records.
func sameLayout(f Files, procs map[string]*Procedure, records map[string]*Record) error {
	paths := f.Paths()
	want := len(procs) + len(records)
	if len(records) > 0 {
		want++
	}
	if len(paths) != want {
		return fmt.Errorf("%w: the proposed tree has %d files for %d procedures and %d records", ErrInvalid, len(paths), len(procs), len(records))
	}
	for _, path := range paths {
		if path == ManifestPath && len(records) > 0 {
			continue
		}
		if t, key, ok := recordKeyOf(path); ok && records[key] != nil && records[key].Type == t {
			continue
		}
		if key, ok := keyOf(path); !ok || procs[key] == nil {
			return fmt.Errorf("%w: the proposed tree holds %s, which is not a validated document", ErrInvalid, path)
		}
	}
	return nil
}

func equalRecords(a, b *Record) bool {
	x, err1 := EncodeRecord(a)
	y, err2 := EncodeRecord(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// Rollback publishes, on top of base, the knowledge of the earlier
// published snapshot to. History is kept: the rollback is a new
// publication, recorded with the snapshot it restores.
func (k *Kernel) Rollback(ctx context.Context, base, to Snapshot, pv Provenance) (Snapshot, error) {
	cur, err := k.At(ctx, base)
	if err != nil {
		return "", err
	}
	old, err := k.At(ctx, to)
	if err != nil {
		return "", err
	}
	var changes []Change
	for _, key := range old.keys {
		if p, ok := cur.procs[key]; !ok || !equalDocs(p, old.procs[key]) {
			changes = append(changes, Change{Key: key, Procedure: old.procs[key]})
		}
	}
	for _, key := range cur.keys {
		if _, ok := old.procs[key]; !ok {
			changes = append(changes, Change{Key: key})
		}
	}
	var records []RecordChange
	for _, key := range old.recordKeys {
		if r, ok := cur.records[key]; !ok || !equalRecords(r, old.records[key]) {
			records = append(records, RecordChange{Key: key, Record: old.records[key]})
		}
	}
	for _, key := range cur.recordKeys {
		if _, ok := old.records[key]; !ok {
			records = append(records, RecordChange{Key: key})
		}
	}
	if len(changes)+len(records) == 0 {
		return "", &ValidationError{Problems: []Problem{{Field: "to", Message: "the active knowledge already equals that snapshot"}}}
	}
	// Only knowledge published here was governed edge by edge. A snapshot
	// that arrived through an activation may be an intermediate one whose
	// own transition was never checked, so restoring it gets no exemption.
	restoring := old
	imported, err := k.store.Imported(ctx, to)
	if err != nil {
		return "", err
	}
	if imported {
		restoring = nil
	}
	return k.publish(ctx, Proposal{Base: base, Changes: changes, Records: records, Provenance: pv}, []Trailer{{TrailerRollback, string(to)}}, restoring)
}

// Publication is one entry of the knowledge history.
type Publication struct {
	Snapshot   Snapshot
	Parents    []Snapshot
	When       time.Time
	Provenance Provenance
	RollbackTo Snapshot
	Validated  string
	Changed    []KeyChange
}

// KeyChange: Action is "added", "modified" or "removed". Record is true
// for a typed record, false for a procedure document.
type KeyChange struct {
	Key    string
	Action string
	Record bool
}

// History lists publications from a published snapshot back, newest first.
func (k *Kernel) History(ctx context.Context, from Snapshot, limit int) ([]Publication, error) {
	entries, err := k.store.Log(ctx, from, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Publication, 0, len(entries))
	for _, e := range entries {
		pub := Publication{Snapshot: e.Snapshot, Parents: e.Parents, When: e.When,
			Provenance: Provenance{Author: e.Author, Summary: e.Subject, Rationale: e.Body}}
		for _, t := range e.Trailers {
			switch t.Key {
			case TrailerFinding:
				pub.Provenance.Finding = t.Value
			case TrailerValidation:
				pub.Provenance.Validation = t.Value
			case TrailerValidated:
				pub.Validated = t.Value
			case TrailerRollback:
				pub.RollbackTo = Snapshot(t.Value)
			}
		}
		for _, c := range e.Changed {
			if c.Path == ManifestPath {
				continue
			}
			action := c.Action
			if action == "deleted" {
				action = "removed"
			}
			if _, key, ok := recordKeyOf(c.Path); ok {
				pub.Changed = append(pub.Changed, KeyChange{Key: key, Action: action, Record: true})
				continue
			}
			key, ok := keyOf(c.Path)
			if !ok {
				return nil, fmt.Errorf("%w: publication %s changed %s, which is not a knowledge document", ErrCorrupt, e.Snapshot, c.Path)
			}
			pub.Changed = append(pub.Changed, KeyChange{Key: key, Action: action})
		}
		out = append(out, pub)
	}
	return out, nil
}
