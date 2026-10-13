// Package knowledge is Polaroid's living procedural knowledge (ADR-0045):
// each procedure has one stable key and one published definition in a
// snapshot of the whole catalog, references resolve by key inside that same
// snapshot, and every publication is validated as a complete catalog.
//
// It is the Git-native knowledge path. The legacy path, versioned procedure
// records in SQLite (internal/memory), is separate and still the only one
// the daemon and its APIs serve; nothing writes to both. Storage is reached
// only through the Store port, so this package knows no Git types.
package knowledge

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
)

// Snapshot is an opaque token for one published state of the whole catalog.
// The empty Snapshot means that nothing has been published.
type Snapshot string

var (
	ErrNotFound = errors.New("not found")
	// ErrStale: the base snapshot of a proposal is no longer the active one.
	ErrStale   = errors.New("stale snapshot: the active knowledge has changed")
	ErrInvalid = errors.New("invalid knowledge")
	// ErrCorrupt: storage holds damaged data.
	ErrCorrupt = errors.New("knowledge storage is damaged")
)

// Problem is one reason knowledge is invalid, at a field path such as
// "procedures[go.module.verify].references[0].key".
type Problem struct {
	Field   string
	Message string
}

type ValidationError struct{ Problems []Problem }

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Field + ": " + p.Message
	}
	return "invalid knowledge: " + strings.Join(parts, "; ")
}

func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

type problems []Problem

func (p *problems) add(field, format string, args ...any) {
	if len(*p) < maxProblems {
		*p = append(*p, Problem{Field: field, Message: fmt.Sprintf(format, args...)})
	}
}

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return &ValidationError{Problems: p}
}

const maxProblems = 50

// Scope is where a procedure applies, as in ADR-0020.
type Scope string

const (
	ScopeUnspecified Scope = ""
	ScopeShared      Scope = "shared"
	ScopeLocal       Scope = "local"
)

// Applicability is a procedure's declared scope; Repository is set only for
// ScopeLocal.
type Applicability struct {
	Scope      Scope
	Repository string
}

// ApplicableIn reports whether the procedure may be used in repository.
func (a Applicability) ApplicableIn(repository string) bool {
	return a.Scope != ScopeLocal || (repository != "" && a.Repository == repository)
}

// admits reports whether a procedure with applicability a may reference one
// with applicability child: a local procedure admits what applies in its
// repository; any other admits no local procedure.
func (a Applicability) admits(child Applicability) bool {
	if a.Scope == ScopeLocal {
		return child.ApplicableIn(a.Repository)
	}
	return child.Scope != ScopeLocal
}

// Procedure is the one published definition of a procedure. There is no
// version: the snapshot it was read at identifies the state.
type Procedure struct {
	Key           string
	Goal          string
	Philosophy    string
	Method        string
	Applicability Applicability
	// Contract and Instructions are task knowledge, stored and returned as
	// given and never interpreted.
	Contract     jsontext.Value
	Instructions jsontext.Value
	References   []Reference
}

// Reference composes another procedure by key. Inputs maps each child input
// to {"input": "<parent input>"} or {"value": <any JSON>}; Condition, when
// set, says when the reference applies.
type Reference struct {
	Name      string
	Key       string
	Condition *string
	Inputs    jsontext.Value
}
