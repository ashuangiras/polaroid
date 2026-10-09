package http

import (
	"net/http"
	"net/url"
	"strconv"

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
	query, ok := strictQuery(w, r, "kind", "subject_type", "subject_id", "subject_version", "repository", "limit", "after")
	if !ok {
		return
	}
	kind := memory.FeedbackKind(query.Get("kind"))
	if query.Has("kind") && kind == "" {
		writeError(w, http.StatusBadRequest, wire.InvalidRequest("kind must not be empty", "kind", `must be "problem" or "suggestion"`))
		return
	}
	page, ok := pageQuery(w, query)
	if !ok || !nonEmpty(w, query, "subject_type", "subject_id", "repository") {
		return
	}
	f := memory.FeedbackFilter{Kind: kind, SubjectType: memory.SubjectType(query.Get("subject_type")),
		SubjectID: query.Get("subject_id"), Repository: query.Get("repository"), Page: page}
	if query.Has("subject_version") {
		n, err := strconv.Atoi(query.Get("subject_version"))
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, wire.InvalidRequest("subject_version must be a number", "subject_version", "must be at least 1"))
			return
		}
		f.SubjectVersion = n
	}
	reports, next, err := a.svc.ListFeedback(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewFeedbackList(reports, next))
}
