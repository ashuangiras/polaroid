package memory

import (
	"encoding/json/jsontext"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxCanonicalKeyLength is the maximum length of a canonical key in bytes.
const MaxCanonicalKeyLength = 128

// A canonical key is one or more runs of lowercase ASCII letters and digits
// joined by single '.', '-' or '_' separators, e.g. "go.dependency.add".
// Accepting a single spelling makes exact-match uniqueness meaningful.
var canonicalKeyPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

type problems []FieldProblem

func (p *problems) add(field, message string) {
	*p = append(*p, FieldProblem{Field: field, Message: message})
}

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return &ValidationError{Problems: p}
}

// validate checks n and returns its definition in stored form.
func (n NewProcedure) validate() (Definition, error) {
	var p problems
	switch {
	case n.CanonicalKey == "":
		p.add("canonical_key", "is required")
	case len(n.CanonicalKey) > MaxCanonicalKeyLength:
		p.add("canonical_key", fmt.Sprintf("must be at most %d characters", MaxCanonicalKeyLength))
	case !canonicalKeyPattern.MatchString(n.CanonicalKey):
		p.add("canonical_key", "must be lowercase letters and digits joined by single '.', '-' or '_' characters")
	}
	def := checkDefinition(&p, n.Definition)
	return def, p.err()
}

// validate checks r and returns its definition in stored form.
func (r Revision) validate() (Definition, error) {
	var p problems
	if r.BaseVersion < 1 {
		p.add("base_version", "must be a version number of at least 1")
	}
	def := checkDefinition(&p, r.Definition)
	return def, p.err()
}

func checkDefinition(p *problems, d Definition) Definition {
	checkText(p, "version.philosophy", d.Philosophy)
	checkText(p, "version.method", d.Method)
	d.Contract = checkObject(p, "version.contract", d.Contract)
	d.Instructions = checkObject(p, "version.instructions", d.Instructions)
	checkText(p, "version.revision_reason", d.RevisionReason)
	return d
}

func checkText(p *problems, field, s string) {
	switch {
	case strings.TrimSpace(s) == "":
		p.add(field, "is required")
	case !utf8.ValidString(s):
		p.add(field, "must be valid UTF-8")
	}
}

// checkObject accepts any JSON object and returns a compacted copy. Member
// names and values are task knowledge and are deliberately not interpreted.
func checkObject(p *problems, field string, v jsontext.Value) jsontext.Value {
	if len(v) == 0 {
		p.add(field, "is required")
		return nil
	}
	c := v.Clone()
	if err := c.Compact(); err != nil || !c.IsValid() {
		p.add(field, "must be valid JSON with unique member names")
		return nil
	}
	if c.Kind() != jsontext.KindBeginObject {
		p.add(field, "must be a JSON object")
		return nil
	}
	return c
}
