// Package memory defines Polaroid's generic procedure and binding records,
// their identity and versioning rules, and the operations agents perform on
// them.
//
// The package knows nothing about HTTP or the storage engine. Task knowledge
// lives in record content (philosophy, method, contract, instructions, binding
// inputs); this package validates the shape of that content but never
// interprets it.
package memory

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"time"
)

// Procedure is the stable identity of a reusable procedure. Its ID and
// canonical key never change after creation and are independent of any
// repository.
type Procedure struct {
	ID            string
	CanonicalKey  string
	CreatedAt     time.Time
	LatestVersion int
}

// Definition is the author-supplied content of one procedure version.
// Contract and Instructions are JSON objects whose members are task knowledge.
type Definition struct {
	Philosophy     string
	Method         string
	Contract       jsontext.Value
	Instructions   jsontext.Value
	RevisionReason string
}

// Version is one immutable definition of a procedure. Versions are numbered
// contiguously from 1 within their procedure.
type Version struct {
	ProcedureID string
	Number      int
	CreatedAt   time.Time
	Definition
}

// History is a procedure together with all of its versions, oldest first.
type History struct {
	Procedure Procedure
	Versions  []Version
}

// NewProcedure asks to create a procedure and its first version.
type NewProcedure struct {
	CanonicalKey string
	Definition   Definition
}

// Revision asks to append a version derived from BaseVersion, which must be
// the procedure's latest version at the time the revision is stored.
type Revision struct {
	BaseVersion int
	Definition  Definition
}

var (
	// ErrNotFound reports that a requested record does not exist.
	ErrNotFound = errors.New("not found")
	// ErrCanonicalKeyExists reports that another procedure already uses the
	// requested canonical key.
	ErrCanonicalKeyExists = errors.New("canonical key already exists")
)

// VersionConflictError reports that a revision's base version is not the
// procedure's latest version, so storing it could silently discard another
// revision. The caller should re-read the latest version and revise again.
type VersionConflictError struct {
	BaseVersion   int
	LatestVersion int
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("base version %d is not the latest version (latest is %d)", e.BaseVersion, e.LatestVersion)
}

// FieldProblem describes one invalid input field. Field uses the record
// contract's field names, with "." separating nested names.
type FieldProblem struct {
	Field   string
	Message string
}

// ValidationError lists every problem found in an input.
type ValidationError struct {
	Problems []FieldProblem
}

func (e *ValidationError) Error() string {
	msg := "invalid input"
	for i, p := range e.Problems {
		sep := "; "
		if i == 0 {
			sep = ": "
		}
		msg += sep + p.Field + " " + p.Message
	}
	return msg
}
