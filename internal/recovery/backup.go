package recovery

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	"github.com/ashuangiras/polaroid/internal/version"
)

// BackupRequest says what to back up and where.
type BackupRequest struct {
	Source  string // absolute path of the catalog
	Dir     string // absolute directory the backup is created in
	Managed bool   // whether Source is the managed service's catalog
	Reason  string // ReasonBackup or ReasonPreRestore
	Now     func() time.Time
}

// BackupResult is what backup prints.
type BackupResult struct {
	Path     string   `json:"path"`
	Metadata Metadata `json:"metadata"`
}

// Backup snapshots the catalog into a private staging directory under
// req.Dir, validates the snapshot, writes its metadata, and publishes the
// directory under a new name (ADR-0030). It returns an error, and leaves no
// staging files, unless the published backup reads back valid.
func Backup(ctx context.Context, req BackupRequest) (BackupResult, error) {
	var res BackupResult
	now := time.Now
	if req.Now != nil {
		now = req.Now
	}
	if req.Reason == "" {
		req.Reason = ReasonBackup
	}
	if !filepath.IsAbs(req.Source) || !filepath.IsAbs(req.Dir) {
		return res, fmt.Errorf("the catalog %q and the backup directory %q must be absolute paths", req.Source, req.Dir)
	}
	supported, err := sqlite.SupportedSchema()
	if err != nil {
		return res, err
	}
	if st, err := os.Stat(req.Source); err != nil {
		return res, fmt.Errorf("no catalog to back up: %w", err)
	} else if !st.Mode().IsRegular() {
		return res, fmt.Errorf("the catalog %s is not a regular file", req.Source)
	}
	if err := mkdirPrivate(req.Dir); err != nil {
		return res, err
	}
	stage, err := os.MkdirTemp(req.Dir, ".staging-")
	if err != nil {
		return res, fmt.Errorf("create a staging directory in %s: %w", req.Dir, err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()

	db := filepath.Join(stage, DatabaseFile)
	f, err := createPrivate(db)
	if err != nil {
		return res, fmt.Errorf("create the snapshot file: %w", err)
	}
	if err := f.Close(); err != nil {
		return res, err
	}
	created := now().UTC()
	if err := sqlite.Snapshot(ctx, req.Source, db); err != nil {
		return res, err
	}
	ex, err := sqlite.Examine(ctx, db)
	switch {
	case err != nil:
		return res, fmt.Errorf("the snapshot of %s cannot be read: %w", req.Source, err)
	case !ex.IntegrityOK():
		return res, fmt.Errorf("the snapshot of %s fails its integrity check: %v; %d foreign key violations", req.Source, ex.Integrity, ex.ForeignKeyViolations)
	case !ex.Catalog():
		return res, fmt.Errorf("%s is not a Polaroid catalog (no procedures table)", req.Source)
	case ex.SchemaVersion < 1 || ex.SchemaVersion > supported:
		return res, fmt.Errorf("the catalog %s has schema version %d; this polaroid supports 1 to %d", req.Source, ex.SchemaVersion, supported)
	}
	if err := syncFile(db); err != nil {
		return res, fmt.Errorf("sync the snapshot: %w", err)
	}
	size, sum, err := fileDigest(db)
	if err != nil {
		return res, err
	}
	host, _ := os.Hostname()
	b := version.Current("polaroid")
	m := Metadata{
		Format: Format, FormatVersion: FormatVersion, CreatedAt: created.Format(time.RFC3339Nano), Reason: req.Reason,
		Source:        Source{Path: req.Source, Managed: req.Managed, Host: host},
		SchemaVersion: ex.SchemaVersion,
		Polaroid:      Build{Version: b.Version, Revision: b.Revision, Modified: b.Modified, Go: b.Go},
		Database:      DatabaseInfo{File: DatabaseFile, Size: size, SHA256: sum},
		Records:       ex.Tables,
	}
	if err := writeMetadata(filepath.Join(stage, MetadataFile), m); err != nil {
		return res, err
	}
	if err := syncDir(stage); err != nil {
		return res, fmt.Errorf("sync %s: %w", stage, err)
	}
	final, err := publish(stage, req.Dir, backupName(created, req.Reason))
	if err != nil {
		return res, err
	}
	published = true
	if err := syncDir(req.Dir); err != nil {
		return res, fmt.Errorf("sync %s: %w", req.Dir, err)
	}
	if r := Inspect(ctx, final); !r.Valid {
		return res, fmt.Errorf("the published backup %s reads back invalid: %v", final, r.Problems)
	}
	return BackupResult{Path: final, Metadata: m}, nil
}

func backupName(t time.Time, reason string) string {
	name := "polaroid-" + t.Format("20060102T150405Z")
	if reason != ReasonBackup {
		name += "-" + reason
	}
	return name
}

func writeMetadata(path string, m Metadata) error {
	b, err := json.Marshal(m, json.Deterministic(true))
	if err != nil {
		return err
	}
	f, err := createPrivate(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	_, werr := f.Write(append(b, '\n'))
	if err := errors.Join(werr, f.Sync(), f.Close()); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// publish renames stage into dir under name, or name-2, name-3 and so on:
// never over an existing backup. rename(2) refuses a non-empty directory as
// its target on Linux and macOS, and a published backup is never empty, so a
// name taken concurrently is skipped, not replaced.
func publish(stage, dir, name string) (string, error) {
	for i := 1; i <= 100; i++ {
		final := filepath.Join(dir, name)
		if i > 1 {
			final += "-" + strconv.Itoa(i)
		}
		if taken, err := exists(final); err != nil {
			return "", err
		} else if taken {
			continue
		}
		if err := os.Rename(stage, final); err != nil {
			if taken, _ := exists(final); taken {
				continue
			}
			return "", fmt.Errorf("publish the backup as %s: %w", final, err)
		}
		return final, nil
	}
	return "", fmt.Errorf("no free backup name for %s in %s", name, dir)
}
