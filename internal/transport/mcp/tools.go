package mcp

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// Tool arguments. Optional members are tagged omitzero so that the
// generated schemas do not require them.

type (
	byID struct {
		ID string `json:"id"`
	}
	// pageArgs ask for one page of a list (ADR-0021, ADR-0023).
	pageArgs struct {
		Limit int    `json:"limit,omitzero" jsonschema:"return at most this many items (1 to 500) and a next cursor if there are more; omit for the complete list"`
		After string `json:"after,omitzero" jsonschema:"the next cursor of the previous page; requires limit and the same other arguments as the first page"`
	}
	// snapshotArgs ask for a snapshot traversal (ADR-0023).
	snapshotArgs struct {
		Snapshot bool `json:"snapshot,omitzero" jsonschema:"page a snapshot fixed at the first page: every item that existed then, exactly once, as it was then, and nothing created later; requires limit; pass it on every page"`
	}
	listProceduresArgs struct {
		Repository string `json:"repository,omitzero" jsonschema:"only procedures whose latest version applies in this repository identifier: shared, unspecified, or local to it"`
		Scope      string `json:"scope,omitzero" jsonschema:"only procedures whose latest version is shared, local or unspecified"`
		Q          string `json:"q,omitzero" jsonschema:"only procedures whose canonical key or goal contains this text, ignoring ASCII case"`
		pageArgs
		snapshotArgs
	}
	procedureRef struct {
		ID           string `json:"id,omitzero" jsonschema:"the procedure ID; give this or canonical_key"`
		CanonicalKey string `json:"canonical_key,omitzero" jsonschema:"the procedure's canonical key; give this or id"`
	}
	versionRef struct {
		ProcedureID string `json:"procedure_id"`
		Version     int    `json:"version"`
	}
	graphArgs struct {
		ProcedureID string `json:"procedure_id"`
		Version     int    `json:"version"`
		Repository  string `json:"repository,omitzero" jsonschema:"resolve contextual references from evidence in this repository; requires environment"`
		Environment string `json:"environment,omitzero" jsonschema:"resolve contextual references from evidence in this environment; requires repository"`
		targetArgs
	}
	// targetArgs ask for each selected combination's status at an exact
	// commit and inputs (ADR-0018).
	targetArgs struct {
		Commit string         `json:"commit,omitzero" jsonschema:"the full commit you will run at; requires inputs. Each node then reports target_verification"`
		Inputs jsontext.Value `json:"inputs,omitzero" jsonschema:"the root's effective inputs at that commit; requires commit"`
	}
	createProcedureArgs struct {
		CanonicalKey string          `json:"canonical_key"`
		Origin       *wire.NewOrigin `json:"origin,omitzero" jsonschema:"where and why the procedure is created: repository_id and reason"`
		wire.Definition
	}
	originArgs struct {
		ProcedureID string `json:"procedure_id"`
		wire.NewOrigin
	}
	reviseProcedureArgs struct {
		ProcedureID string `json:"procedure_id"`
		BaseVersion int    `json:"base_version" jsonschema:"the latest version you read; a stale base is a version_conflict"`
		wire.Definition
	}
	repositoryArgs struct {
		Repository string `json:"repository"`
		pageArgs
		snapshotArgs
	}
	repositoryRef struct {
		ID         string `json:"id,omitzero" jsonschema:"the repository ID; give this or identifier"`
		Identifier string `json:"identifier,omitzero" jsonschema:"a registered identifier, canonical or alias; give this or id"`
	}
	aliasArgs struct {
		RepositoryID string `json:"repository_id"`
		wire.NewAlias
	}
	createBindingArgs struct {
		Repository  string `json:"repository"`
		Name        string `json:"name"`
		ProcedureID string `json:"procedure_id"`
		wire.BindingConfig
	}
	reviseBindingArgs struct {
		BindingID    string `json:"binding_id"`
		BaseRevision int    `json:"base_revision" jsonschema:"the latest revision you read; a stale base is a revision_conflict"`
		wire.BindingConfig
	}
	bindingRevisionRef struct {
		BindingID string `json:"binding_id"`
		Revision  int    `json:"revision"`
	}
	resolveBindingArgs struct {
		BindingID   string `json:"binding_id"`
		Environment string `json:"environment"`
		targetArgs
	}
	listExecutionsArgs struct {
		ProcedureID string `json:"procedure_id,omitzero"`
		Version     int    `json:"version,omitzero" jsonschema:"requires procedure_id"`
		Repository  string `json:"repository,omitzero" jsonschema:"a repository identifier; a registered one matches every identifier of its repository"`
		Commit      string `json:"commit,omitzero"`
		pageArgs
	}
	executionRef struct {
		ExecutionID string `json:"execution_id"`
	}
	listVerificationsArgs struct {
		ProcedureID string `json:"procedure_id"`
		Version     int    `json:"version"`
		Repository  string `json:"repository,omitzero"`
		Commit      string `json:"commit,omitzero"`
		Environment string `json:"environment,omitzero"`
	}
	listFeedbackArgs struct {
		Kind           string `json:"kind,omitzero" jsonschema:"only reports of this kind: problem or suggestion"`
		SubjectType    string `json:"subject_type,omitzero" jsonschema:"only reports about a service, repository, procedure, binding or execution; service includes reports without a subject"`
		SubjectID      string `json:"subject_id,omitzero" jsonschema:"only reports about this record; requires subject_type"`
		SubjectVersion int    `json:"subject_version,omitzero" jsonschema:"only reports about this procedure version or binding revision; requires subject_id"`
		Repository     string `json:"repository,omitzero" jsonschema:"only reports made in, or about, this repository (by identity)"`
		pageArgs
	}
)

