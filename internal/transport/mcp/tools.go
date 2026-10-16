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
		Lifecycle  string `json:"lifecycle,omitzero" jsonschema:"active (the default), retired or all"`
		pageArgs
		snapshotArgs
	}
	procedureRef struct {
		ID           string `json:"id,omitzero" jsonschema:"the procedure ID; give this or canonical_key"`
		CanonicalKey string `json:"canonical_key,omitzero" jsonschema:"the procedure's canonical key; give this or id"`
	}
	discoverArgs struct {
		Task       string `json:"task" jsonschema:"the task you need to perform, in your own words (at most 4096 bytes)"`
		Repository string `json:"repository,omitzero" jsonschema:"search only procedures whose latest version applies in this repository identifier: shared, unspecified, or local to it; omit to search the whole catalog"`
		Lifecycle  string `json:"lifecycle,omitzero" jsonschema:"search active procedures (the default), retired ones, or all"`
		Limit      int    `json:"limit,omitzero" jsonschema:"return at most this many candidates (1 to 50, default 10)"`
	}
	versionRef struct {
		ProcedureID string `json:"procedure_id"`
		Version     int    `json:"version"`
		Fields      string `json:"fields,omitzero" jsonschema:"a comma-separated subset of philosophy,method,goal,applicability,contract,instructions,references,revision_reason: return only those members (plus procedure_id, version, scope, created_at), never a member inside contract or instructions; omit for the complete version"`
	}
	graphArgs struct {
		ProcedureID string `json:"procedure_id"`
		Version     int    `json:"version"`
		Repository  string `json:"repository,omitzero" jsonschema:"resolve contextual references from evidence in this repository; requires environment"`
		Environment string `json:"environment,omitzero" jsonschema:"resolve contextual references from evidence in this environment; requires repository"`
		Format      string `json:"format,omitzero" jsonschema:"full (default): the complete tree. plan: a compact, dependency-ordered projection (procedure_id, version, selected_by, decision, status), children before the parent they compose; see prepare_execution for the target-required, binding-level equivalent"`
		targetArgs
	}
	// targetArgs ask for each selected combination's status at an exact
	// commit and inputs (ADR-0018).
	targetArgs struct {
		Commit    string         `json:"commit,omitzero" jsonschema:"the full commit you will run at; requires inputs. Each node then reports target_verification"`
		Inputs    jsontext.Value `json:"inputs,omitzero" jsonschema:"the root's effective inputs at that commit; requires commit"`
		Decisions jsontext.Value `json:"decisions,omitzero" jsonschema:"your applicability decisions for the conditional references, as an object from reference path (names from the root joined by /) to true or false; requires commit and inputs. A node with an undecided conditional reference below it is never target-verified"`
	}
	createProcedureArgs struct {
		CanonicalKey string          `json:"canonical_key" jsonschema:"the new procedure's unique key: lowercase letters and digits joined by single '.', '-' or '_', at most 128 characters, for example go.dependency.add"`
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
		Repository  string `json:"repository" jsonschema:"the repository identifier, for example github.com/owner/repo; a local procedure version must be local to this repository"`
		Name        string `json:"name" jsonschema:"the binding's name in the repository, unique there, in canonical-key form, for example verify-change"`
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
		Format      string `json:"format,omitzero" jsonschema:"full (default): the complete graph. plan: a compact, dependency-ordered projection; see prepare_execution"`
		targetArgs
	}
	prepareExecutionArgs struct {
		BindingID   string         `json:"binding_id"`
		Environment string         `json:"environment"`
		Commit      string         `json:"commit" jsonschema:"the full commit you will run at (required: preparing an execution always needs an exact target)"`
		Inputs      jsontext.Value `json:"inputs" jsonschema:"the root's effective inputs at that commit (required)"`
		Decisions   jsontext.Value `json:"decisions,omitzero" jsonschema:"your applicability decisions for the conditional references, as in resolve_binding"`
	}
	listExecutionsArgs struct {
		ProcedureID    string `json:"procedure_id,omitzero"`
		Version        int    `json:"version,omitzero" jsonschema:"requires procedure_id"`
		Repository     string `json:"repository,omitzero" jsonschema:"a repository identifier; a registered one matches every identifier of its repository"`
		Commit         string `json:"commit,omitzero"`
		EvidenceStatus string `json:"evidence_status,omitzero" jsonschema:"only valid or only invalidated executions"`
		pageArgs
	}
	executionRef struct {
		ExecutionID string `json:"execution_id"`
	}
	invalidateArgs struct {
		ExecutionID string `json:"execution_id"`
		wire.InvalidationRequest
	}
	lifecycleArgs struct {
		ProcedureID string `json:"procedure_id"`
		wire.LifecycleChange
	}
	procedureIDArgs struct {
		ProcedureID string `json:"procedure_id"`
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
		Status         string `json:"status,omitzero" jsonschema:"only reports with this status: open or resolved; omitted lists every report"`
		pageArgs
	}
	resolveFeedbackArgs struct {
		FeedbackID string `json:"feedback_id"`
		wire.FeedbackResolutionRequest
	}
	createToilItemArgs struct {
		CanonicalKey string `json:"canonical_key" jsonschema:"a new key in canonical-key form; an existing toil item's key is a canonical_key_exists conflict"`
		wire.ToilContent
	}
	reviseToilItemArgs struct {
		ToilItemID   string `json:"toil_item_id"`
		BaseRevision int    `json:"base_revision" jsonschema:"the latest revision you read; a stale base is a revision_conflict"`
		wire.ToilContent
	}
	toilItemRef struct {
		ID           string `json:"id,omitzero" jsonschema:"the toil item ID; give this or canonical_key"`
		CanonicalKey string `json:"canonical_key,omitzero" jsonschema:"the toil item's canonical key; give this or id"`
	}
	toilRevisionRef struct {
		ToilItemID string `json:"toil_item_id"`
		Revision   int    `json:"revision"`
	}
	listToilItemsArgs struct {
		Status      string `json:"status,omitzero" jsonschema:"only items whose latest revision has this status: observed, candidate, implemented, validated or retired"`
		Repository  string `json:"repository,omitzero" jsonschema:"only shared items and items local to this repository identifier's repository (by identity, aliases included)"`
		ProcedureID string `json:"procedure_id,omitzero" jsonschema:"only items whose latest revision cites this procedure, at any version, as a source"`
		pageArgs
		snapshotArgs
	}
	// listWalkthroughsArgs is an operator's list_walkthroughs (ADR-0056),
	// on this legacy surface, not /mcp/agent/v3.
	listWalkthroughsArgs struct {
		Repository string `json:"repository,omitzero" jsonschema:"only walkthroughs stored with this repository identifier"`
		Goal       string `json:"goal,omitzero" jsonschema:"only walkthroughs for this goal"`
		Status     string `json:"status,omitzero" jsonschema:"only walkthroughs with this coarse status: active, escalate or blocked; omitted lists every one. Coarser than /mcp/agent/v3's own in_progress/complete: this surface has no access to the composition tree, so active also covers an already fully done walkthrough"`
		pageArgs
	}
	walkthroughRef struct {
		Token string `json:"token"`
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

func (a listToilItemsArgs) page() memory.Page {
	return memory.Page{Limit: a.Limit, After: a.After, Snapshot: a.Snapshot}
}

func (a targetArgs) target() *memory.Target {
	if a.Commit == "" && a.Inputs == nil && a.Decisions == nil {
		return nil
	}
	return &memory.Target{Commit: a.Commit, Inputs: a.Inputs, Decisions: a.Decisions}
}

func (t *tools) register(s *sdk.Server) {
	const read, write = true, false

	add(s, t, "list_procedures", "List procedures, ordered by canonical key, with origin, lifecycle_status and the scope (shared, local or unspecified), goal and applicability of version latest_version only. "+
		"Unspecified declares nothing: admissible everywhere, not a claim of reuse. Before reusing one, resolve it and read the selected version's scope and contract. "+
		"Retired procedures are left out unless lifecycle is retired or all. "+
		"Pass repository to see only the procedures that apply in it, scope or q to narrow further, limit to page, and snapshot to page a fixed snapshot.", read,
		func(ctx context.Context, in listProceduresArgs) (any, error) {
			ps, next, err := t.svc.ListProcedures(ctx, memory.ProcedureFilter{Repository: in.Repository, Scope: in.Scope, Query: in.Q, Lifecycle: in.Lifecycle, Page: in.page()})
			return wire.NewProcedureList(ps, next), err
		})
	add(s, t, "discover_procedures", "Find procedures for a task described in your own words, by lexical matching (shared words, not meaning). "+
		"Searches each procedure's latest version: canonical key, goal, method, philosophy, and the text of contract and instructions. "+
		"Candidates are ranked by score (ADR-0033): each matched term adds its rarity among the candidates times the weight of the best field holding it (4 canonical key or goal, 2 method, 1 philosophy, contract or instructions; long prose weighs less), and contract and instruction matches together are capped. "+
		"contributions and matches explain each candidate. The score ranks, it is not a confidence. "+
		"Retired procedures are searched only with lifecycle retired or all. "+
		"Subprocedures are found like any procedure. A candidate is not a recommendation and not verified for you: read its version, judge it, and resolve it before reuse.", read,
		func(ctx context.Context, in discoverArgs) (any, error) {
			d, err := t.svc.DiscoverProcedures(ctx, memory.DiscoveryRequest{Task: in.Task, Repository: in.Repository, Lifecycle: in.Lifecycle, Limit: in.Limit})
			return wire.NewDiscovery(d), err
		})
	add(s, t, "suggest_duplicates", "Before create_procedure or revise_procedure, check a proposal (method, philosophy, optional goal and canonical_key) against existing procedures, retired ones included. "+
		"key_collision names a procedure that already has the canonical key, which create_procedure would refuse. "+
		"suggestions are advisory content overlaps: similarity is 2 x shared terms / (proposal terms + candidate terms) over canonical key, goal, method and philosophy, at least 0.35. "+
		"A retired match (lifecycle_status retired) is knowledge that was withdrawn: reactivate and revise it rather than create a second identity, if it fits. "+
		"Pass exclude_procedure_id when revising. Reuse, revise or create is your decision; nothing is stored.", read,
		func(ctx context.Context, in wire.DuplicateCheck) (any, error) {
			d, err := t.svc.SuggestDuplicates(ctx, in.Domain())
			return wire.NewDuplicates(d), err
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
	add(s, t, "get_version", "Get one version of a procedure. Pass fields (for example instructions, or contract,instructions) to get only those top-level members plus its identity (procedure_id, version, scope, created_at), instead of the complete version; contract and instructions are each selected whole, never a member inside either.", read,
		func(ctx context.Context, in versionRef) (any, error) {
			v, err := t.svc.Version(ctx, in.ProcedureID, in.Version)
			if err != nil {
				return nil, err
			}
			if in.Fields == "" {
				return wire.NewVersion(v), nil
			}
			fields, ferr := wire.ParseFields(in.Fields, wire.VersionFieldNames)
			if ferr != nil {
				return nil, &memory.ValidationError{Problems: []memory.FieldProblem{{Field: "fields", Message: ferr.Error()}}}
			}
			return wire.NewVersionProjection(v, fields), nil
		})
	add(s, t, "get_graph", "Get a version's composition graph with the exact version each reference selects. "+
		"With repository and environment, contextual references resolve from verification evidence, reported per node as selection_evidence (any commit). "+
		"Add commit and inputs to get target_verification: whether each selected combination is verified at that exact commit and inputs. "+
		"References with a condition apply only when it holds: decide each for your task and pass decisions; the graph always shows every reference and its condition. "+
		"Pass format=plan for a compact, dependency-ordered projection instead of the complete tree.", read,
		func(ctx context.Context, in graphArgs) (any, error) {
			if in.Format != "" && in.Format != "full" && in.Format != "plan" {
				return nil, &memory.ValidationError{Problems: []memory.FieldProblem{{Field: "format", Message: "must be full or plan"}}}
			}
			g, err := t.svc.CompositionGraph(ctx, in.ProcedureID, in.Version,
				memory.ResolutionContext{Repository: in.Repository, Environment: in.Environment, Target: in.target()})
			if err != nil {
				return nil, err
			}
			if in.Format == "plan" {
				return wire.NewGraphPlan(g), nil
			}
			return wire.NewGraphNode(g), nil
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
		"Add commit and inputs to get target_verification for each selected combination at exactly that commit and inputs, and decisions for its conditional references. "+
		"Pass format=plan for a compact, dependency-ordered projection instead of the complete graph; prepare_execution is the same projection with a target required.", read,
		func(ctx context.Context, in resolveBindingArgs) (any, error) {
			if in.Format != "" && in.Format != "full" && in.Format != "plan" {
				return nil, &memory.ValidationError{Problems: []memory.FieldProblem{{Field: "format", Message: "must be full or plan"}}}
			}
			r, err := t.svc.ResolveBinding(ctx, in.BindingID, in.Environment, in.target())
			if err != nil {
				return nil, err
			}
			if in.Format == "plan" {
				return wire.NewBindingPlan(r), nil
			}
			return wire.NewBindingResolution(r), nil
		})
	add(s, t, "prepare_execution", "Before running anything, get a compact, dependency-ordered execution plan for a binding at an exact commit and inputs: one step per node (procedure_id, version, selected_by, decision, status), children before the parent they compose, and next_action naming the first step still needing a decision or a run. "+
		"Uses the same resolution and verification as resolve_binding and get_graph, computed the same way: it is a thin, additive projection, not a new engine or stored state. A step's status verified reads the same derived verification as get_verification; the plan itself is never evidence of a successful run. Fetch a step's full version with get_version (optionally with fields) when you need its contract or instructions.", read,
		func(ctx context.Context, in prepareExecutionArgs) (any, error) {
			r, err := t.svc.ResolveBinding(ctx, in.BindingID, in.Environment, &memory.Target{Commit: in.Commit, Inputs: in.Inputs, Decisions: in.Decisions})
			if err != nil {
				return nil, err
			}
			return wire.NewBindingPlan(r), nil
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
	add(s, t, "record_execution", "Record one finished run of an exact version. Record children first, each with exactly the inputs its reference maps from the parent's inputs, then the parent listing them in children. "+
		"For every reference with a condition, add a decision {reference, applicable, rationale}: applicable true needs its child; false needs a concrete rationale and no child. Required references cannot be skipped. "+
		"Keep evidence concise (results, short excerpts, references to logs and commits); evidence over 64 KiB, inputs over 16 KiB and other values over their limits are value_too_large.", write,
		func(ctx context.Context, in wire.ExecutionRecord) (any, error) {
			e, err := t.svc.RecordExecution(ctx, in.Domain())
			return wire.NewExecution(e), err
		})
	add(s, t, "get_execution", "Get one execution, with its inputs, evidence, children and applicability decisions.", read,
		func(ctx context.Context, in byID) (any, error) {
			e, err := t.svc.Execution(ctx, in.ID)
			return wire.NewExecution(e), err
		})
	add(s, t, "list_executions", "List executions, oldest first, without inputs and evidence: optionally of one procedure and version, in one repository, at one commit, with one evidence_status (valid or invalidated).", read,
		func(ctx context.Context, in listExecutionsArgs) (any, error) {
			es, next, err := t.svc.ListExecutions(ctx, memory.ExecutionFilter{ProcedureID: in.ProcedureID, Version: in.Version,
				Repository: in.Repository, Commit: in.Commit, EvidenceStatus: memory.EvidenceStatus(in.EvidenceStatus), Page: in.page()})
			return wire.NewExecutionList(es, next), err
		})
	add(s, t, "get_verification", "Whether an execution is verified, why not, and the combination it belongs to. An invalidated execution is never verified (problem invalidated); a parent of one reports child_invalidated.", read,
		func(ctx context.Context, in executionRef) (any, error) {
			v, err := t.svc.Verification(ctx, in.ExecutionID)
			return wire.NewVerification(v), err
		})
	add(s, t, "list_verifications", "List a version's execution combinations, each judged by its latest valid execution; invalidated ones are listed in invalidated_execution_ids and never decide.", read,
		func(ctx context.Context, in listVerificationsArgs) (any, error) {
			vs, err := t.svc.Verifications(ctx, memory.VerificationFilter{
				ProcedureID: in.ProcedureID, Version: in.Version, Repository: in.Repository, Commit: in.Commit, Environment: in.Environment,
			})
			return wire.NewVerificationList(vs), err
		})
	add(s, t, "report_feedback", "Report a problem with Polaroid (a confusing error, a missing capability, a tool that misbehaved, a wrong procedure) "+
		"or suggest an improvement. kind is problem or suggestion; summary is one line. "+
		"reporter names you in canonical-key form: lowercase letters and digits joined by single '.', '-' or '_', for example copilot.vscode. "+
		`subject says what it is about: {"type": "service"}, {"type": "repository", "repository_id"}, {"type": "procedure", "procedure_id"[, "version"]}, `+
		`{"type": "binding", "binding_id"[, "revision"]} or {"type": "execution", "execution_id"}; repository and execution_id say where you were. `+
		"context is an optional free-form object. Reports are never changed or deleted.", write,
		func(ctx context.Context, in wire.FeedbackRecord) (any, error) {
			f, err := t.svc.ReportFeedback(ctx, in.Domain())
			return wire.NewFeedback(f), err
		})
	add(s, t, "list_feedback", "List feedback reports, oldest first, optionally by kind, subject, repository and status. Without status, every report is listed, open and resolved alike; status=open answers what is still unresolved.", read,
		func(ctx context.Context, in listFeedbackArgs) (any, error) {
			fs, next, err := t.svc.ListFeedback(ctx, memory.FeedbackFilter{Kind: memory.FeedbackKind(in.Kind), SubjectType: memory.SubjectType(in.SubjectType),
				SubjectID: in.SubjectID, SubjectVersion: in.SubjectVersion, Repository: in.Repository, Status: memory.FeedbackStatus(in.Status), Page: in.page()})
			return wire.NewFeedbackList(fs, next), err
		})
	add(s, t, "get_feedback", "Get one feedback report.", read,
		func(ctx context.Context, in byID) (any, error) {
			f, err := t.svc.Feedback(ctx, in.ID)
			return wire.NewFeedback(f), err
		})
	add(s, t, "resolve_feedback", "Record how a feedback report was handled, permanently: outcome is addressed, declined, not_reproduced or duplicate. "+
		"actor names you in canonical-key form, like report_feedback's reporter. references is optional: issue URLs, PRs, commits, or the procedure version or execution that addressed it. "+
		"The report stays unchanged and readable; a second resolution is feedback_resolved. If the problem comes back, report it again rather than reopening this one.", write,
		func(ctx context.Context, in resolveFeedbackArgs) (any, error) {
			r, err := t.svc.ResolveFeedback(ctx, in.FeedbackID, in.Domain())
			return wire.NewFeedbackResolution(r), err
		})
	t.registerToil(s)
	t.registerLifecycle(s)
	t.registerWalkthroughs(s)
	add(s, t, "get_catalog_statistics", "Read-only statistics of the whole catalog (statistics_version 1): record counts (identities, their revisions and relationship records), "+
		"procedures by lifecycle and scope, executions by recorded outcome and by evidence status (an outcome is not verification), toil items by latest status, "+
		"logical byte sizes of the stored JSON payloads (count, total, mean, max), database pages and file sizes, and write_limits: the byte limit of every bounded field of a write and of the whole write. "+
		"Counts and payloads are one snapshot; file sizes are read just after it and may differ. Takes no arguments and writes nothing.", read,
		func(ctx context.Context, _ struct{}) (any, error) {
			st, err := t.svc.Statistics(ctx)
			return wire.NewStatistics(st), err
		})
}

// registerLifecycle adds the evidence invalidation and procedure retirement
// tools (ADR-0035).
func (t *tools) registerLifecycle(s *sdk.Server) {
	const read, write = true, false
	add(s, t, "invalidate_execution", "Withdraw one execution as evidence, permanently, with a precise reason and your actor name (canonical-key form). "+
		"Use it when the run did not do what its procedure version claims, for example it was recorded against the wrong version. "+
		"The execution and its evidence stay unchanged and readable; it stops supporting contextual selection and target verification, and parents linking it are no longer verified. "+
		"Nothing replaces it automatically: record a new genuine execution. A second invalidation is execution_invalidated.", write,
		func(ctx context.Context, in invalidateArgs) (any, error) {
			st, err := t.svc.InvalidateExecution(ctx, in.ExecutionID, in.Domain())
			return wire.NewInvalidationState(st), err
		})
	add(s, t, "get_execution_invalidation", "Whether an execution is still valid evidence, and its invalidation (reason, actor, created_at) if it was withdrawn.", read,
		func(ctx context.Context, in executionRef) (any, error) {
			st, err := t.svc.ExecutionInvalidation(ctx, in.ExecutionID)
			return wire.NewInvalidationState(st), err
		})
	add(s, t, "get_procedure_lifecycle", "A procedure's lifecycle state (active or retired), latest_event and every retirement and reactivation with its reason and actor.", read,
		func(ctx context.Context, in procedureIDArgs) (any, error) {
			l, err := t.svc.ProcedureLifecycle(ctx, in.ProcedureID)
			return wire.NewLifecycle(l), err
		})
	add(s, t, "retire_procedure", "Retire a procedure so it is no longer used for new work, with base_event (the latest lifecycle event you read, 0 if none), a reason and your actor name. "+
		"It leaves discovery and list_procedures by default; its versions, bindings, references and executions stay unchanged and resolvable, and graph nodes of it carry retirement. "+
		"New versions of it, new references to it and new bindings or binding revisions of it are refused (procedure_retired). Parents are not retired with it.", write,
		func(ctx context.Context, in lifecycleArgs) (any, error) {
			l, err := t.svc.RetireProcedure(ctx, in.ProcedureID, in.Domain())
			return wire.NewLifecycle(l), err
		})
	add(s, t, "reactivate_procedure", "Reactivate a retired procedure, with base_event (the latest lifecycle event you read), a reason and your actor name. The retirement stays in its history.", write,
		func(ctx context.Context, in lifecycleArgs) (any, error) {
			l, err := t.svc.ReactivateProcedure(ctx, in.ProcedureID, in.Domain())
			return wire.NewLifecycle(l), err
		})
}

// registerToil adds the toil item tools (ADR-0034).
func (t *tools) registerToil(s *sdk.Server) {
	const read, write = true, false
	add(s, t, "list_toil_items", "List toil items (assessments of repetitive work that may be automated), ordered by canonical key, as summaries of their latest revision: activity, status, scope. "+
		"Filter by status, repository (shared items and items local to it) and procedure_id (items citing it as a source). "+
		"Look here before create_toil_item: different keys may describe the same activity, and nothing detects that for you. Pass limit to page, and snapshot to page a fixed snapshot.", read,
		func(ctx context.Context, in listToilItemsArgs) (any, error) {
			items, next, err := t.svc.ListToilItems(ctx, memory.ToilFilter{Status: memory.ToilStatus(in.Status), Repository: in.Repository,
				ProcedureID: in.ProcedureID, Page: in.page()})
			return wire.NewToilItemList(items, next), err
		})
	add(s, t, "get_toil_item", "Get a toil item and all of its revisions, by id or by canonical_key.", read,
		func(ctx context.Context, in toilItemRef) (any, error) {
			var h memory.ToilHistory
			var err error
			switch {
			case in.ID != "" && in.CanonicalKey != "":
				return nil, &memory.ValidationError{Problems: []memory.FieldProblem{{Field: "canonical_key", Message: "must not be given with id"}}}
			case in.ID != "":
				h, err = t.svc.ToilItem(ctx, in.ID)
			case in.CanonicalKey != "":
				h, err = t.svc.ToilItemByKey(ctx, in.CanonicalKey)
			default:
				return nil, required("id")
			}
			return wire.NewToilHistory(h), err
		})
	add(s, t, "get_toil_revision", "Get one revision of a toil item.", read,
		func(ctx context.Context, in toilRevisionRef) (any, error) {
			r, err := t.svc.ToilRevision(ctx, in.ToilItemID, in.Revision)
			return wire.NewToilRevision(r), err
		})
	const content = "activity is one line; applicability is {\"shared\": {}} or {\"repository\": \"<repository id>\"}. " +
		"sources are exact procedure versions {procedure_id, version, steps?}; steps are declared identifiers, never checked against instructions. " +
		"observations are supporting executions {execution_id, note}; a failed run is valid support. " +
		"assessment is free-form, but repetition.status and burden.status must be observed, hypothesis or unknown: keep what you saw apart from what you assume. " +
		"proposal (inputs, outputs, remaining_judgment, failure_and_recovery, acceptance_criteria) is required from candidate on; replacement {summary, references} for implemented and validated; " +
		"validation {rationale, executions, references} only for validated (or retired). References are declared, never fetched. " +
		"status is your assessment, not verification: observed, candidate, implemented, validated, retired, with status_rationale. author names you in canonical-key form."
	add(s, t, "create_toil_item", "Create a toil item, an evidence-backed assessment of repetitive work that may be automated, with its revision 1 (status observed or candidate). "+content, write,
		func(ctx context.Context, in createToilItemArgs) (any, error) {
			h, err := t.svc.CreateToilItem(ctx, memory.NewToilItem{CanonicalKey: in.CanonicalKey, Content: in.Domain()})
			return wire.NewToilHistory(h), flatten(err, "revision.")
		})
	add(s, t, "revise_toil_item", "Append a complete revision of a toil item derived from base_revision, which must be the latest. "+
		"Statuses follow observed -> candidate -> implemented -> validated; candidate may go back to observed; validated or implemented go back to candidate when a replacement fails; any status may be retired, and retired reopens as observed; every status may stay the same. "+content, write,
		func(ctx context.Context, in reviseToilItemArgs) (any, error) {
			r, err := t.svc.ReviseToilItem(ctx, in.ToilItemID, memory.ToilRevise{BaseRevision: in.BaseRevision, Content: in.Domain()})
			return wire.NewToilRevision(r), flatten(err, "revision.")
		})
}

// flatten renames the domain's field paths to the flat argument names,
// for example version.method to method.
func flatten(err error, prefix string) error {
	var tooLarge *memory.LimitError
	if errors.As(err, &tooLarge) {
		out := &memory.LimitError{Violations: slices.Clone(tooLarge.Violations)}
		for i := range out.Violations {
			out.Violations[i].Field = strings.TrimPrefix(out.Violations[i].Field, prefix)
		}
		return out
	}
	var retired *memory.RetiredProcedureError
	if errors.As(err, &retired) {
		out := *retired
		out.Fields = slices.Clone(retired.Fields)
		for i := range out.Fields {
			out.Fields[i].Field = strings.TrimPrefix(out.Fields[i].Field, prefix)
		}
		return &out
	}
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

// registerWalkthroughs adds an operator's read-only visibility into
// guided-procedure walkthroughs (ADR-0056): list_walkthroughs and
// get_walkthrough, on this legacy surface, not /mcp/agent/v3's own
// start/advance (registered separately, at v3 only; see agent.go).
func (t *tools) registerWalkthroughs(s *sdk.Server) {
	const read = true
	add(s, t, "list_walkthroughs", "List guided-procedure walkthroughs an agent has started with /mcp/agent/v3's start, oldest first, optionally by repository, goal and status. "+
		"status is active, escalate or blocked (coarser than start/advance's own in_progress/complete: this surface cannot see the composition tree, so active also covers an already fully done walkthrough). "+
		"A walkthrough not touched again within its TTL is gone; this never lists one already expired or closed.", read,
		func(ctx context.Context, in listWalkthroughsArgs) (any, error) {
			states, next, err := t.svc.ListWalkthroughs(ctx, memory.WalkthroughFilter{Repository: in.Repository, Goal: in.Goal, Status: in.Status, Page: in.page()})
			return wire.NewWalkthroughList(states, next), err
		})
	add(s, t, "get_walkthrough", "Get one guided-procedure walkthrough's current state: which keys have a reported outcome, and (for a blocked one) why, by token. "+
		"A read-only peek: it does not reset the walkthrough's own TTL the way /mcp/agent/v3's advance does.", read,
		func(ctx context.Context, in walkthroughRef) (any, error) {
			st, err := t.svc.GetWalkthrough(ctx, in.Token)
			return wire.NewWalkthrough(st), err
		})
}
