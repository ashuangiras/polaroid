package wire

import (
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// Statistics is the catalog statistics body (ADR-0036).
type Statistics struct {
	StatisticsVersion int                 `json:"statistics_version"`
	GeneratedAt       time.Time           `json:"generated_at"`
	Records           RecordCounts        `json:"records"`
	Procedures        ProcedureStatistics `json:"procedures"`
	Executions        ExecutionStatistics `json:"executions"`
	ToilItems         ToilStatistics      `json:"toil_items"`
	Payloads          PayloadStatistics   `json:"payloads"`
	Storage           StorageStatistics   `json:"storage"`
}

// RecordCounts counts every stored record kind.
type RecordCounts struct {
	Procedures               int64 `json:"procedures"`
	ProcedureVersions        int64 `json:"procedure_versions"`
	ProcedureReferences      int64 `json:"procedure_references"`
	ProcedureOrigins         int64 `json:"procedure_origins"`
	ProcedureLifecycleEvents int64 `json:"procedure_lifecycle_events"`
	Repositories             int64 `json:"repositories"`
	RepositoryAliases        int64 `json:"repository_aliases"`
	Bindings                 int64 `json:"bindings"`
	BindingRevisions         int64 `json:"binding_revisions"`
	Executions               int64 `json:"executions"`
	ExecutionChildren        int64 `json:"execution_children"`
	ExecutionDecisions       int64 `json:"execution_decisions"`
	ExecutionInvalidations   int64 `json:"execution_invalidations"`
	Feedback                 int64 `json:"feedback"`
	ToilItems                int64 `json:"toil_items"`
	ToilRevisions            int64 `json:"toil_revisions"`
	ToilRevisionSources      int64 `json:"toil_revision_sources"`
	ToilRevisionExecutions   int64 `json:"toil_revision_executions"`
}

// ProcedureStatistics counts procedures by current lifecycle and scope.
type ProcedureStatistics struct {
	ByLifecycle struct {
		Active  int64 `json:"active"`
		Retired int64 `json:"retired"`
	} `json:"by_lifecycle"`
	ByScope struct {
		Shared      int64 `json:"shared"`
		Local       int64 `json:"local"`
		Unspecified int64 `json:"unspecified"`
	} `json:"by_scope"`
}

// OutcomeCounts counts executions by recorded outcome.
type OutcomeCounts struct {
	Succeeded int64 `json:"succeeded"`
	Failed    int64 `json:"failed"`
}

// ExecutionStatistics counts executions by recorded outcome and evidence
// status.
type ExecutionStatistics struct {
	ByOutcome        OutcomeCounts `json:"by_outcome"`
	ByEvidenceStatus struct {
		Valid       int64 `json:"valid"`
		Invalidated int64 `json:"invalidated"`
	} `json:"by_evidence_status"`
	InvalidatedByOutcome      OutcomeCounts `json:"invalidated_by_outcome"`
	ValidWithInvalidatedChild int64         `json:"valid_with_invalidated_child"`
}

// ToilStatistics counts toil items by their latest revision's status.
type ToilStatistics struct {
	ByStatus struct {
		Observed    int64 `json:"observed"`
		Candidate   int64 `json:"candidate"`
		Implemented int64 `json:"implemented"`
		Validated   int64 `json:"validated"`
		Retired     int64 `json:"retired"`
	} `json:"by_status"`
}

// PayloadSize summarises one category of stored JSON values.
type PayloadSize struct {
	Count      int64 `json:"count"`
	TotalBytes int64 `json:"total_bytes"`
	MeanBytes  int64 `json:"mean_bytes"`
	MaxBytes   int64 `json:"max_bytes"`
}

func newPayloadSize(p memory.PayloadSize) PayloadSize {
	return PayloadSize{Count: p.Count, TotalBytes: p.TotalBytes, MeanBytes: p.MeanBytes(), MaxBytes: p.MaxBytes}
}

// PayloadStatistics holds a PayloadSize per stored JSON category.
type PayloadStatistics struct {
	ProcedureContracts             PayloadSize `json:"procedure_contracts"`
	ProcedureInstructions          PayloadSize `json:"procedure_instructions"`
	ReferenceInputs                PayloadSize `json:"reference_inputs"`
	BindingRevisionInputs          PayloadSize `json:"binding_revision_inputs"`
	ExecutionInputs                PayloadSize `json:"execution_inputs"`
	ExecutionEvidence              PayloadSize `json:"execution_evidence"`
	ExecutionEnvironmentAttributes PayloadSize `json:"execution_environment_attributes"`
	DecisionEvidence               PayloadSize `json:"decision_evidence"`
	FeedbackContexts               PayloadSize `json:"feedback_contexts"`
	ToilAssessments                PayloadSize `json:"toil_assessments"`
	ToilProposals                  PayloadSize `json:"toil_proposals"`
}

// StorageStatistics describes the database pages and files.
type StorageStatistics struct {
	SchemaVersion int64 `json:"schema_version"`
	PageSize      int64 `json:"page_size"`
	PageCount     int64 `json:"page_count"`
	FreelistCount int64 `json:"freelist_count"`
	DatabaseBytes int64 `json:"database_bytes"`
	Files         struct {
		Database FileSize `json:"database"`
		WAL      FileSize `json:"wal"`
		SHM      FileSize `json:"shm"`
	} `json:"files"`
}

// FileSize is a file's size; bytes is omitted when the file is absent.
type FileSize struct {
	Present bool   `json:"present"`
	Bytes   *int64 `json:"bytes,omitzero"`
}

func newFileSize(f memory.FileSize) FileSize {
	if !f.Present {
		return FileSize{}
	}
	return FileSize{Present: true, Bytes: &f.Bytes}
}

// NewStatistics returns the body for s.
func NewStatistics(s memory.Statistics) Statistics {
	r, e, p, st := s.Records, s.Executions, s.Payloads, s.Storage
	out := Statistics{
		StatisticsVersion: memory.StatisticsVersion,
		GeneratedAt:       s.GeneratedAt,
		Records: RecordCounts{
			Procedures: r.Procedures, ProcedureVersions: r.ProcedureVersions, ProcedureReferences: r.ProcedureReferences,
			ProcedureOrigins: r.ProcedureOrigins, ProcedureLifecycleEvents: r.ProcedureLifecycleEvents,
			Repositories: r.Repositories, RepositoryAliases: r.RepositoryAliases,
			Bindings: r.Bindings, BindingRevisions: r.BindingRevisions,
			Executions: r.Executions, ExecutionChildren: r.ExecutionChildren, ExecutionDecisions: r.ExecutionDecisions,
			ExecutionInvalidations: r.ExecutionInvalidations, Feedback: r.Feedback,
			ToilItems: r.ToilItems, ToilRevisions: r.ToilRevisions, ToilRevisionSources: r.ToilRevisionSources,
			ToilRevisionExecutions: r.ToilRevisionExecutions,
		},
		Executions: ExecutionStatistics{
			ByOutcome:                 OutcomeCounts{Succeeded: e.ValidSucceeded + e.InvalidatedSucceeded, Failed: e.ValidFailed + e.InvalidatedFailed},
			InvalidatedByOutcome:      OutcomeCounts{Succeeded: e.InvalidatedSucceeded, Failed: e.InvalidatedFailed},
			ValidWithInvalidatedChild: e.ValidWithInvalidatedChild,
		},
		Payloads: PayloadStatistics{
			ProcedureContracts:             newPayloadSize(p.ProcedureContracts),
			ProcedureInstructions:          newPayloadSize(p.ProcedureInstructions),
			ReferenceInputs:                newPayloadSize(p.ReferenceInputs),
			BindingRevisionInputs:          newPayloadSize(p.BindingRevisionInputs),
			ExecutionInputs:                newPayloadSize(p.ExecutionInputs),
			ExecutionEvidence:              newPayloadSize(p.ExecutionEvidence),
			ExecutionEnvironmentAttributes: newPayloadSize(p.ExecutionEnvironmentAttributes),
			DecisionEvidence:               newPayloadSize(p.DecisionEvidence),
			FeedbackContexts:               newPayloadSize(p.FeedbackContexts),
			ToilAssessments:                newPayloadSize(p.ToilAssessments),
			ToilProposals:                  newPayloadSize(p.ToilProposals),
		},
		Storage: StorageStatistics{
			SchemaVersion: st.SchemaVersion, PageSize: st.PageSize, PageCount: st.PageCount,
			FreelistCount: st.FreelistCount, DatabaseBytes: st.DatabaseBytes(),
		},
	}
	out.Procedures.ByLifecycle.Active, out.Procedures.ByLifecycle.Retired = s.Procedures.Active, s.Procedures.Retired
	out.Procedures.ByScope.Shared, out.Procedures.ByScope.Local, out.Procedures.ByScope.Unspecified =
		s.Procedures.Shared, s.Procedures.Local, s.Procedures.Unspecified
	out.Executions.ByEvidenceStatus.Valid = e.ValidSucceeded + e.ValidFailed
	out.Executions.ByEvidenceStatus.Invalidated = e.InvalidatedSucceeded + e.InvalidatedFailed
	t := s.ToilItems
	out.ToilItems.ByStatus.Observed, out.ToilItems.ByStatus.Candidate, out.ToilItems.ByStatus.Implemented =
		t.Observed, t.Candidate, t.Implemented
	out.ToilItems.ByStatus.Validated, out.ToilItems.ByStatus.Retired = t.Validated, t.Retired
	out.Storage.Files.Database = newFileSize(st.Files.Database)
	out.Storage.Files.WAL = newFileSize(st.Files.WAL)
	out.Storage.Files.SHM = newFileSize(st.Files.SharedMemory)
	return out
}
