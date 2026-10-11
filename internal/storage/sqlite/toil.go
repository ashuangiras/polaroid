package sqlite

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"

	sqlitelib "modernc.org/sqlite/lib"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// CreateToilItem implements memory.Store.
func (s *Store) CreateToilItem(ctx context.Context, t memory.ToilItem, first memory.ToilRevision) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO toil_items (id, canonical_key, created_at) VALUES (?, ?, ?)`,
			t.ID, t.CanonicalKey, formatTime(t.CreatedAt))
		// canonical_key is the only UNIQUE constraint on toil_items.
		if resultCode(err) == sqlitelib.SQLITE_CONSTRAINT_UNIQUE {
			return memory.ErrCanonicalKeyExists
		}
		if err != nil {
			return fmt.Errorf("insert toil item: %w", err)
		}
		return insertToilRevision(ctx, tx, first)
	})
}

// AppendToilRevision implements memory.Store.
func (s *Store) AppendToilRevision(ctx context.Context, baseRevision int, next memory.ToilRevision) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var latest sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(revision) FROM toil_revisions WHERE toil_item_id = ?`,
			next.ToilItemID).Scan(&latest); err != nil {
			return fmt.Errorf("read latest toil revision: %w", err)
		}
		if !latest.Valid {
			return memory.ErrNotFound
		}
		if int(latest.Int64) != baseRevision {
			return &memory.RevisionConflictError{BaseRevision: baseRevision, LatestRevision: int(latest.Int64)}
		}
		return insertToilRevision(ctx, tx, next)
	})
}

// insertToilRevision stores r with its links. Links go first: the schema
// accepts them only for a revision that does not exist yet, and checks the
// revision against them when it is inserted.
func insertToilRevision(ctx context.Context, tx *sql.Tx, r memory.ToilRevision) error {
	for i, src := range r.Sources {
		var steps any
		if len(src.Steps) > 0 {
			b, err := json.Marshal(src.Steps)
			if err != nil {
				return fmt.Errorf("encode steps: %w", err)
			}
			steps = string(b)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO toil_revision_sources (toil_item_id, revision, position, procedure_id, version, steps)
			VALUES (?, ?, ?, ?, ?, ?)`, r.ToilItemID, r.Number, i, src.ProcedureID, src.Version, steps); err != nil {
			return fmt.Errorf("insert toil source %d: %w", i, err)
		}
	}
	cites := []struct {
		role  string
		cites []memory.ToilEvidence
	}{{"observation", r.Observations}}
	if r.Validation != nil {
		cites = append(cites, struct {
			role  string
			cites []memory.ToilEvidence
		}{"validation", r.Validation.Executions})
	}
	for _, c := range cites {
		for i, e := range c.cites {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO toil_revision_executions (toil_item_id, revision, role, position, execution_id, note)
				VALUES (?, ?, ?, ?, ?, ?)`, r.ToilItemID, r.Number, c.role, i, e.ExecutionID, e.Note); err != nil {
				return fmt.Errorf("insert toil %s %d: %w", c.role, i, err)
			}
		}
	}
	var scopeRepository, proposal, replacementSummary, replacementRefs, validationRationale, validationRefs any
	if r.Applicability.Kind == memory.ScopeLocal {
		scopeRepository = r.Applicability.RepositoryID
	}
	if r.Proposal != nil {
		proposal = string(r.Proposal)
	}
	var err error
	if r.Replacement != nil {
		replacementSummary = r.Replacement.Summary
		if replacementRefs, err = encodeReferences(r.Replacement.References); err != nil {
			return err
		}
	}
	if r.Validation != nil {
		validationRationale = r.Validation.Rationale
		if validationRefs, err = encodeReferences(r.Validation.References); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO toil_revisions
			(toil_item_id, revision, activity, scope, scope_repository_id, assessment, proposal, status, status_rationale,
			 replacement_summary, replacement_references, validation_rationale, validation_references,
			 revision_reason, author, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ToilItemID, r.Number, r.Activity, string(r.Applicability.Kind), scopeRepository, string(r.Assessment), proposal,
		string(r.Status), r.StatusRationale, replacementSummary, replacementRefs, validationRationale, validationRefs,
		r.RevisionReason, r.Author, formatTime(r.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert toil revision %d: %w", r.Number, err)
	}
	return nil
}

// wireReference is the stored form of a declared reference.
type wireReference struct {
	URI  string `json:"uri"`
	Note string `json:"note,omitzero"`
}

func encodeReferences(refs []memory.ExternalReference) (any, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]wireReference, len(refs))
	for i, r := range refs {
		out[i] = wireReference(r)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode references: %w", err)
	}
	return string(b), nil
}

func decodeReferences(raw sql.NullString) ([]memory.ExternalReference, error) {
	if !raw.Valid {
		return nil, nil
	}
	var in []wireReference
	if err := json.Unmarshal([]byte(raw.String), &in); err != nil {
		return nil, fmt.Errorf("stored references: %w", err)
	}
	out := make([]memory.ExternalReference, len(in))
	for i, r := range in {
		out[i] = memory.ExternalReference(r)
	}
	return out, nil
}

