package http

import (
	"net/http"
	"net/url"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

func (a *api) reportFeedback(w http.ResponseWriter, r *http.Request) {
	var body wire.FeedbackRecord
	if !decode(w, r, &body) {
		return
	}
	f, err := a.svc.ReportFeedback(r.Context(), body.Domain())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/feedback/"+url.PathEscape(f.ID))
	a.respond(w, r, http.StatusCreated, wire.NewFeedback(f))
}

func (a *api) getFeedback(w http.ResponseWriter, r *http.Request) {
	f, err := a.svc.Feedback(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewFeedback(f))
}

func (a *api) listFeedback(w http.ResponseWriter, r *http.Request) {
	query, ok := strictQuery(w, r, "kind")
	if !ok {
		return
	}
	kind := memory.FeedbackKind(query.Get("kind"))
	if query.Has("kind") && kind == "" {
		writeError(w, http.StatusBadRequest, wire.InvalidRequest("kind must not be empty", "kind", `must be "problem" or "suggestion"`))
		return
	}
	reports, err := a.svc.ListFeedback(r.Context(), kind)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewFeedbackList(reports))
}
