package knowledge

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

// Limits shared with the legacy catalog (internal/memory), so that its
// procedures map one to one onto keys here.
const (
	maxKeyBytes        = 128
	maxRepositoryBytes = 255
	maxLineBytes       = 2 << 10  // goal
	maxTextBytes       = 8 << 10  // philosophy, method, condition
	maxObjectBytes     = 16 << 10 // reference inputs
	maxDocumentBytes   = 256 << 10
	maxJSONBytes       = 64 << 10 // contract, instructions
	MaxGraphDepth      = 32
	MaxGraphNodes      = 2048
	MaxProcedures      = 50_000
)

var (
	keyPattern        = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	repositoryPattern = regexp.MustCompile(`^[a-z0-9._-]+(?:/[a-z0-9._-]+)*$`)
)

func validKey(s string) bool { return len(s) <= maxKeyBytes && keyPattern.MatchString(s) }

func checkKey(p *problems, field, s string) {
	switch {
	case s == "":
		p.add(field, "is required")
	case !validKey(s):
		p.add(field, "must be at most %d lowercase letters and digits joined by single '.', '-' or '_' characters", maxKeyBytes)
	}
}

func checkText(p *problems, field, s string, max int) {
	switch {
	case strings.TrimSpace(s) == "":
		p.add(field, "is required")
	case !utf8.ValidString(s):
		p.add(field, "must be valid UTF-8")
	case len(s) > max:
		p.add(field, "must be at most %d bytes", max)
	}
}

func checkObject(p *problems, field string, v jsontext.Value, max int) {
	switch {
	case len(v) == 0:
		p.add(field, "is required")
	case !v.IsValid():
		p.add(field, "must be valid JSON with unique member names")
	case v.Kind() != jsontext.KindBeginObject:
		p.add(field, "must be a JSON object")
	default:
		c := v.Clone()
		if c.Compact() == nil && len(c) > max {
			p.add(field, "must be at most %d bytes of compact JSON", max)
		}
	}
}

func checkRepository(p *problems, field, s string) {
	switch {
	case s == "":
		p.add(field, "is required")
	case len(s) > maxRepositoryBytes || !repositoryPattern.MatchString(s) || strings.HasSuffix(s, ".git") ||
		slices.ContainsFunc(strings.Split(s, "/"), func(seg string) bool { return seg == "." || seg == ".." }):
		p.add(field, "must be a repository identifier such as github.com/owner/repo")
	}
}

const mappingRule = `must be {"input": "<parent input name>"} or {"value": <any JSON>}`

func checkMapping(p *problems, field string, v jsontext.Value) {
	checkObject(p, field, v, maxObjectBytes)
	var members map[string]jsontext.Value
	if json.Unmarshal(v, &members) != nil {
		return
	}
	for name, source := range members {
		if name == "" {
			p.add(field, "child input names must not be empty")
			continue
		}
		var src map[string]jsontext.Value
		if json.Unmarshal(source, &src) != nil || len(src) != 1 {
			p.add(field+"."+name, mappingRule)
			continue
		}
		if _, ok := src["value"]; ok {
			continue
		}
		var parent string
		if in, ok := src["input"]; !ok || json.Unmarshal(in, &parent) != nil || strings.TrimSpace(parent) == "" {
			p.add(field+"."+name, mappingRule)
		}
	}
}

