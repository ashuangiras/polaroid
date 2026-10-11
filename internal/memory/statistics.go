package memory

import (
	"context"
	"fmt"
	"time"
)

// StatisticsVersion is the version of the statistics shape (ADR-0036).
const StatisticsVersion = 1

// Statistics is a read-only summary of the catalog (ADR-0036). Everything
// except Storage.Files describes one committed state of the catalog.
type Statistics struct {
	GeneratedAt time.Time
	Records     RecordCounts
	Procedures  ProcedureCounts
	Executions  ExecutionCounts
	ToilItems   ToilStatusCounts
	Payloads    PayloadSizes
	Storage     StorageStatistics
}

// RecordCounts counts every stored record kind: identities, their history
// and relationship records.
type RecordCounts struct {
	Procedures               int64
	ProcedureVersions        int64
	ProcedureReferences      int64
	ProcedureOrigins         int64
	ProcedureLifecycleEvents int64
	Repositories             int64
	RepositoryAliases        int64
	Bindings                 int64
	BindingRevisions         int64
	Executions               int64
	ExecutionChildren        int64
	ExecutionDecisions       int64
	ExecutionInvalidations   int64
	Feedback                 int64
	ToilItems                int64
	ToilRevisions            int64
	ToilRevisionSources      int64
	ToilRevisionExecutions   int64
}

// ProcedureCounts counts procedures by their current lifecycle status and by
// the scope of their latest version.
type ProcedureCounts struct {
	Active, Retired            int64
	Shared, Local, Unspecified int64
}

// ExecutionCounts counts executions by recorded outcome and evidence status.
// The outcome is the one recorded, whether or not the execution was later
// invalidated.
type ExecutionCounts struct {
	ValidSucceeded, ValidFailed             int64
	InvalidatedSucceeded, InvalidatedFailed int64
	// ValidWithInvalidatedChild counts valid executions that directly link
	// at least one invalidated child; ancestors above them are not counted.
	ValidWithInvalidatedChild int64
}

// ToilStatusCounts counts toil items by the status of their latest revision.
type ToilStatusCounts struct {
	Observed, Candidate, Implemented, Validated, Retired int64
}

// PayloadSize summarises the logical sizes of one category of stored JSON
// values: absent values are not counted.
type PayloadSize struct {
	Count, TotalBytes, MaxBytes int64
}

// MeanBytes is TotalBytes divided by Count, rounded down, or 0 for none.
func (p PayloadSize) MeanBytes() int64 {
	if p.Count == 0 {
		return 0
	}
	return p.TotalBytes / p.Count
}

// PayloadSizes holds a PayloadSize per stored JSON category, over every
// version and revision.
type PayloadSizes struct {
	ProcedureContracts             PayloadSize
	ProcedureInstructions          PayloadSize
	ReferenceInputs                PayloadSize
	BindingRevisionInputs          PayloadSize
	ExecutionInputs                PayloadSize
	ExecutionEvidence              PayloadSize
	ExecutionEnvironmentAttributes PayloadSize
	DecisionEvidence               PayloadSize
	FeedbackContexts               PayloadSize
	ToilAssessments                PayloadSize
	ToilProposals                  PayloadSize
}

// StorageStatistics describes the database: pages as of the snapshot, and
// file sizes read separately afterwards.
type StorageStatistics struct {
	SchemaVersion int64
	PageSize      int64
	PageCount     int64
	FreelistCount int64
	Files         StorageFiles
}

// DatabaseBytes is the size of the database image in the snapshot.
func (s StorageStatistics) DatabaseBytes() int64 {
	return s.PageSize * s.PageCount
}

// StorageFiles holds the sizes of the database file and of SQLite's
// write-ahead log and shared-memory files.
type StorageFiles struct {
	Database, WAL, SharedMemory FileSize
}

// FileSize is a file's size, when it exists.
type FileSize struct {
	Present bool
	Bytes   int64
}

// Statistics returns the catalog's statistics. It writes nothing.
func (s *Service) Statistics(ctx context.Context) (Statistics, error) {
	at := timestamp()
	st, err := s.store.Statistics(ctx)
	if err != nil {
		return Statistics{}, fmt.Errorf("read statistics: %w", err)
	}
	st.GeneratedAt = at
	return st, nil
}
