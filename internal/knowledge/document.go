package knowledge

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"
)

// The catalog is laid out as one JSON document per procedure, at
// procedures/<first key segment>/<key>.json: file names are exact keys, and
// the first segment spreads the tree by namespace.
const proceduresDir = "procedures"

// Path is where a procedure's document lives in a snapshot.
func Path(key string) string {
	first := key
	if i := strings.IndexAny(key, "._-"); i > 0 {
		first = key[:i]
	}
	return proceduresDir + "/" + first + "/" + key + ".json"
}

// keyOf is the inverse of Path; ok is false for any other path.
func keyOf(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, proceduresDir+"/")
	if !ok {
		return "", false
	}
	dir, file, ok := strings.Cut(rest, "/")
	key, isJSON := strings.CutSuffix(file, ".json")
	if !ok || !isJSON || !validKey(key) || Path(key) != proceduresDir+"/"+dir+"/"+file {
		return "", false
	}
	return key, true
}

type document struct {
	Key           string             `json:"key"`
	Goal          string             `json:"goal,omitzero"`
	Philosophy    string             `json:"philosophy"`
	Method        string             `json:"method"`
	Applicability *applicabilityJSON `json:"applicability,omitzero"`
	Contract      jsontext.Value     `json:"contract"`
	Instructions  jsontext.Value     `json:"instructions"`
	References    []referenceJSON    `json:"references,omitzero"`
}

type applicabilityJSON struct {
	Shared     *struct{} `json:"shared,omitzero"`
	Repository string    `json:"repository,omitzero"`
}

type referenceJSON struct {
	Name      string         `json:"name"`
	Key       string         `json:"key"`
	Condition *string        `json:"condition,omitzero"`
	Inputs    jsontext.Value `json:"inputs,omitzero"`
}

// Encode returns the canonical document of p: compact JSON with members in a
// fixed order, free-form objects compacted with their member order kept, and
// one trailing newline. Equal procedures encode to equal bytes.
func Encode(p *Procedure) ([]byte, error) {
	d := document{Key: p.Key, Goal: p.Goal, Philosophy: p.Philosophy, Method: p.Method}
	switch p.Applicability.Scope {
	case ScopeShared:
		d.Applicability = &applicabilityJSON{Shared: &struct{}{}}
	case ScopeLocal:
		d.Applicability = &applicabilityJSON{Repository: p.Applicability.Repository}
	}
	var err error
	if d.Contract, err = compact(p.Contract); err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	if d.Instructions, err = compact(p.Instructions); err != nil {
		return nil, fmt.Errorf("instructions: %w", err)
	}
	for i, r := range p.References {
		rj := referenceJSON{Name: r.Name, Key: r.Key, Condition: r.Condition}
		if len(r.Inputs) > 0 {
			if rj.Inputs, err = compact(r.Inputs); err != nil {
				return nil, fmt.Errorf("references[%d].inputs: %w", i, err)
			}
		}
		d.References = append(d.References, rj)
	}
	b, err := json.Marshal(&d)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func compact(v jsontext.Value) (jsontext.Value, error) {
	c := v.Clone()
	if err := c.Compact(); err != nil {
		return nil, err
	}
	return c, nil
}

// Decode parses a stored document strictly: unknown or duplicate members,
// trailing data and non-canonical bytes are refused, so a stored document
// has exactly one spelling.
func Decode(b []byte) (*Procedure, error) {
	if len(b) > maxDocumentBytes {
		return nil, fmt.Errorf("document of %d bytes exceeds %d", len(b), maxDocumentBytes)
	}
	var d document
	if err := json.Unmarshal(b, &d, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	p := &Procedure{Key: d.Key, Goal: d.Goal, Philosophy: d.Philosophy, Method: d.Method, Contract: d.Contract, Instructions: d.Instructions}
	if a := d.Applicability; a != nil {
		switch {
		case a.Shared != nil && a.Repository == "":
			p.Applicability = Applicability{Scope: ScopeShared}
		case a.Shared == nil && a.Repository != "":
			p.Applicability = Applicability{Scope: ScopeLocal, Repository: a.Repository}
		default:
			return nil, fmt.Errorf(`applicability must be {"shared": {}} or {"repository": "<repository>"}`)
		}
	}
	for _, r := range d.References {
		p.References = append(p.References, Reference(r))
	}
	again, err := Encode(p)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(again, b) {
		return nil, fmt.Errorf("document is not in canonical form")
	}
	return p, nil
}