// checkProcedure checks one procedure on its own; references are checked
// against the catalog by checkCatalog.
func checkProcedure(p *problems, field string, proc *Procedure) {
	checkKey(p, field+".key", proc.Key)
	if proc.Goal != "" {
		checkText(p, field+".goal", proc.Goal, maxLineBytes)
		if strings.ContainsAny(proc.Goal, "\r\n") {
			p.add(field+".goal", "must be one line")
		}
	}
	checkText(p, field+".philosophy", proc.Philosophy, maxTextBytes)
	checkText(p, field+".method", proc.Method, maxTextBytes)
	switch proc.Applicability.Scope {
	case ScopeUnspecified, ScopeShared:
		if proc.Applicability.Repository != "" {
			p.add(field+".applicability", "names a repository only when local")
		}
	case ScopeLocal:
		checkRepository(p, field+".applicability.repository", proc.Applicability.Repository)
	default:
		p.add(field+".applicability", "must be shared, local or unspecified")
	}
	checkObject(p, field+".contract", proc.Contract, maxJSONBytes)
	checkObject(p, field+".instructions", proc.Instructions, maxJSONBytes)
	names := map[string]bool{}
	for i, r := range proc.References {
		rf := fmt.Sprintf("%s.references[%d]", field, i)
		checkKey(p, rf+".name", r.Name)
		if names[r.Name] {
			p.add(rf+".name", "duplicates the name of an earlier reference")
		}
		names[r.Name] = true
		checkKey(p, rf+".key", r.Key)
		if r.Key == proc.Key {
			p.add(rf+".key", "a procedure cannot reference itself")
		}
		if len(r.Inputs) > 0 {
			checkMapping(p, rf+".inputs", r.Inputs)
		}
		if r.Condition != nil {
			checkText(p, rf+".condition", *r.Condition, maxTextBytes)
		}
	}
}

// checkCatalog checks a complete catalog: every procedure, every reference
// target, applicability admission, cycles, depth and expanded size, and
// every record against the whole catalog.
func checkCatalog(procs map[string]*Procedure, records map[string]*Record) error {
	var p problems
	if len(procs)+len(records) > MaxProcedures {
		p.add("procedures", "%d procedures and records exceed %d", len(procs)+len(records), MaxProcedures)
		return p.err()
	}
	keys := sortedKeys(procs)
	for _, k := range keys {
		proc := procs[k]
		field := "procedures[" + k + "]"
		if proc.Key != k {
			p.add(field+".key", "is %q, but the document is stored as %q", proc.Key, k)
		}
		checkProcedure(&p, field, proc)
		for i, r := range proc.References {
			child, ok := procs[r.Key]
			switch {
			case !ok:
				if validKey(r.Key) && r.Key != proc.Key {
					p.add(fmt.Sprintf("%s.references[%d].key", field, i), "names %s, which is not in the catalog", r.Key)
				}
			case !proc.Applicability.admits(child.Applicability):
				p.add(fmt.Sprintf("%s.references[%d].key", field, i), "%s does not apply wherever %s applies", r.Key, k)
			}
		}
	}
	if len(p) == 0 {
		checkGraph(&p, procs, keys)
	}
	if len(p) == 0 && len(records) > 0 {
		checkRecords(&p, procs, records)
	}
	return p.err()
}

// checkGraph finds cycles and composition graphs deeper than MaxGraphDepth
// or with more than MaxGraphNodes nodes once expanded, as the legacy graph
// rules do (ADR-0009).
func checkGraph(p *problems, procs map[string]*Procedure, keys []string) {
	const visiting = -1
	depth, nodes := map[string]int{}, map[string]int{}
	var visit func(k string, path []string) bool
	visit = func(k string, path []string) bool {
		switch d := depth[k]; {
		case d == visiting:
			i := slices.Index(path, k)
			p.add("procedures["+k+"].references", "reference cycle: %s", strings.Join(append(path[i:], k), " -> "))
			return false
		case d > 0:
			return true
		}
		depth[k] = visiting
		d, n := 1, 1
		for _, r := range procs[k].References {
			if !visit(r.Key, append(path, k)) {
				return false
			}
			d = max(d, 1+depth[r.Key])
			n = min(n+nodes[r.Key], MaxGraphNodes+1)
		}
		depth[k], nodes[k] = d, n
		return true
	}
	for _, k := range keys {
		if !visit(k, nil) {
			return
		}
		switch {
		case depth[k] > MaxGraphDepth:
			p.add("procedures["+k+"].references", "composition is %d levels deep; at most %d", depth[k], MaxGraphDepth)
		case nodes[k] > MaxGraphNodes:
			p.add("procedures["+k+"].references", "composition expands to more than %d procedures", MaxGraphNodes)
		}
	}
}

func sortedKeys(procs map[string]*Procedure) []string {
	keys := make([]string, 0, len(procs))
	for k := range procs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// equalDocs reports whether two procedures encode identically.
func equalDocs(a, b *Procedure) bool {
	x, err1 := Encode(a)
	y, err2 := Encode(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}
