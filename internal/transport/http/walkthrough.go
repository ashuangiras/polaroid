package http

import (
	"net/http"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// Operator visibility into guided-procedure walkthroughs (ADR-0056):
// list_walkthroughs and get_walkthrough, on the legacy HTTP surface, not
// /mcp/agent/v3; see wire.Walkthrough's doc comment for why their status
// is coarser than start/advance's own.

func (a *api) listWalkthroughs(w http.ResponseWriter, r *http.Request) {
	query, ok := strictQuery(w, r, "repository", "goal", "status", "limit", "after")
	if !ok {
		return
	}
	page, ok := pageQuery(w, query)
	if !ok || !nonEmpty(w, query, "repository", "goal", "status") {
		return
	}
	f := memory.WalkthroughFilter{Repository: query.Get("repository"), Goal: query.Get("goal"), Status: query.Get("status"), Page: page}
	states, next, err := a.svc.ListWalkthroughs(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewWalkthroughList(states, next))
}

func (a *api) getWalkthrough(w http.ResponseWriter, r *http.Request) {
	st, err := a.svc.GetWalkthrough(r.Context(), r.PathValue("token"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewWalkthrough(st))
}
