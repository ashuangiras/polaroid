package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// defaultDBPath returns <home>/.polaroid/data/polaroid.db (ADR-0025), with the
// home directory from userHomeDir, never from the working directory.
func defaultDBPath(userHomeDir func() (string, error)) (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate the per-user database: %w; pass -db or set POLAROID_DB", err)
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("cannot locate the per-user database: home directory %q is not absolute; pass -db or set POLAROID_DB", home)
	}
	return filepath.Join(home, ".polaroid", "data", "polaroid.db"), nil
}

// prepareDefaultDB creates what the default database needs and is missing:
// .polaroid and .polaroid/data with mode 0700, and an empty database file with
// mode 0600 before SQLite opens it, because SQLite gives its -wal and -shm
// files the database file's mode. It changes no existing entry, and returns
// the existing ones that grant group or other access.
func prepareDefaultDB(path string) (exposed []string, err error) {
	data := filepath.Dir(path)
	root := filepath.Dir(data)
	home := filepath.Dir(root)
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return nil, fmt.Errorf("home directory %s is not usable for the per-user database: %w; pass -db or set POLAROID_DB", home, err)
	}
	for _, dir := range []string{root, data} {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	for _, p := range []string{root, data, path} {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if st.Mode().Perm()&0o077 != 0 {
			exposed = append(exposed, p)
		}
	}
	return exposed, nil
}
