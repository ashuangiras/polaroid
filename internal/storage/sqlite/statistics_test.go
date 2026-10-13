package sqlite_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

func statistics(t *testing.T, s *sqlite.Store) memory.Statistics {
	t.Helper()
	st, err := s.Statistics(context.Background())
	if err != nil {
		t.Fatalf("Statistics: %v", err)
	}
	return st
}

// TestStatisticsOfAnEmptyCatalog: a new catalog counts nothing, and its
// storage is described (ADR-0036).
func TestStatisticsOfAnEmptyCatalog(t *testing.T) {
	st := statistics(t, openStore(t, dbPath(t)))
	if st.Records != (memory.RecordCounts{}) || st.Procedures != (memory.ProcedureCounts{}) || st.Executions != (memory.ExecutionCounts{}) ||
		st.Feedback != (memory.FeedbackCounts{}) || st.ToilItems != (memory.ToilStatusCounts{}) || st.Payloads != (memory.PayloadSizes{}) {
		t.Fatalf("empty catalog: %+v", st)
	}
	s := st.Storage
	if s.SchemaVersion != 11 || s.PageSize <= 0 || s.PageCount <= 0 || s.FreelistCount < 0 || s.DatabaseBytes() != s.PageSize*s.PageCount {
		t.Fatalf("storage = %+v", s)
	}
	if !s.Files.Database.Present || s.Files.Database.Bytes <= 0 || !s.Files.WAL.Present || !s.Files.SharedMemory.Present {
		t.Fatalf("files = %+v", s.Files)
	}
}

// TestStatisticsWriteNothing: reading statistics never writes, checkpoints
// or vacuums: the database and its WAL keep their exact bytes.
func TestStatisticsWriteNothing(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	seedExecutions(t, s)
	mustInvalidate(t, s, "e1")
	read := func() (db, wal []byte) {
		t.Helper()
		var err error
		if db, err = os.ReadFile(path); err != nil {
			t.Fatal(err)
		}
		if wal, err = os.ReadFile(path + "-wal"); err != nil {
			t.Fatal(err)
		}
		return db, wal
	}
	db, wal := read()
	if len(wal) == 0 {
		t.Fatal("the seeded writes should still be in the WAL")
	}
	first := statistics(t, s)
	for range 3 {
		if again := statistics(t, s); again.Records != first.Records || again.Payloads != first.Payloads ||
			again.Storage.PageCount != first.Storage.PageCount || again.Storage.FreelistCount != first.Storage.FreelistCount {
			t.Fatalf("statistics changed without a write:\n%+v\n%+v", again, first)
		}
	}
	if db2, wal2 := read(); !bytes.Equal(db, db2) || !bytes.Equal(wal, wal2) {
		t.Fatal("reading statistics changed the database or its WAL")
	}
	if first.Records.Procedures != 1 || first.Records.ProcedureVersions != 2 || first.Records.Executions != 3 ||
		first.Records.ExecutionInvalidations != 1 || first.Executions.InvalidatedSucceeded+first.Executions.InvalidatedFailed != 1 {
		t.Fatalf("seeded statistics = %+v", first)
	}
}

// TestStorageFilesReportAbsentFiles: a missing file is absent, not an
// error; any other failure to read a file's size is an error.
func TestStorageFilesReportAbsentFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "polaroid.db")
	write := func(name string, n int) {
		t.Helper()
		if err := os.WriteFile(name, bytes.Repeat([]byte{'x'}, n), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	files, err := sqlite.StorageFiles(path)
	if err != nil || files != (memory.StorageFiles{}) {
		t.Fatalf("no files: %+v, %v", files, err)
	}
	write(path, 5)
	files, err = sqlite.StorageFiles(path)
	if err != nil || files != (memory.StorageFiles{Database: memory.FileSize{Present: true, Bytes: 5}}) {
		t.Fatalf("database only: %+v, %v", files, err)
	}
	write(path+"-wal", 0)
	write(path+"-shm", 3)
	files, err = sqlite.StorageFiles(path)
	want := memory.StorageFiles{Database: memory.FileSize{Present: true, Bytes: 5}, WAL: memory.FileSize{Present: true},
		SharedMemory: memory.FileSize{Present: true, Bytes: 3}}
	if err != nil || files != want {
		t.Fatalf("all files: %+v, %v", files, err)
	}

	// A path below a regular file cannot be examined: not "absent".
	if files, err := sqlite.StorageFiles(filepath.Join(path, "polaroid.db")); err == nil {
		t.Fatalf("path below a file: %+v, no error", files)
	}
}