func (a pageArgs) page() memory.Page {
	return memory.Page{Limit: a.Limit, After: a.After}
}

func (a listProceduresArgs) page() memory.Page {
	return memory.Page{Limit: a.Limit, After: a.After, Snapshot: a.Snapshot}
}

func (a repositoryArgs) page() memory.Page {
	return memory.Page{Limit: a.Limit, After: a.After, Snapshot: a.Snapshot}
}

func (a targetArgs) target() *memory.Target {
	if a.Commit == "" && a.Inputs == nil {
		return nil
	}
	return &memory.Target{Commit: a.Commit, Inputs: a.Inputs}
}

func (t *tools) register(s *sdk.Server) {
	const read, write = true, false

	add(s, t, "list_procedures", "List procedures, ordered by canonical key, with origin and the scope (shared, local or unspecified), goal and applicability of version latest_version only. "+
		"Unspecified declares nothing: admissible everywhere, not a claim of reuse. Before reusing one, resolve it and read the selected version's scope and contract. "+
		"Pass repository to see only the procedures that apply in it, scope or q to narrow further, limit to page, and snapshot to page a fixed snapshot.", read,
		func(ctx context.Context, in listProceduresArgs) (any, error) {
			ps, next, err := t.svc.ListProcedures(ctx, memory.ProcedureFilter{Repository: in.Repository, Scope: in.Scope, Query: in.Q, Page: in.page()})
			return wire.NewProcedureList(ps, next), err
		})
	add(s, t, "get_procedure", "Get a procedure and all of its versions, by id or by canonical_key.", read,
		func(ctx context.Context, in procedureRef) (any, error) {
			var h memory.History
			var err error
			switch {
			case in.ID != "" && in.CanonicalKey != "":
				return nil, &memory.ValidationError{Problems: []memory.FieldProblem{{Field: "canonical_key", Message: "must not be given with id"}}}
			case in.ID != "":
				h, err = t.svc.Procedure(ctx, in.ID)
			case in.CanonicalKey != "":
				h, err = t.svc.ProcedureByKey(ctx, in.CanonicalKey)
			default:
				return nil, required("id")
			}
			return wire.NewHistory(h), err
		})
	add(s, t, "get_version", "Get one version of a procedure.", read,
		func(ctx context.Context, in versionRef) (any, error) {
			v, err := t.svc.Version(ctx, in.ProcedureID, in.Version)
			return wire.NewVersion(v), err
		})
	add(s, t, "get_graph", "Get a version's composition graph with the exact version each reference selects. "+
		"With repository and environment, contextual references resolve from verification evidence, reported per node as selection_evidence (any commit). "+
		"Add commit and inputs to get target_verification: whether each selected combination is verified at that exact commit and inputs.", read,
		func(ctx context.Context, in graphArgs) (any, error) {
			g, err := t.svc.CompositionGraph(ctx, in.ProcedureID, in.Version,
				memory.ResolutionContext{Repository: in.Repository, Environment: in.Environment, Target: in.target()})
			return wire.NewGraphNode(g), err
		})
	add(s, t, "create_procedure", "Create a procedure and its version 1. contract and instructions are free-form JSON objects. "+
		`Declare applicability as {"shared": {}} or {"repository": "<repository id>"}, and origin if you know where the procedure comes from.`, write,
		func(ctx context.Context, in createProcedureArgs) (any, error) {
			var origin *memory.Origin
			if in.Origin != nil {
				o := in.Origin.Domain()
				origin = &o
			}
			h, err := t.svc.CreateProcedure(ctx, memory.NewProcedure{CanonicalKey: in.CanonicalKey, Origin: origin, Definition: in.Domain()})
			return wire.NewHistory(h), flatten(err, "version.")
		})
	add(s, t, "revise_procedure", "Append a version derived from base_version, which must be the latest version. "+
		"Changing applicability, for example promoting a local procedure to shared, is a new version.", write,
		func(ctx context.Context, in reviseProcedureArgs) (any, error) {
			v, err := t.svc.ReviseProcedure(ctx, in.ProcedureID, memory.Revision{BaseVersion: in.BaseVersion, Definition: in.Domain()})
			return wire.NewVersion(v), flatten(err, "version.")
		})
	add(s, t, "record_procedure_origin", "Record where (repository_id) and why (reason) a procedure was first created. Allowed once per procedure.", write,
		func(ctx context.Context, in originArgs) (any, error) {
			h, err := t.svc.RecordOrigin(ctx, in.ProcedureID, in.Domain())
			return wire.NewHistory(h), flatten(err, "origin.")
		})
	add(s, t, "register_repository", "Register a repository under its canonical identifier (host and path, lowercase, no scheme or .git) with a display name. "+
		"Register a repository once; add other identifiers of the same repository as aliases.", write,
		func(ctx context.Context, in wire.NewRepository) (any, error) {
			r, err := t.svc.RegisterRepository(ctx, in.Domain())
			return wire.NewRepositoryBody(r), err
		})
	add(s, t, "add_repository_alias", "Register another identifier of the same repository, with the reason it is the same repository.", write,
		func(ctx context.Context, in aliasArgs) (any, error) {
			r, err := t.svc.AddRepositoryAlias(ctx, in.RepositoryID, in.Domain())
			return wire.NewRepositoryBody(r), err
		})
	add(s, t, "get_repository", "Get a registered repository, by id or by any of its identifiers.", read,
		func(ctx context.Context, in repositoryRef) (any, error) {
			var r memory.Repository
			var err error
			switch {
			case in.ID != "" && in.Identifier != "":
				return nil, &memory.ValidationError{Problems: []memory.FieldProblem{{Field: "identifier", Message: "must not be given with id"}}}
			case in.ID != "":
				r, err = t.svc.Repository(ctx, in.ID)
			case in.Identifier != "":
				r, err = t.svc.RepositoryByIdentifier(ctx, in.Identifier)
			default:
				return nil, required("id")
			}
			return wire.NewRepositoryBody(r), err
		})
	add(s, t, "list_repositories", "List registered repositories, oldest first.", read,
		func(ctx context.Context, in pageArgs) (any, error) {
			rs, next, err := t.svc.ListRepositories(ctx, in.page())
			return wire.NewRepositoryList(rs, next), err
		})
	add(s, t, "list_bindings", "List a repository's bindings, ordered by name. A registered identifier lists the bindings of every identifier of its repository. Pass limit to page, and snapshot to page a fixed snapshot.", read,
		func(ctx context.Context, in repositoryArgs) (any, error) {
			bs, next, err := t.svc.ListBindings(ctx, in.Repository, in.page())
			return wire.NewBindingList(bs, next), err
		})
	add(s, t, "get_binding", "Get a binding and all of its revisions.", read,
		func(ctx context.Context, in byID) (any, error) {
			h, err := t.svc.Binding(ctx, in.ID)
			return wire.NewBindingHistory(h), err
		})
	add(s, t, "get_binding_revision", "Get one revision of a binding.", read,
		func(ctx context.Context, in bindingRevisionRef) (any, error) {
			r, err := t.svc.BindingRevision(ctx, in.BindingID, in.Revision)
			return wire.NewBindingRevision(r), err
		})
	add(s, t, "resolve_binding", "Resolve a binding's latest revision in its repository and an environment. "+
		"Versions are selected from earlier evidence, reported per node as selection_evidence with the commit it ran at; that is not verification of your checkout. "+
		"Add commit and inputs to get target_verification for each selected combination at exactly that commit and inputs.", read,
		func(ctx context.Context, in resolveBindingArgs) (any, error) {
			r, err := t.svc.ResolveBinding(ctx, in.BindingID, in.Environment, in.target())
			return wire.NewBindingResolution(r), err
		})
	add(s, t, "create_binding", "Bind a procedure in a repository under a local name, with inputs and a version policy.", write,
		func(ctx context.Context, in createBindingArgs) (any, error) {
			h, err := t.svc.CreateBinding(ctx, memory.NewBinding{Repository: in.Repository, Name: in.Name, ProcedureID: in.ProcedureID, Config: in.Domain()})
			return wire.NewBindingHistory(h), flatten(err, "revision.")
		})
	add(s, t, "revise_binding", "Append a binding revision derived from base_revision, which must be the latest revision.", write,
		func(ctx context.Context, in reviseBindingArgs) (any, error) {
			r, err := t.svc.ReviseBinding(ctx, in.BindingID, memory.BindingRevise{BaseRevision: in.BaseRevision, Config: in.Domain()})
			return wire.NewBindingRevision(r), flatten(err, "revision.")
		})
	add(s, t, "record_execution", "Record one finished run of an exact version. Record children first, then the parent listing them in children.", write,
		func(ctx context.Context, in wire.ExecutionRecord) (any, error) {
			e, err := t.svc.RecordExecution(ctx, in.Domain())
			return wire.NewExecution(e), err
		})
	add(s, t, "get_execution", "Get one execution, with its inputs, evidence and children.", read,
		func(ctx context.Context, in byID) (any, error) {
			e, err := t.svc.Execution(ctx, in.ID)
			return wire.NewExecution(e), err
		})
	add(s, t, "list_executions", "List executions, oldest first, without inputs and evidence: optionally of one procedure and version, in one repository, at one commit.", read,
		func(ctx context.Context, in listExecutionsArgs) (any, error) {
			es, next, err := t.svc.ListExecutions(ctx, memory.ExecutionFilter{ProcedureID: in.ProcedureID, Version: in.Version,
				Repository: in.Repository, Commit: in.Commit, Page: in.page()})
			return wire.NewExecutionList(es, next), err
		})
	add(s, t, "get_verification", "Whether an execution is verified, why not, and the combination it belongs to.", read,
		func(ctx context.Context, in executionRef) (any, error) {
			v, err := t.svc.Verification(ctx, in.ExecutionID)
			return wire.NewVerification(v), err
		})
	add(s, t, "list_verifications", "List a version's execution combinations, each judged by its latest execution.", read,
		func(ctx context.Context, in listVerificationsArgs) (any, error) {
			vs, err := t.svc.Verifications(ctx, memory.VerificationFilter{
				ProcedureID: in.ProcedureID, Version: in.Version, Repository: in.Repository, Commit: in.Commit, Environment: in.Environment,
			})
			return wire.NewVerificationList(vs), err
		})
	add(s, t, "report_feedback", "Report a problem with Polaroid (a confusing error, a missing capability, a tool that misbehaved, a wrong procedure) "+
		"or suggest an improvement. kind is problem or suggestion; summary is one line. "+
		`subject says what it is about: {"type": "service"}, {"type": "repository", "repository_id"}, {"type": "procedure", "procedure_id"[, "version"]}, `+
		`{"type": "binding", "binding_id"[, "revision"]} or {"type": "execution", "execution_id"}; repository and execution_id say where you were. `+
		"context is an optional free-form object. Reports are never changed or deleted.", write,
		func(ctx context.Context, in wire.FeedbackRecord) (any, error) {
			f, err := t.svc.ReportFeedback(ctx, in.Domain())
			return wire.NewFeedback(f), err
		})
	add(s, t, "list_feedback", "List feedback reports, oldest first, optionally by kind, subject and repository.", read,
		func(ctx context.Context, in listFeedbackArgs) (any, error) {
			fs, next, err := t.svc.ListFeedback(ctx, memory.FeedbackFilter{Kind: memory.FeedbackKind(in.Kind), SubjectType: memory.SubjectType(in.SubjectType),
				SubjectID: in.SubjectID, SubjectVersion: in.SubjectVersion, Repository: in.Repository, Page: in.page()})
			return wire.NewFeedbackList(fs, next), err
		})
	add(s, t, "get_feedback", "Get one feedback report.", read,
		func(ctx context.Context, in byID) (any, error) {
			f, err := t.svc.Feedback(ctx, in.ID)
			return wire.NewFeedback(f), err
		})
}

// flatten renames the domain's field paths to the flat argument names,
// for example version.method to method.
func flatten(err error, prefix string) error {
	var invalid *memory.ValidationError
	if !errors.As(err, &invalid) {
		return err
	}
	out := &memory.ValidationError{Problems: slices.Clone(invalid.Problems)}
	for i := range out.Problems {
		out.Problems[i].Field = strings.TrimPrefix(out.Problems[i].Field, prefix)
	}
	return out
}
