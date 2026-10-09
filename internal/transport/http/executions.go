package http

import (
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

func (a *api) recordExecution(w http.ResponseWriter, r *http.Request) {
	var body wire.ExecutionRecord
	if !decode(w, r, &body) {
		return
	}
	e, err := a.svc.RecordExecution(r.Context(), body.Domain())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/executions/"+url.PathEscape(e.ID))
	a.respond(w, r, http.StatusCreated, wire.NewExecution(e))
}

func (a *api) getExecution(w http.ResponseWriter, r *http.Request) {
	e, err := a.svc.Execution(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewExecution(e))
}

func (a *api) listExecutions(w http.ResponseWriter, r *http.Request) {
	query, ok := strictQuery(w, r, "procedure_id", "version", "repository", "commit", "limit", "after")
	if !ok {
		return
	}
	page, ok := pageQuery(w, query)
	if !ok || !nonEmpty(w, query, "procedure_id", "repository", "commit") {
		return
	}
	f := memory.ExecutionFilter{ProcedureID: query.Get("procedure_id"), Repository: query.Get("repository"), Commit: query.Get("commit"), Page: page}
	if v := query.Get("version"); query.Has("version") {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, errorDetail{
				Code:    "invalid_request",
				Message: "version must be a version number",
				Fields:  []fieldProblem{{Field: "version", Message: "must be a version number of at least 1"}},
			})
			return
		}
		f.Version = n
	}
	executions, next, err := a.svc.ListExecutions(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewExecutionList(executions, next))
}

func (a *api) getVerification(w http.ResponseWriter, r *http.Request) {
	v, err := a.svc.Verification(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewVerification(v))
}

func (a *api) listVerifications(w http.ResponseWriter, r *http.Request) {
	number, ok := versionNumber(w, r)
	if !ok {
		return
	}
	query, ok := strictQuery(w, r, "repository", "commit", "environment")
	if !ok {
		return
	}
	statuses, err := a.svc.Verifications(r.Context(), memory.VerificationFilter{
		ProcedureID: r.PathValue("id"),
		Version:     number,
		Repository:  query.Get("repository"),
		Commit:      query.Get("commit"),
		Environment: query.Get("environment"),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewVerificationList(statuses))
}

// pageQuery parses the optional limit and after parameters (ADR-0021). On
// failure it writes the error response.
func pageQuery(w http.ResponseWriter, query url.Values) (memory.Page, bool) {
	page := memory.Page{After: query.Get("after")}
	if query.Has("limit") {
		n, err := strconv.Atoi(query.Get("limit"))
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, wire.InvalidRequest("limit must be a number", "limit",
				fmt.Sprintf("must be a number from 1 to %d", memory.MaxPageLimit)))
			return memory.Page{}, false
		}
		page.Limit = n
	}
	if query.Has("after") && page.After == "" {
		writeError(w, http.StatusBadRequest, wire.InvalidRequest("after must not be empty", "after", "must be the next cursor of an earlier page"))
		return memory.Page{}, false
	}
	return page, true
}

// snapshotPageQuery is pageQuery plus the snapshot parameter, true or false
// (ADR-0023), of the lists that offer it.
func snapshotPageQuery(w http.ResponseWriter, query url.Values) (memory.Page, bool) {
	page, ok := pageQuery(w, query)
	if !ok || !query.Has("snapshot") {
		return page, ok
	}
	switch query.Get("snapshot") {
	case "true":
		page.Snapshot = true
	case "false":
	default:
		writeError(w, http.StatusBadRequest, wire.InvalidRequest("snapshot must be true or false", "snapshot", "must be true or false"))
		return memory.Page{}, false
	}
	return page, true
}

// nonEmpty rejects a given but empty filter parameter, so it is never
// mistaken for an absent one. On failure it writes the error response.
func nonEmpty(w http.ResponseWriter, query url.Values, names ...string) bool {
	for _, name := range names {
		if query.Has(name) && query.Get(name) == "" {
			writeError(w, http.StatusBadRequest, wire.InvalidRequest(name+" must not be empty", name, "must not be empty"))
			return false
		}
	}
	return true
}

// targetQuery returns a resolution's optional target: commit and inputs (a
// JSON object), which the service requires together.
func targetQuery(query url.Values) *memory.Target {
	if !query.Has("commit") && !query.Has("inputs") {
		return nil
	}
	return &memory.Target{Commit: query.Get("commit"), Inputs: jsontext.Value(query.Get("inputs"))}
}

// strictQuery parses the query string, accepting only the allowed
// parameters, each at most once, so a filter this server does not support
// is never silently ignored. On failure it writes the error response.
func strictQuery(w http.ResponseWriter, r *http.Request, allowed ...string) (url.Values, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, errorDetail{Code: "invalid_request", Message: "malformed query string"})
		return nil, false
	}
	for name, values := range query {
		if !slices.Contains(allowed, name) {
			writeError(w, http.StatusBadRequest, errorDetail{Code: "invalid_request", Message: fmt.Sprintf("unknown query parameter %q", name)})
			return nil, false
		}
		if len(values) > 1 {
			writeError(w, http.StatusBadRequest, errorDetail{
				Code:    "invalid_request",
				Message: name + " must be given once",
				Fields:  []fieldProblem{{Field: name, Message: "must be given once"}},
			})
			return nil, false
		}
	}
	return query, true
}
