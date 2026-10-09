package http

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/ashuangiras/polaroid/internal/memory"
)

func (a *api) createBinding(w http.ResponseWriter, r *http.Request) {
	var body createBindingBody
	if !decode(w, r, &body) {
		return
	}
	h, err := a.svc.CreateBinding(r.Context(), memory.NewBinding{
		Repository:  body.Repository,
		Name:        body.Name,
		ProcedureID: body.ProcedureID,
		Config:      body.Revision.config(),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", bindingPath(h.Binding.ID))
	a.respond(w, r, http.StatusCreated, newBindingHistoryBody(h))
}

// listBindings requires the repository query parameter and rejects any
// other, so a filter this server does not support is never silently ignored.
func (a *api) listBindings(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, errorDetail{Code: "invalid_request", Message: "malformed query string"})
		return
	}
	for name, values := range query {
		if name != "repository" {
			writeError(w, http.StatusBadRequest, errorDetail{Code: "invalid_request", Message: fmt.Sprintf("unknown query parameter %q", name)})
			return
		}
		if len(values) > 1 {
			writeError(w, http.StatusBadRequest, errorDetail{
				Code:    "invalid_request",
				Message: "repository must be given once",
				Fields:  []fieldProblem{{Field: "repository", Message: "must be given once"}},
			})
			return
		}
	}
	bindings, err := a.svc.ListBindings(r.Context(), query.Get("repository"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	body := bindingListBody{Bindings: make([]bindingBody, len(bindings))}
	for i, b := range bindings {
		body.Bindings[i] = newBindingBody(b)
	}
	a.respond(w, r, http.StatusOK, body)
}

func (a *api) getBinding(w http.ResponseWriter, r *http.Request) {
	h, err := a.svc.Binding(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, newBindingHistoryBody(h))
}

func (a *api) reviseBinding(w http.ResponseWriter, r *http.Request) {
	var body reviseBindingBody
	if !decode(w, r, &body) {
		return
	}
	rev, err := a.svc.ReviseBinding(r.Context(), r.PathValue("id"), memory.BindingRevise{
		BaseRevision: body.BaseRevision,
		Config:       body.Revision.config(),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", revisionPath(rev.BindingID, rev.Number))
	a.respond(w, r, http.StatusCreated, newBindingRevisionBody(rev))
}

func (a *api) getBindingRevision(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.Atoi(r.PathValue("revision"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errorDetail{
			Code:    "invalid_request",
			Message: "revision must be a revision number",
			Fields:  []fieldProblem{{Field: "revision", Message: "must be a revision number of at least 1"}},
		})
		return
	}
	rev, err := a.svc.BindingRevision(r.Context(), r.PathValue("id"), number)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, newBindingRevisionBody(rev))
}
