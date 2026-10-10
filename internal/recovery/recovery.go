// Package recovery backs up, inspects and restores a Polaroid catalog
// (ADR-0030). It owns the backup format, staging and publication, and the
// order of a restore; SQLite access is internal/storage/sqlite's, and the
// managed service is internal/lifecycle's. It knows nothing of procedures.
package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// The backup format: a directory holding DatabaseFile and MetadataFile.
const (
	Format        = "polaroid-backup"
	FormatVersion = 1
	DatabaseFile  = "polaroid.db"
	MetadataFile  = "backup.json"
)

// Reasons a backup was taken.
const (
	ReasonBackup     = "backup"
	ReasonPreRestore = "pre-restore"
)

// Metadata is backup.json.
type Metadata struct {
	Format        string           `json:"format"`
	FormatVersion int              `json:"format_version"`
	CreatedAt     string           `json:"created_at"`
	Reason        string           `json:"reason"`
	Source        Source           `json:"source"`
	SchemaVersion int              `json:"schema_version"`
	Polaroid      Build            `json:"polaroid"`
	Database      DatabaseInfo     `json:"database"`
	Records       map[string]int64 `json:"records"`
}

// Source identifies the catalog a backup was taken from.
type Source struct {
	Path    string `json:"path"`
	Managed bool   `json:"managed"`
	Host    string `json:"host"`
}

// Build is the polaroid that took the backup.
type Build struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Modified bool   `json:"modified"`
	Go       string `json:"go"`
}

// DatabaseInfo identifies the backup's database file.
type DatabaseInfo struct {
	File   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func fileDigest(path string) (size int64, sum string, err error) {
	f, err := os.Open(path) //nolint:gosec // paths inside a backup or staging directory
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	size, err = io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

// createPrivate creates path, which must not exist, with mode 0600.
func createPrivate(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // staging paths this package chose
	if err != nil {
		return nil, err
	}
	// The umask may have narrowed the mode, never widened it; make it exact.
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // staging paths this package chose
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// syncDir makes a rename or link in dir durable where the platform's fsync
// does (ADR-0030: on macOS it does not force the drive's cache).
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // a directory this package wrote into
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// mkdirPrivate creates dir and any missing parents with mode 0700; existing
// directories are left as they are.
func mkdirPrivate(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return nil
}

// exists reports whether path exists, without following a final symlink.
func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// SamePath reports whether a and b name the same file: equal once cleaned,
// or once symlinks are resolved where both exist.
func SamePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
