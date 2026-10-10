package main

import (
	"fmt"
	"os"
	"sync"
)

// logMaxBytes bounds -log-file: the file is rotated to file.1 when a write
// would take it past this size, so at most two files are kept.
const logMaxBytes = 4 << 20

// rotatingFile is the -log-file writer. redirect, if not nil, is given each
// newly opened file so that the process's standard output and error follow
// it (ADR-0026).
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	max      int64
	redirect func(*os.File) error
	f        *os.File
	size     int64
}

func openLogFile(path string, max int64, redirect func(*os.File) error) (*rotatingFile, error) {
	r := &rotatingFile{path: path, max: max, redirect: redirect}
	if err := r.open(); err != nil {
		return nil, err
	}
	if r.size >= r.max {
		if err := r.rotate(); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("open log file: %w", err)
	}
	if r.redirect != nil {
		if err := r.redirect(f); err != nil {
			_ = f.Close()
			return fmt.Errorf("redirect output to the log file: %w", err)
		}
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *rotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return fmt.Errorf("rotate log file: %w", err)
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil {
		return fmt.Errorf("rotate log file: %w", err)
	}
	return r.open()
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}
