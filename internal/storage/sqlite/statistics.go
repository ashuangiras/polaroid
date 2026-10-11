package sqlite

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// statisticsQuery is one compound statement, so every row comes from one
// snapshot (ADR-0036). A row is a name, a count and, for payloads, the
// total and maximum octet_length: the stored JSON text's UTF-8 bytes.
// COUNT(column) and the aggregates skip NULL, an absent optional value.
const statisticsQuery = `
SELECT 'procedures', COUNT(*), 0, 0 FROM procedures
UNION ALL SELECT 'procedure_versions', COUNT(*), 0, 0 FROM procedure_versions
UNION ALL SELECT 'procedure_references', COUNT(*), 0, 0 FROM procedure_version_references
UNION ALL SELECT 'procedure_origins', COUNT(*), 0, 0 FROM procedure_origins
UNION ALL SELECT 'procedure_lifecycle_events', COUNT(*), 0, 0 FROM procedure_lifecycle_events
UNION ALL SELECT 'repositories', COUNT(*), 0, 0 FROM repositories
UNION ALL SELECT 'repository_aliases', COUNT(*), 0, 0 FROM repository_identifiers WHERE canonical = 0
UNION ALL SELECT 'bindings', COUNT(*), 0, 0 FROM bindings
UNION ALL SELECT 'binding_revisions', COUNT(*), 0, 0 FROM binding_revisions
UNION ALL SELECT 'executions', COUNT(*), 0, 0 FROM executions
UNION ALL SELECT 'execution_children', COUNT(*), 0, 0 FROM execution_children
UNION ALL SELECT 'execution_decisions', COUNT(*), 0, 0 FROM execution_decisions
UNION ALL SELECT 'execution_invalidations', COUNT(*), 0, 0 FROM execution_invalidations
UNION ALL SELECT 'feedback', COUNT(*), 0, 0 FROM feedback
UNION ALL SELECT 'toil_items', COUNT(*), 0, 0 FROM toil_items
UNION ALL SELECT 'toil_revisions', COUNT(*), 0, 0 FROM toil_revisions
UNION ALL SELECT 'toil_revision_sources', COUNT(*), 0, 0 FROM toil_revision_sources
UNION ALL SELECT 'toil_revision_executions', COUNT(*), 0, 0 FROM toil_revision_executions
UNION ALL SELECT 'lifecycle.' || status, COUNT(*), 0, 0
	FROM (SELECT ` + lifecycleStatusOfP + ` AS status FROM procedures AS p) GROUP BY status
UNION ALL SELECT 'scope.' || scope, COUNT(*), 0, 0
	FROM (SELECT COALESCE(v.scope, 'unspecified') AS scope FROM procedures AS p
	      JOIN procedure_versions AS v ON v.procedure_id = p.id
	           AND v.version = (SELECT MAX(pv.version) FROM procedure_versions AS pv WHERE pv.procedure_id = p.id))
	GROUP BY scope
UNION ALL SELECT 'executions.' || evidence || '.' || outcome, COUNT(*), 0, 0
	FROM (SELECT e.outcome AS outcome, CASE WHEN i.execution_id IS NULL THEN 'valid' ELSE 'invalidated' END AS evidence
	      FROM executions AS e LEFT JOIN execution_invalidations AS i ON i.execution_id = e.id)
	GROUP BY evidence, outcome
UNION ALL SELECT 'executions.valid_with_invalidated_child', COUNT(DISTINCT c.parent_execution_id), 0, 0
	FROM execution_children AS c JOIN execution_invalidations AS ci ON ci.execution_id = c.child_execution_id
	WHERE c.parent_execution_id NOT IN (SELECT execution_id FROM execution_invalidations)
UNION ALL SELECT 'toil.' || r.status, COUNT(*), 0, 0 FROM toil_revisions AS r
	WHERE r.revision = (SELECT MAX(r2.revision) FROM toil_revisions AS r2 WHERE r2.toil_item_id = r.toil_item_id)
	GROUP BY r.status
UNION ALL SELECT 'payload.procedure_contracts', COUNT(contract), COALESCE(SUM(octet_length(contract)), 0),
	COALESCE(MAX(octet_length(contract)), 0) FROM procedure_versions
UNION ALL SELECT 'payload.procedure_instructions', COUNT(instructions), COALESCE(SUM(octet_length(instructions)), 0),
	COALESCE(MAX(octet_length(instructions)), 0) FROM procedure_versions
UNION ALL SELECT 'payload.reference_inputs', COUNT(inputs), COALESCE(SUM(octet_length(inputs)), 0),
	COALESCE(MAX(octet_length(inputs)), 0) FROM procedure_version_references
UNION ALL SELECT 'payload.binding_revision_inputs', COUNT(inputs), COALESCE(SUM(octet_length(inputs)), 0),
	COALESCE(MAX(octet_length(inputs)), 0) FROM binding_revisions
UNION ALL SELECT 'payload.execution_inputs', COUNT(inputs), COALESCE(SUM(octet_length(inputs)), 0),
	COALESCE(MAX(octet_length(inputs)), 0) FROM executions
UNION ALL SELECT 'payload.execution_evidence', COUNT(evidence), COALESCE(SUM(octet_length(evidence)), 0),
	COALESCE(MAX(octet_length(evidence)), 0) FROM executions
UNION ALL SELECT 'payload.execution_environment_attributes', COUNT(environment_attributes),
	COALESCE(SUM(octet_length(environment_attributes)), 0), COALESCE(MAX(octet_length(environment_attributes)), 0) FROM executions
UNION ALL SELECT 'payload.decision_evidence', COUNT(evidence), COALESCE(SUM(octet_length(evidence)), 0),
	COALESCE(MAX(octet_length(evidence)), 0) FROM execution_decisions
UNION ALL SELECT 'payload.feedback_contexts', COUNT(context), COALESCE(SUM(octet_length(context)), 0),
	COALESCE(MAX(octet_length(context)), 0) FROM feedback
UNION ALL SELECT 'payload.toil_assessments', COUNT(assessment), COALESCE(SUM(octet_length(assessment)), 0),
	COALESCE(MAX(octet_length(assessment)), 0) FROM toil_revisions
UNION ALL SELECT 'payload.toil_proposals', COUNT(proposal), COALESCE(SUM(octet_length(proposal)), 0),
	COALESCE(MAX(octet_length(proposal)), 0) FROM toil_revisions
UNION ALL SELECT 'storage.schema_version', user_version, 0, 0 FROM pragma_user_version
UNION ALL SELECT 'storage.page_size', page_size, 0, 0 FROM pragma_page_size
UNION ALL SELECT 'storage.page_count', page_count, 0, 0 FROM pragma_page_count
UNION ALL SELECT 'storage.freelist_count', freelist_count, 0, 0 FROM pragma_freelist_count`

