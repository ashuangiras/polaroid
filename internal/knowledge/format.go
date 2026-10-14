package knowledge

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"slices"
)

// ManifestPath holds a snapshot's format manifest. A snapshot without one
// is format 1: procedures only, read exactly as before ADR-0050.
const ManifestPath = "corpus.json"

// FormatRecords is the format of a snapshot that holds typed records.
const FormatRecords = 2

// Capabilities are what this build implements of format 2. A manifest that
// requires anything else is refused, so knowledge from a newer instance is
// never read with a meaning it does not have here.
var Capabilities = []string{"records/1", "rules/corpus-logic-1"}

type manifest struct {
	Format       int      `json:"format"`
	Capabilities []string `json:"capabilities"`
}

func encodeManifest() []byte {
	b, _ := json.Marshal(manifest{Format: FormatRecords, Capabilities: Capabilities})
	return append(b, '\n')
}

func checkManifest(p *problems, b []byte) {
	var m manifest
	if err := json.Unmarshal(b, &m, json.RejectUnknownMembers(true)); err != nil {
		p.add(ManifestPath, "%v", err)
		return
	}
	if m.Format != FormatRecords {
		p.add(ManifestPath, "format %d is not supported by this instance (it reads format 1 and %d)", m.Format, FormatRecords)
		return
	}
	for _, c := range m.Capabilities {
		if !slices.Contains(Capabilities, c) {
			p.add(ManifestPath, "capability %q is not supported by this instance (it supports %v)", c, Capabilities)
		}
	}
	if len(*p) == 0 && !bytes.Equal(encodeManifest(), b) {
		p.add(ManifestPath, "is not in canonical form")
	}
}

// RecordDocument is a record with its canonical document.
type RecordDocument struct {
	Record   *Record
	Document []byte
}

// RecordValidator checks what the kernel cannot without semantic libraries:
// that the records' JSON-LD is valid under the bundled context, that its
// RDF projection states exactly the decoded relationships, and that the
// graph conforms to the bundled shapes. internal/knowledge/semantic
// implements it. Its problems are reported as a ValidationError.
type RecordValidator interface {
	ValidateRecords(ctx context.Context, docs []RecordDocument) error
}

// Option configures a Kernel.
type Option func(*Kernel)

// WithRecordValidator lets the kernel read and publish format-2 snapshots.
// Without one, a snapshot holding records is refused. Several validators
// may be given; every one must accept.
func WithRecordValidator(v RecordValidator) Option {
	return func(k *Kernel) { k.validators = append(k.validators, v) }
}

func validateRecords(ctx context.Context, vs []RecordValidator, records map[string]*Record) error {
	if len(records) == 0 {
		return nil
	}
	if len(vs) == 0 {
		return &ValidationError{Problems: []Problem{{Field: "records", Message: "this instance has no semantic validator, so it cannot accept typed records"}}}
	}
	docs := make([]RecordDocument, 0, len(records))
	for _, k := range sortedRecordKeys(records) {
		b, err := EncodeRecord(records[k])
		if err != nil {
			return fmt.Errorf("records[%s]: %w", k, err)
		}
		docs = append(docs, RecordDocument{Record: records[k].Clone(), Document: b})
	}
	for _, v := range vs {
		if err := v.ValidateRecords(ctx, docs); err != nil {
			return err
		}
	}
	return nil
}
