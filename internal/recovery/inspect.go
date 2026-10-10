package recovery

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

// Report is what inspect-backup prints: a backup checked without restoring
// it or changing any of its files.
type Report struct {
	Path                   string           `json:"path"`
	Valid                  bool             `json:"valid"`
	Problems               []string         `json:"problems,omitempty"`
	Metadata               *Metadata        `json:"metadata,omitempty"`
	SchemaVersion          int              `json:"schema_version,omitzero"`
	SupportedSchemaVersion int              `json:"supported_schema_version"`
	Integrity              []string         `json:"integrity,omitempty"`
	Records                map[string]int64 `json:"records,omitempty"`
}

// Inspect checks the backup directory dir: its metadata and format version,
// the database file's size and SHA-256, integrity and foreign keys, that the
// recorded schema version is the file's, and that this build supports it.
// It opens the database read-only and immutable, and never repairs.
func Inspect(ctx context.Context, dir string) (r Report) {
	r.Path = dir
	problem := func(format string, args ...any) { r.Problems = append(r.Problems, fmt.Sprintf(format, args...)) }
	defer func() { r.Valid = len(r.Problems) == 0 }()

	supported, err := sqlite.SupportedSchema()
	if err != nil {
		problem("this build's migrations: %v", err)
		return r
	}
	r.SupportedSchemaVersion = supported
	if abs, err := filepath.Abs(dir); err == nil {
		r.Path = abs
	}
	if st, err := os.Stat(r.Path); err != nil || !st.IsDir() {
		problem("%s is not a backup directory", r.Path)
		return r
	}
	b, err := os.ReadFile(filepath.Join(r.Path, MetadataFile))
	if err != nil {
		problem("%s is missing or unreadable, so this is not a complete backup: %v", MetadataFile, err)
		return r
	}
	var m Metadata
	var head struct {
		Format        string `json:"format"`
		FormatVersion int    `json:"format_version"`
	}
	switch {
	case json.Unmarshal(b, &head) != nil:
		problem("%s is not valid JSON", MetadataFile)
		return r
	case head.Format != Format:
		problem("%s has format %q, want %q", MetadataFile, head.Format, Format)
		return r
	case head.FormatVersion != FormatVersion:
		problem("%s has format_version %d; this polaroid reads only %d", MetadataFile, head.FormatVersion, FormatVersion)
		return r
	}
	if err := json.Unmarshal(b, &m, json.RejectUnknownMembers(true)); err != nil {
		problem("%s does not match format version %d: %v", MetadataFile, FormatVersion, err)
		return r
	}
	r.Metadata = &m
	if _, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err != nil {
		problem("created_at %q is not a timestamp", m.CreatedAt)
	}
	if m.Database.File != DatabaseFile {
		problem("the metadata names database file %q, want %q", m.Database.File, DatabaseFile)
		return r
	}
	db := filepath.Join(r.Path, DatabaseFile)
	size, sum, err := fileDigest(db)
	if err != nil {
		problem("the database file is missing or unreadable: %v", err)
		return r
	}
	if size != m.Database.Size {
		problem("the database file has %d bytes; the metadata records %d", size, m.Database.Size)
	}
	if sum != m.Database.SHA256 {
		problem("the database file's SHA-256 is %s; the metadata records %s", sum, m.Database.SHA256)
	}
	ex, err := sqlite.Examine(ctx, db)
	if err != nil {
		problem("the database file cannot be read as a database: %v", err)
		return r
	}
	r.SchemaVersion, r.Integrity, r.Records = ex.SchemaVersion, ex.Integrity, ex.Tables
	if !ex.IntegrityOK() {
		problem("the database fails its integrity check: %v; %d foreign key violations", ex.Integrity, ex.ForeignKeyViolations)
	}
	if !ex.Catalog() {
		problem("the database holds no Polaroid catalog (no procedures table)")
	}
	if ex.SchemaVersion != m.SchemaVersion {
		problem("the database has schema version %d; the metadata records %d", ex.SchemaVersion, m.SchemaVersion)
	}
	switch {
	case ex.SchemaVersion < 1:
		problem("schema version %d is not a Polaroid schema", ex.SchemaVersion)
	case ex.SchemaVersion > supported:
		problem("schema version %d is newer than this polaroid supports (%d); use a polaroid that supports it", ex.SchemaVersion, supported)
	}
	return r
}
