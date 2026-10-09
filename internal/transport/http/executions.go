package http

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/ashuangiras/polaroid/internal/memory"
)

func (a *api) recordExecution(w http.ResponseWriter, r *http.Request) {
	var body recordExecutionBody
	if !decode(w, r, &body) {
		return
	}
	e, err := a.svc.RecordExecution(r.Context(), body.record())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/executions/"+url.PathEscape(e.ID))
	a.respond(w, r, http.StatusCreated, newExecutionBody(e))
}

func (a *api) getExecution(w http.ResponseWriter, r *http.Request) {
	e, err := a.svc.Execution(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, newExecutionBody(e))
}

func (a *api) listExecutions(w http.ResponseWriter, r *http.Request) {
	query, ok := strictQuery(w, r, "procedure_id", "version", "repository")
	if !ok {
		return
	}
	f := memory.ExecutionFilter{ProcedureID: query.Get("procedure_id"), Repository: query.Get("repository")}
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
	executions, err := a.svc.ListExecutions(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	body := executionListBody{Executions: make([]executionSummaryBody, len(executions))}
	for i, e := range executions {
		body.Executions[i] = newExecutionSummaryBody(e)
	}
	a.respond(w, r, http.StatusOK, body)
}

func (a *api) getVerification(w http.ResponseWriter, r *http.Request) {
	v, err := a.svc.Verification(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, newVerificationBody(v))
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
	body := verificationListBody{Verifications: make([]combinationStatusBody, len(statuses))}
	for i, s := range statuses {
		body.Verifications[i] = combinationStatusBody{
			Combination:       newCombinationBody(s.Combination),
			Verified:          s.Verified,
			LatestExecutionID: s.LatestExecutionID,
			ExecutionIDs:      s.ExecutionIDs,
		}
	}
	a.respond(w, r, http.StatusOK, body)
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
