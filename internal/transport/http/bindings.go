package http

import (
	"net/http"
	"strconv"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
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
		Config:      body.Revision.Domain(),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", bindingPath(h.Binding.ID))
	a.respond(w, r, http.StatusCreated, wire.NewBindingHistory(h))
}

// listBindings requires the repository query parameter and rejects any
// other, so a filter this server does not support is never silently ignored.
func (a *api) listBindings(w http.ResponseWriter, r *http.Request) {
	query, ok := strictQuery(w, r, "repository", "limit", "after", "snapshot")
	if !ok {
		return
	}
	page, ok := snapshotPageQuery(w, query)
	if !ok {
		return
	}
	bindings, next, err := a.svc.ListBindings(r.Context(), query.Get("repository"), page)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewBindingList(bindings, next))
}

func (a *api) getBinding(w http.ResponseWriter, r *http.Request) {
	h, err := a.svc.Binding(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewBindingHistory(h))
}

func (a *api) resolveBinding(w http.ResponseWriter, r *http.Request) {
	query, ok := strictQuery(w, r, "environment", "commit", "inputs", "decisions", "format")
	if !ok {
		return
	}
	format, ok := planFormat(w, query)
	if !ok {
		return
	}
	res, err := a.svc.ResolveBinding(r.Context(), r.PathValue("id"), query.Get("environment"), targetQuery(query))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if format == "plan" {
		a.respond(w, r, http.StatusOK, wire.NewBindingPlan(res))
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewBindingResolution(res))
}

// prepareExecution is resolve_binding, always projected to the compact plan
// (#131), with a target required: preparing to run implies an exact commit
// and effective inputs, unlike resolve_binding's optional target. It is a
// thin, additive wrapper: the same ResolveBinding the HTTP and MCP
// resolution calls already use, never a new engine or stored state.
func (a *api) prepareExecution(w http.ResponseWriter, r *http.Request) {
	query, ok := strictQuery(w, r, "environment", "commit", "inputs", "decisions")
	if !ok {
		return
	}
	var fields []fieldProblem
	if !query.Has("commit") {
		fields = append(fields, fieldProblem{Field: "commit", Message: "is required to prepare an execution"})
	}
	if !query.Has("inputs") {
		fields = append(fields, fieldProblem{Field: "inputs", Message: "is required to prepare an execution"})
	}
	if len(fields) > 0 {
		writeError(w, http.StatusBadRequest, errorDetail{Code: "invalid_request", Message: "commit and inputs are required to prepare an execution", Fields: fields})
		return
	}
	res, err := a.svc.ResolveBinding(r.Context(), r.PathValue("id"), query.Get("environment"), targetQuery(query))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewBindingPlan(res))
}

func (a *api) reviseBinding(w http.ResponseWriter, r *http.Request) {
	var body reviseBindingBody
	if !decode(w, r, &body) {
		return
	}
	rev, err := a.svc.ReviseBinding(r.Context(), r.PathValue("id"), memory.BindingRevise{
		BaseRevision: body.BaseRevision,
		Config:       body.Revision.Domain(),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", revisionPath(rev.BindingID, rev.Number))
	a.respond(w, r, http.StatusCreated, wire.NewBindingRevision(rev))
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
	a.respond(w, r, http.StatusOK, wire.NewBindingRevision(rev))
}