// Statistics implements memory.Store. The file sizes are read after the
// statement has finished, so they are not part of its snapshot.
func (s *Store) Statistics(ctx context.Context) (memory.Statistics, error) {
	st, err := s.snapshotStatistics(ctx)
	if err != nil {
		return memory.Statistics{}, err
	}
	if st.Storage.Files, err = storageFiles(s.path); err != nil {
		return memory.Statistics{}, err
	}
	return st, nil
}

func (s *Store) snapshotStatistics(ctx context.Context) (memory.Statistics, error) {
	var st memory.Statistics
	r, e, p := &st.Records, &st.Executions, &st.Payloads
	counts := map[string]*int64{
		"procedures": &r.Procedures, "procedure_versions": &r.ProcedureVersions, "procedure_references": &r.ProcedureReferences,
		"procedure_origins": &r.ProcedureOrigins, "procedure_lifecycle_events": &r.ProcedureLifecycleEvents,
		"repositories": &r.Repositories, "repository_aliases": &r.RepositoryAliases,
		"bindings": &r.Bindings, "binding_revisions": &r.BindingRevisions,
		"executions": &r.Executions, "execution_children": &r.ExecutionChildren, "execution_decisions": &r.ExecutionDecisions,
		"execution_invalidations": &r.ExecutionInvalidations, "feedback": &r.Feedback,
		"toil_items": &r.ToilItems, "toil_revisions": &r.ToilRevisions, "toil_revision_sources": &r.ToilRevisionSources,
		"toil_revision_executions": &r.ToilRevisionExecutions,
		"lifecycle.active":         &st.Procedures.Active, "lifecycle.retired": &st.Procedures.Retired,
		"scope.shared": &st.Procedures.Shared, "scope.local": &st.Procedures.Local, "scope.unspecified": &st.Procedures.Unspecified,
		"executions.valid.succeeded": &e.ValidSucceeded, "executions.valid.failed": &e.ValidFailed,
		"executions.invalidated.succeeded": &e.InvalidatedSucceeded, "executions.invalidated.failed": &e.InvalidatedFailed,
		"executions.valid_with_invalidated_child": &e.ValidWithInvalidatedChild,
		"toil.observed": &st.ToilItems.Observed, "toil.candidate": &st.ToilItems.Candidate, "toil.implemented": &st.ToilItems.Implemented,
		"toil.validated": &st.ToilItems.Validated, "toil.retired": &st.ToilItems.Retired,
		"storage.schema_version": &st.Storage.SchemaVersion, "storage.page_size": &st.Storage.PageSize,
		"storage.page_count": &st.Storage.PageCount, "storage.freelist_count": &st.Storage.FreelistCount,
	}
	payloads := map[string]*memory.PayloadSize{
		"payload.procedure_contracts": &p.ProcedureContracts, "payload.procedure_instructions": &p.ProcedureInstructions,
		"payload.reference_inputs": &p.ReferenceInputs, "payload.binding_revision_inputs": &p.BindingRevisionInputs,
		"payload.execution_inputs": &p.ExecutionInputs, "payload.execution_evidence": &p.ExecutionEvidence,
		"payload.execution_environment_attributes": &p.ExecutionEnvironmentAttributes,
		"payload.decision_evidence":                &p.DecisionEvidence, "payload.feedback_contexts": &p.FeedbackContexts,
		"payload.toil_assessments": &p.ToilAssessments, "payload.toil_proposals": &p.ToilProposals,
	}
	rows, err := s.db.QueryContext(ctx, statisticsQuery)
	if err != nil {
		return memory.Statistics{}, fmt.Errorf("query statistics: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		var n, total, largest int64
		if err := rows.Scan(&name, &n, &total, &largest); err != nil {
			return memory.Statistics{}, fmt.Errorf("scan statistics: %w", err)
		}
		if size, ok := payloads[name]; ok {
			*size = memory.PayloadSize{Count: n, TotalBytes: total, MaxBytes: largest}
			continue
		}
		count, ok := counts[name]
		if !ok {
			return memory.Statistics{}, fmt.Errorf("unexpected statistics row %q", name)
		}
		*count = n
	}
	if err := rows.Err(); err != nil {
		return memory.Statistics{}, fmt.Errorf("read statistics: %w", err)
	}
	return st, nil
}

// storageFiles returns the sizes of the database file and its -wal and -shm
// files; a file that does not exist is not present.
func storageFiles(path string) (memory.StorageFiles, error) {
	var files memory.StorageFiles
	for _, f := range []struct {
		suffix string
		size   *memory.FileSize
	}{{"", &files.Database}, {"-wal", &files.WAL}, {"-shm", &files.SharedMemory}} {
		info, err := os.Stat(path + f.suffix)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return memory.StorageFiles{}, fmt.Errorf("read storage file size: %w", err)
		default:
			*f.size = memory.FileSize{Present: true, Bytes: info.Size()}
		}
	}
	return files, nil
}