// toilSelect reads revisions with their item and links in one statement:
// each revision's sources and executions are aggregated in link order.
const toilSelect = `
	SELECT t.id, t.canonical_key, t.created_at,
	       r.revision, r.activity, r.scope, r.scope_repository_id, r.assessment, r.proposal, r.status, r.status_rationale,
	       r.replacement_summary, r.replacement_references, r.validation_rationale, r.validation_references,
	       r.revision_reason, r.author, r.created_at,
	       (SELECT json_group_array(json_object('procedure_id', s.procedure_id, 'version', s.version, 'steps', json(s.steps))
	                                ORDER BY s.position)
	        FROM toil_revision_sources AS s WHERE s.toil_item_id = r.toil_item_id AND s.revision = r.revision),
	       (SELECT json_group_array(json_object('role', e.role, 'execution_id', e.execution_id, 'note', e.note)
	                                ORDER BY e.role, e.position)
	        FROM toil_revision_executions AS e WHERE e.toil_item_id = r.toil_item_id AND e.revision = r.revision)
	FROM toil_items AS t
	JOIN toil_revisions AS r ON r.toil_item_id = t.id
`

type storedSource struct {
	ProcedureID string   `json:"procedure_id"`
	Version     int      `json:"version"`
	Steps       []string `json:"steps"`
}

type storedCite struct {
	Role        string `json:"role"`
	ExecutionID string `json:"execution_id"`
	Note        string `json:"note"`
}

// ToilHistory implements memory.Store.
func (s *Store) ToilHistory(ctx context.Context, id string) (memory.ToilHistory, error) {
	return s.toilHistory(ctx, toilSelect+`WHERE t.id = ? ORDER BY r.revision`, id)
}

// ToilHistoryByKey implements memory.Store.
func (s *Store) ToilHistoryByKey(ctx context.Context, canonicalKey string) (memory.ToilHistory, error) {
	return s.toilHistory(ctx, toilSelect+`WHERE t.canonical_key = ? ORDER BY r.revision`, canonicalKey)
}

// ToilRevision implements memory.Store.
func (s *Store) ToilRevision(ctx context.Context, id string, number int) (memory.ToilRevision, error) {
	h, err := s.toilHistory(ctx, toilSelect+`WHERE t.id = ? AND r.revision = ?`, id, number)
	if err != nil {
		return memory.ToilRevision{}, err
	}
	return h.Revisions[0], nil
}

