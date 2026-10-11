package http

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

func (a *api) createToilItem(w http.ResponseWriter, r *http.Request) {
	var body createToilItemBody
	if !decode(w, r, &body) {
		return
	}
	h, err := a.svc.CreateToilItem(r.Context(), memory.NewToilItem{CanonicalKey: body.CanonicalKey, Content: body.Revision.Domain()})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", toilPath(h.Item.ID))
	a.respond(w, r, http.StatusCreated, wire.NewToilHistory(h))
}

func (a *api) listToilItems(w http.ResponseWriter, r *http.Request) {
	query, ok := strictQuery(w, r, "status", "repository", "procedure_id", "limit", "after", "snapshot")
	if !ok {
		return
	}
	page, ok := snapshotPageQuery(w, query)
	if !ok || !nonEmpty(w, query, "status", "repository", "procedure_id") {
		return
	}
	items, next, err := a.svc.ListToilItems(r.Context(), memory.ToilFilter{
		Status: memory.ToilStatus(query.Get("status")), Repository: query.Get("repository"), ProcedureID: query.Get("procedure_id"), Page: page,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewToilItemList(items, next))
}

func (a *api) getToilItem(w http.ResponseWriter, r *http.Request) {
	h, err := a.svc.ToilItem(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewToilHistory(h))
}

func (a *api) getToilItemByKey(w http.ResponseWriter, r *http.Request) {
	h, err := a.svc.ToilItemByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewToilHistory(h))
}

func (a *api) reviseToilItem(w http.ResponseWriter, r *http.Request) {
	var body reviseToilItemBody
	if !decode(w, r, &body) {
		return
	}
	rev, err := a.svc.ReviseToilItem(r.Context(), r.PathValue("id"), memory.ToilRevise{BaseRevision: body.BaseRevision, Content: body.Revision.Domain()})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", toilPath(rev.ToilItemID)+"/revisions/"+strconv.Itoa(rev.Number))
	a.respond(w, r, http.StatusCreated, wire.NewToilRevision(rev))
}

func (a *api) getToilRevision(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.Atoi(r.PathValue("revision"))
	if err != nil {
		writeError(w, http.StatusBadRequest, wire.InvalidRequest("revision must be a revision number", "revision", "must be a revision number of at least 1"))
		return
	}
	rev, err := a.svc.ToilRevision(r.Context(), r.PathValue("id"), number)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewToilRevision(rev))
}

func toilPath(id string) string {
	return "/v1/toil-items/" + url.PathEscape(id)
}
