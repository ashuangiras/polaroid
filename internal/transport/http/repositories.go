package http

import (
	"net/http"
	"net/url"

	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

func (a *api) registerRepository(w http.ResponseWriter, r *http.Request) {
	var body wire.NewRepository
	if !decode(w, r, &body) {
		return
	}
	repo, err := a.svc.RegisterRepository(r.Context(), body.Domain())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", repositoryPath(repo.ID))
	a.respond(w, r, http.StatusCreated, wire.NewRepositoryBody(repo))
}

func (a *api) addRepositoryAlias(w http.ResponseWriter, r *http.Request) {
	var body wire.NewAlias
	if !decode(w, r, &body) {
		return
	}
	repo, err := a.svc.AddRepositoryAlias(r.Context(), r.PathValue("id"), body.Domain())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusCreated, wire.NewRepositoryBody(repo))
}

func (a *api) getRepository(w http.ResponseWriter, r *http.Request) {
	repo, err := a.svc.Repository(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewRepositoryBody(repo))
}

func (a *api) getRepositoryByIdentifier(w http.ResponseWriter, r *http.Request) {
	repo, err := a.svc.RepositoryByIdentifier(r.Context(), r.PathValue("identifier"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewRepositoryBody(repo))
}

func (a *api) listRepositories(w http.ResponseWriter, r *http.Request) {
	query, ok := strictQuery(w, r, "limit", "after")
	if !ok {
		return
	}
	page, ok := pageQuery(w, query)
	if !ok {
		return
	}
	repos, next, err := a.svc.ListRepositories(r.Context(), page)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewRepositoryList(repos, next))
}

func repositoryPath(id string) string {
	return "/v1/repositories/" + url.PathEscape(id)
}