func (s *Store) toilHistory(ctx context.Context, query string, args ...any) (memory.ToilHistory, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return memory.ToilHistory{}, fmt.Errorf("query toil history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var h memory.ToilHistory
	var itemCreated string
	for rows.Next() {
		var r memory.ToilRevision
		var scope, created, sources, cites string
		var scopeRepository, proposal, replacementSummary, replacementRefs, validationRationale, validationRefs sql.NullString
		var assessment []byte
		if err := rows.Scan(&h.Item.ID, &h.Item.CanonicalKey, &itemCreated,
			&r.Number, &r.Activity, &scope, &scopeRepository, &assessment, &proposal, &r.Status, &r.StatusRationale,
			&replacementSummary, &replacementRefs, &validationRationale, &validationRefs,
			&r.RevisionReason, &r.Author, &created, &sources, &cites); err != nil {
			return memory.ToilHistory{}, fmt.Errorf("scan toil revision: %w", err)
		}
		r.ToilItemID = h.Item.ID
		r.Applicability = memory.Applicability{Kind: memory.ScopeKind(scope), RepositoryID: scopeRepository.String}
		r.Assessment = jsontext.Value(assessment)
		if proposal.Valid {
			r.Proposal = jsontext.Value(proposal.String)
		}
		if r.CreatedAt, err = parseTime(created); err != nil {
			return memory.ToilHistory{}, err
		}
		if err := decodeLinks(&r, sources, cites); err != nil {
			return memory.ToilHistory{}, err
		}
		if replacementSummary.Valid {
			refs, err := decodeReferences(replacementRefs)
			if err != nil {
				return memory.ToilHistory{}, err
			}
			r.Replacement = &memory.ToilReplacement{Summary: replacementSummary.String, References: refs}
		}
		if validationRationale.Valid {
			refs, err := decodeReferences(validationRefs)
			if err != nil {
				return memory.ToilHistory{}, err
			}
			if r.Validation == nil {
				r.Validation = &memory.ToilValidation{}
			}
			r.Validation.Rationale, r.Validation.References = validationRationale.String, refs
		}
		h.Revisions = append(h.Revisions, r)
	}
	if err := rows.Err(); err != nil {
		return memory.ToilHistory{}, fmt.Errorf("read toil history: %w", err)
	}
	// Every item is created together with revision 1, so no rows means no
	// item (or, for one revision, no such revision).
	if len(h.Revisions) == 0 {
		return memory.ToilHistory{}, memory.ErrNotFound
	}
	if h.Item.CreatedAt, err = parseTime(itemCreated); err != nil {
		return memory.ToilHistory{}, err
	}
	latest := h.Revisions[len(h.Revisions)-1]
	h.Item.LatestRevision, h.Item.Activity, h.Item.Status = latest.Number, latest.Activity, latest.Status
	h.Item.Applicability, h.Item.UpdatedAt = latest.Applicability, latest.CreatedAt
	return h, nil
}

// decodeLinks sets r's sources, observations and validation executions from
// the aggregated link arrays of toilSelect.
func decodeLinks(r *memory.ToilRevision, sources, cites string) error {
	var stored []storedSource
	if err := json.Unmarshal([]byte(sources), &stored); err != nil {
		return fmt.Errorf("stored toil sources: %w", err)
	}
	for _, s := range stored {
		r.Sources = append(r.Sources, memory.ToilSource{ProcedureID: s.ProcedureID, Version: s.Version, Steps: s.Steps})
	}
	var links []storedCite
	if err := json.Unmarshal([]byte(cites), &links); err != nil {
		return fmt.Errorf("stored toil executions: %w", err)
	}
	for _, l := range links {
		e := memory.ToilEvidence{ExecutionID: l.ExecutionID, Note: l.Note}
		switch l.Role {
		case "observation":
			r.Observations = append(r.Observations, e)
		case "validation":
			if r.Validation == nil {
				r.Validation = &memory.ToilValidation{}
			}
			r.Validation.Executions = append(r.Validation.Executions, e)
		default:
			return fmt.Errorf("stored toil execution role %q", l.Role)
		}
	}
	return nil
}

// toilSnapshot covers the item list: items, revisions and repository
// identifiers. Links are written with their revision, so the revision mark
// bounds them too.
const toilSnapshot = `SELECT
	(SELECT COALESCE(MAX(rowid), 0) FROM toil_items),
	(SELECT COALESCE(MAX(rowid), 0) FROM toil_revisions),
	(SELECT COALESCE(MAX(rowid), 0) FROM repository_identifiers)`

// ToilSnapshot implements memory.Store.
func (s *Store) ToilSnapshot(ctx context.Context) (memory.Snapshot, error) {
	marks := make(memory.Snapshot, 3)
	if err := s.db.QueryRowContext(ctx, toilSnapshot).Scan(&marks[0], &marks[1], &marks[2]); err != nil {
		return nil, fmt.Errorf("read toil snapshot: %w", err)
	}
	return marks, nil
}

// ListToilItems implements memory.Store: the latest revision at the
// boundary decides the summary and the filters.
func (s *Store) ListToilItems(ctx context.Context, f memory.ToilFilter) ([]memory.ToilItem, error) {
	marks, err := boundary(f.Snapshot, 3)
	if err != nil {
		return nil, err
	}
	afterKey := ""
	if f.After != nil {
		afterKey = f.After.Key
	}
	status := string(f.Status)
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.canonical_key, t.created_at, r.revision, r.activity, r.status, r.scope, r.scope_repository_id, r.created_at
		FROM toil_items AS t
		JOIN toil_revisions AS r ON r.toil_item_id = t.id
		     AND r.revision = (SELECT MAX(x.revision) FROM toil_revisions AS x WHERE x.toil_item_id = t.id AND x.rowid <= ?)
		WHERE t.rowid <= ?
		  AND (? = '' OR t.canonical_key > ?)
		  AND (? = '' OR r.status = ?)
		  AND (? = '' OR r.scope = 'shared' OR r.scope_repository_id = (
		       SELECT ri.repository_id FROM repository_identifiers AS ri WHERE ri.identifier = ? AND ri.rowid <= ?))
		  AND (? = '' OR EXISTS (SELECT 1 FROM toil_revision_sources AS s
		                         WHERE s.toil_item_id = t.id AND s.revision = r.revision AND s.procedure_id = ?))
		ORDER BY t.canonical_key
		LIMIT ?`,
		marks[1], marks[0], afterKey, afterKey, status, status, f.Repository, f.Repository, marks[2],
		f.ProcedureID, f.ProcedureID, fetchLimit(f.Page.Limit))
	if err != nil {
		return nil, fmt.Errorf("query toil items: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := []memory.ToilItem{}
	for rows.Next() {
		var t memory.ToilItem
		var created, updated, scope string
		var scopeRepository sql.NullString
		if err := rows.Scan(&t.ID, &t.CanonicalKey, &created, &t.LatestRevision, &t.Activity, &t.Status,
			&scope, &scopeRepository, &updated); err != nil {
			return nil, fmt.Errorf("scan toil item: %w", err)
		}
		if t.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if t.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		t.Applicability = memory.Applicability{Kind: memory.ScopeKind(scope), RepositoryID: scopeRepository.String}
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read toil items: %w", err)
	}
	return items, nil
}
