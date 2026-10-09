package memory

import (
	"encoding/json/jsontext"
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
	switch c.VersionPolicy.Kind {
	case PolicyPin:
		if c.VersionPolicy.Pin < 1 {
			p.add("revision.version_policy.pin", "must be a version number of at least 1")
		}
	case PolicyContextual:
		if c.VersionPolicy.Pin != 0 {
			p.add("revision.version_policy", "must not pin a version for a contextual policy")
		}
	default:
		p.add("revision.version_policy", "must set exactly one of pin or contextual")
	}
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
