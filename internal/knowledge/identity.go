package knowledge

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"strings"
)

var (
	// Snapshot tokens are opaque to clients, but bounded and printable.
	snapshotPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	instancePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

// FeedbackRef is the knowledge a finding concerns: one procedure, by its
// stable key, as one snapshot served it. It is the typed identity of
// knowledge feedback wherever that feedback is stored.
type FeedbackRef struct {
	Key      string
	Snapshot Snapshot
}

type feedbackRefJSON struct {
	Key      string `json:"key"`
	Snapshot string `json:"snapshot"`
}

// Validate checks the reference's form; Kernel.CheckFeedbackRef also checks
// that it names served knowledge.
func (r FeedbackRef) Validate() error {
	var p problems
	checkKey(&p, "key", r.Key)
	if !snapshotPattern.MatchString(string(r.Snapshot)) {
		p.add("snapshot", "must be the snapshot token the procedure was read from")
	}
	return p.err()
}

// Encode is the reference's canonical JSON, {"key":...,"snapshot":...}.
func (r FeedbackRef) Encode() (jsontext.Value, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(feedbackRefJSON{Key: r.Key, Snapshot: string(r.Snapshot)})
	return jsontext.Value(b), err
}

// ParseFeedbackRef decodes a reference strictly: exactly the members key and
// snapshot, both strings, both valid.
func ParseFeedbackRef(v jsontext.Value) (FeedbackRef, error) {
	var in feedbackRefJSON
	if err := json.Unmarshal(v, &in, json.RejectUnknownMembers(true)); err != nil {
		return FeedbackRef{}, &ValidationError{Problems: []Problem{{Field: "knowledge", Message: "must be {\"key\": string, \"snapshot\": string}"}}}
	}
	var members map[string]jsontext.Value
	if err := json.Unmarshal(v, &members); err != nil || len(members) != 2 {
		return FeedbackRef{}, &ValidationError{Problems: []Problem{{Field: "knowledge", Message: "must have exactly the members key and snapshot"}}}
	}
	r := FeedbackRef{Key: in.Key, Snapshot: Snapshot(in.Snapshot)}
	return r, r.Validate()
}

// CheckFeedbackRef confirms that the snapshot is published here and served
// the procedure.
func (k *Kernel) CheckFeedbackRef(ctx context.Context, r FeedbackRef) error {
	if err := r.Validate(); err != nil {
		return err
	}
	c, err := k.At(ctx, r.Snapshot)
	if err != nil {
		return err
	}
	_, err = c.Get(r.Key)
	return err
}

// PublicationRef names one procedure as one publication on one instance
// left it. It never changes once the publication exists, so it can be
// recorded elsewhere (a feedback resolution) and checked again later.
type PublicationRef struct {
	Instance string
	Snapshot Snapshot
	Key      string
}

const publicationScheme = "polaroid-knowledge:"

func (r PublicationRef) Validate() error {
	var p problems
	if !instancePattern.MatchString(r.Instance) {
		p.add("instance", "must be an instance identity")
	}
	if !snapshotPattern.MatchString(string(r.Snapshot)) {
		p.add("snapshot", "must be a snapshot token")
	}
	checkKey(&p, "key", r.Key)
	return p.err()
}

// String is the reference's URI: polaroid-knowledge:<instance>/<snapshot>#<key>.
func (r PublicationRef) String() string {
	return publicationScheme + r.Instance + "/" + string(r.Snapshot) + "#" + r.Key
}

func ParsePublicationRef(s string) (PublicationRef, error) {
	rest, ok := strings.CutPrefix(s, publicationScheme)
	inst, rest, ok2 := strings.Cut(rest, "/")
	snap, key, ok3 := strings.Cut(rest, "#")
	r := PublicationRef{Instance: inst, Snapshot: Snapshot(snap), Key: key}
	if !ok || !ok2 || !ok3 {
		return PublicationRef{}, &ValidationError{Problems: []Problem{{Field: "publication", Message: "must be " + publicationScheme + "<instance>/<snapshot>#<key>"}}}
	}
	return r, r.Validate()
}

// Origin says how a published snapshot came to this instance.
type Origin string

const (
	OriginLocal    Origin = "local"    // published here
	OriginImported Origin = "imported" // activated from another instance
)

// Origin classifies a published snapshot from how it arrived, never from its
// author or trailers: a commit that claims this instance but was activated
// from elsewhere is imported.
func (k *Kernel) Origin(ctx context.Context, s Snapshot) (Origin, error) {
	imported, err := k.store.Imported(ctx, s)
	if err != nil {
		return "", err
	}
	if imported {
		return OriginImported, nil
	}
	return OriginLocal, nil
}

// Instance is this store's instance identity.
func (k *Kernel) Instance(ctx context.Context) (string, error) {
	id, err := k.store.Instance(ctx)
	if err == nil && !instancePattern.MatchString(id) {
		err = fmt.Errorf("%w: instance identity %q", ErrCorrupt, id)
	}
	return id, err
}

// MapInputs gives a child its inputs: each member of a reference's mapping
// takes a parent input ({"input": name}) or a constant ({"value": v}), in
// the mapping's order; a parent input that is absent leaves the child's
// input absent. It is the knowledge path's form of ADR-0024's mapping.
func MapInputs(mapping, parent jsontext.Value) (jsontext.Value, error) {
	var from map[string]jsontext.Value
	if len(parent) > 0 {
		if err := json.Unmarshal(parent, &from); err != nil {
			return nil, fmt.Errorf("%w: inputs must be a JSON object", ErrInvalid)
		}
	}
	if len(mapping) == 0 {
		return jsontext.Value("{}"), nil
	}
	var out bytes.Buffer
	enc := jsontext.NewEncoder(&out)
	dec := jsontext.NewDecoder(bytes.NewReader(mapping))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return nil, fmt.Errorf("%w: a mapping must be a JSON object", ErrInvalid)
	}
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return nil, err
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, fmt.Errorf("%w: mapping: %w", ErrInvalid, err)
		}
		name := tok.String()
		var source struct {
			Input *string        `json:"input"`
			Value jsontext.Value `json:"value"`
		}
		if err := json.UnmarshalDecode(dec, &source, json.RejectUnknownMembers(true)); err != nil || (source.Input == nil) == (source.Value == nil) {
			return nil, fmt.Errorf("%w: mapping %s %s", ErrInvalid, name, mappingRule)
		}
		value := source.Value
		if source.Input != nil {
			v, ok := from[*source.Input]
			if !ok {
				continue
			}
			value = v
		}
		if err := enc.WriteToken(jsontext.String(name)); err != nil {
			return nil, err
		}
		if err := enc.WriteValue(value); err != nil {
			return nil, err
		}
	}
	if err := enc.WriteToken(jsontext.EndObject); err != nil {
		return nil, err
	}
	return jsontext.Value(bytes.TrimSpace(out.Bytes())), nil
}
