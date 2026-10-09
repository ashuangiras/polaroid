package memory

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// MaxCanonicalKeyLength is the maximum length of a canonical key in bytes.
const MaxCanonicalKeyLength = 128

// A canonical key is one or more runs of lowercase ASCII letters and digits
// joined by single '.', '-' or '_' separators, e.g. "go.dependency.add".
// Accepting a single spelling makes exact-match uniqueness meaningful.
var canonicalKeyPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

// MaxRepositoryLength is the maximum length of a repository identifier in
// bytes.
const MaxRepositoryLength = 255

// A repository identifier is a canonical path such as
// "github.com/ashuangiras/polaroid" or a bare name such as "scratch"; see
// ADR-0007.
var repositoryPattern = regexp.MustCompile(`^[a-z0-9._-]+(?:/[a-z0-9._-]+)*$`)

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
	checkKey(&p, "canonical_key", n.CanonicalKey)
	if n.Origin != nil {
		checkOrigin(&p, "origin", *n.Origin)
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
	if d.Goal != "" {
		checkLine(p, "version.goal", d.Goal)
	}
	checkApplicability(p, "version.applicability", d.Applicability)
	d.Contract = checkObject(p, "version.contract", d.Contract)
	d.Instructions = checkObject(p, "version.instructions", d.Instructions)
	d.References = checkReferences(p, d.References)
	checkText(p, "version.revision_reason", d.RevisionReason)
	return d
}

// checkReferences returns a copy of refs in stored form, or nil if there are
// none, so an empty list and an absent one are the same.
func checkReferences(p *problems, refs []Reference) []Reference {
	if len(refs) == 0 {
		return nil
	}
	out := make([]Reference, len(refs))
	seen := make(map[string]bool, len(refs))
	for i, r := range refs {
		field := fmt.Sprintf("version.references[%d]", i)
		checkKey(p, field+".name", r.Name)
		if r.Name != "" && seen[r.Name] {
			p.add(field+".name", "duplicates the name of an earlier reference")
		}
		seen[r.Name] = true
		if r.ProcedureID == "" {
			p.add(field+".procedure_id", "is required")
		}
		checkPolicy(p, field+".version_policy", r.VersionPolicy)
		r.Inputs = checkMapping(p, field+".inputs", r.Inputs)
		out[i] = r
	}
	return out
}

const mappingRule = `must be {"input": "<parent input name>"} or {"value": <any JSON>}`

// checkMapping accepts a JSON object whose members each map a child input to
// a parent input or a literal value, and returns it compacted. Names are not
// checked against any contract, which Polaroid never interprets.
func checkMapping(p *problems, field string, v jsontext.Value) jsontext.Value {
	c := checkObject(p, field, v)
	if c == nil {
		return nil
	}
	dec := jsontext.NewDecoder(bytes.NewReader(c))
	if _, err := dec.ReadToken(); err != nil {
		p.add(field, "must be a JSON object")
		return nil
	}
	for dec.PeekKind() != jsontext.KindEndObject {
		tok, err := dec.ReadToken()
		if err != nil {
			break
		}
		name := tok.String()
		source, err := dec.ReadValue()
		if err != nil {
			break
		}
		switch {
		case name == "":
			p.add(field, "child input names must not be empty")
		case !validInputSource(source):
			p.add(field+"."+name, mappingRule)
		}
	}
	return c
}

func validInputSource(v jsontext.Value) bool {
	var members map[string]jsontext.Value
	if json.Unmarshal(v, &members) != nil || len(members) != 1 {
		return false
	}
	if _, ok := members["value"]; ok {
		return true
	}
	var parent string
	src, ok := members["input"]
	return ok && json.Unmarshal(src, &parent) == nil && strings.TrimSpace(parent) != ""
}

func checkPolicy(p *problems, field string, vp VersionPolicy) {
	switch vp.Kind {
	case PolicyPin:
		if vp.Pin < 1 {
			p.add(field+".pin", "must be a version number of at least 1")
		}
	case PolicyContextual:
		if vp.Pin != 0 {
			p.add(field, "must not pin a version for a contextual policy")
		}
	default:
		p.add(field, "must set exactly one of pin or contextual")
	}
}

// validate checks n and returns its configuration in stored form.
func (n NewBinding) validate() (BindingConfig, error) {
	var p problems
	checkRepository(&p, "repository", n.Repository)
	checkKey(&p, "name", n.Name)
	if n.ProcedureID == "" {
		p.add("procedure_id", "is required")
	}
	cfg := checkBindingConfig(&p, n.Config)
	return cfg, p.err()
}

// validate checks r and returns its configuration in stored form.
func (r BindingRevise) validate() (BindingConfig, error) {
	var p problems
	if r.BaseRevision < 1 {
		p.add("base_revision", "must be a revision number of at least 1")
	}
	cfg := checkBindingConfig(&p, r.Config)
	return cfg, p.err()
}

func checkBindingConfig(p *problems, c BindingConfig) BindingConfig {
	c.Inputs = checkObject(p, "revision.inputs", c.Inputs)
	checkPolicy(p, "revision.version_policy", c.VersionPolicy)
	checkText(p, "revision.revision_reason", c.RevisionReason)
	return c
}

// checkKey checks the canonical-key format, which local binding names share.
func checkKey(p *problems, field, s string) {
	switch {
	case s == "":
		p.add(field, "is required")
	case len(s) > MaxCanonicalKeyLength:
		p.add(field, fmt.Sprintf("must be at most %d characters", MaxCanonicalKeyLength))
	case !canonicalKeyPattern.MatchString(s):
		p.add(field, "must be lowercase letters and digits joined by single '.', '-' or '_' characters")
	}
}

func checkRepository(p *problems, field, s string) {
	switch {
	case s == "":
		p.add(field, "is required")
	case len(s) > MaxRepositoryLength:
		p.add(field, fmt.Sprintf("must be at most %d characters", MaxRepositoryLength))
	case !repositoryPattern.MatchString(s):
		p.add(field, "must be '/'-separated segments of lowercase letters, digits, '.', '_' and '-'")
	case slices.ContainsFunc(strings.Split(s, "/"), func(seg string) bool { return seg == "." || seg == ".." }):
		p.add(field, "must not contain '.' or '..' segments")
	case strings.HasSuffix(s, ".git"):
		p.add(field, "must not end in .git")
	}
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
