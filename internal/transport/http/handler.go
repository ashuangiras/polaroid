// Package http exposes the memory service as Polaroid's JSON HTTP API.
//
// It owns the wire contract documented in docs/architecture/http-api.md:
// request parsing, JSON shape checks, domain calls, and the mapping of
// results and errors to responses. Internal failures are logged and reported
// to clients without detail.
package http

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// MaxRequestBytes bounds every request body.
const MaxRequestBytes = wire.MaxRequestBytes

type api struct {
	svc    *memory.Service
	logger *slog.Logger
	mux    *http.ServeMux
}

// NewHandler returns the Polaroid HTTP API backed by svc. It rejects unsafe
// cross-origin browser requests; wrap it with RequireLoopbackHost when the
// server listens on a loopback address.
func NewHandler(svc *memory.Service, logger *slog.Logger) http.Handler {
	a := &api{svc: svc, logger: logger, mux: http.NewServeMux()}
	a.mux.HandleFunc("GET /healthz", a.health)
	a.mux.HandleFunc("POST /v1/procedures", a.createProcedure)
	a.mux.HandleFunc("GET /v1/procedures", a.listProcedures)
	a.mux.HandleFunc("GET /v1/procedures/by-key/{key}", a.getProcedureByKey)
	a.mux.HandleFunc("GET /v1/procedures/{id}", a.getProcedure)
	a.mux.HandleFunc("POST /v1/procedures/{id}/versions", a.reviseProcedure)
	a.mux.HandleFunc("POST /v1/procedures/{id}/origin", a.recordOrigin)
	a.mux.HandleFunc("GET /v1/procedures/{id}/versions/{version}", a.getVersion)
	a.mux.HandleFunc("GET /v1/procedures/{id}/versions/{version}/graph", a.getGraph)
	a.mux.HandleFunc("GET /v1/procedures/{id}/versions/{version}/verifications", a.listVerifications)
	a.mux.HandleFunc("POST /v1/bindings", a.createBinding)
	a.mux.HandleFunc("GET /v1/bindings", a.listBindings)
	a.mux.HandleFunc("GET /v1/bindings/{id}", a.getBinding)
	a.mux.HandleFunc("POST /v1/bindings/{id}/revisions", a.reviseBinding)
	a.mux.HandleFunc("GET /v1/bindings/{id}/revisions/{revision}", a.getBindingRevision)
	a.mux.HandleFunc("GET /v1/bindings/{id}/resolution", a.resolveBinding)
	a.mux.HandleFunc("POST /v1/executions", a.recordExecution)
	a.mux.HandleFunc("GET /v1/executions", a.listExecutions)
	a.mux.HandleFunc("GET /v1/executions/{id}", a.getExecution)
	a.mux.HandleFunc("GET /v1/executions/{id}/verification", a.getVerification)
	a.mux.HandleFunc("POST /v1/feedback", a.reportFeedback)
	a.mux.HandleFunc("GET /v1/feedback", a.listFeedback)
	a.mux.HandleFunc("GET /v1/feedback/{id}", a.getFeedback)
	a.mux.HandleFunc("POST /v1/repositories", a.registerRepository)
	a.mux.HandleFunc("GET /v1/repositories", a.listRepositories)
	a.mux.HandleFunc("GET /v1/repositories/by-identifier/{identifier...}", a.getRepositoryByIdentifier)
	a.mux.HandleFunc("GET /v1/repositories/{id}", a.getRepository)
	a.mux.HandleFunc("POST /v1/repositories/{id}/aliases", a.addRepositoryAlias)

	csrf := http.NewCrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusForbidden, errorDetail{Code: "forbidden", Message: "cross-origin browser requests are not allowed"})
	}))
	return csrf.Handler(a)
}

// ServeHTTP routes r, answering unknown paths and methods with JSON errors
// rather than ServeMux's plain-text defaults.
func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, pattern := a.mux.Handler(r); pattern != "" {
		a.mux.ServeHTTP(w, r)
		return
	}
	var allowed []string
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		probe := r.Clone(r.Context())
		probe.Method = method
		if _, pattern := a.mux.Handler(probe); pattern != "" {
			allowed = append(allowed, method)
		}
	}
	if len(allowed) == 0 {
		writeError(w, http.StatusNotFound, errorDetail{Code: "not_found", Message: "no such endpoint"})
		return
	}
	if slices.Contains(allowed, http.MethodGet) {
		allowed = append(allowed, http.MethodHead)
	}
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	writeError(w, http.StatusMethodNotAllowed, errorDetail{
		Code:    "method_not_allowed",
		Message: fmt.Sprintf("method %s is not allowed for this endpoint", r.Method),
	})
}

func (a *api) health(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.Health(r.Context()); err != nil {
		a.logger.ErrorContext(r.Context(), "health check failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, errorDetail{Code: "unavailable", Message: "storage is unavailable"})
		return
	}
	a.respond(w, r, http.StatusOK, healthBody{Status: "ok"})
}

func (a *api) createProcedure(w http.ResponseWriter, r *http.Request) {
	var body createProcedureBody
	if !decode(w, r, &body) {
		return
	}
	var origin *memory.Origin
	if body.Origin != nil {
		o := body.Origin.Domain()
		origin = &o
	}
	h, err := a.svc.CreateProcedure(r.Context(), memory.NewProcedure{
		CanonicalKey: body.CanonicalKey,
		Origin:       origin,
		Definition:   body.Version.Domain(),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", procedurePath(h.Procedure.ID))
	a.respond(w, r, http.StatusCreated, wire.NewHistory(h))
}

func (a *api) recordOrigin(w http.ResponseWriter, r *http.Request) {
	var body wire.NewOrigin
	if !decode(w, r, &body) {
		return
	}
	h, err := a.svc.RecordOrigin(r.Context(), r.PathValue("id"), body.Domain())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusCreated, wire.NewHistory(h))
}

func (a *api) listProcedures(w http.ResponseWriter, r *http.Request) {
	query, ok := strictQuery(w, r, "repository", "scope", "q", "limit", "after", "snapshot")
	if !ok {
		return
	}
	page, ok := snapshotPageQuery(w, query)
	if !ok {
		return
	}
	if !nonEmpty(w, query, "repository", "scope", "q") {
		return
	}
	procedures, next, err := a.svc.ListProcedures(r.Context(), memory.ProcedureFilter{
		Repository: query.Get("repository"), Scope: query.Get("scope"), Query: query.Get("q"), Page: page,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewProcedureList(procedures, next))
}

func (a *api) getProcedure(w http.ResponseWriter, r *http.Request) {
	h, err := a.svc.Procedure(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewHistory(h))
}

func (a *api) getProcedureByKey(w http.ResponseWriter, r *http.Request) {
	h, err := a.svc.ProcedureByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewHistory(h))
}

func (a *api) reviseProcedure(w http.ResponseWriter, r *http.Request) {
	var body reviseProcedureBody
	if !decode(w, r, &body) {
		return
	}
	v, err := a.svc.ReviseProcedure(r.Context(), r.PathValue("id"), memory.Revision{
		BaseVersion: body.BaseVersion,
		Definition:  body.Version.Domain(),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", versionPath(v.ProcedureID, v.Number))
	a.respond(w, r, http.StatusCreated, wire.NewVersion(v))
}

func (a *api) getVersion(w http.ResponseWriter, r *http.Request) {
	number, ok := versionNumber(w, r)
	if !ok {
		return
	}
	v, err := a.svc.Version(r.Context(), r.PathValue("id"), number)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewVersion(v))
}

func (a *api) getGraph(w http.ResponseWriter, r *http.Request) {
	number, ok := versionNumber(w, r)
	if !ok {
		return
	}
	query, ok := strictQuery(w, r, "repository", "environment", "commit", "inputs", "decisions")
	if !ok {
		return
	}
	if query.Has("repository") != query.Has("environment") {
		missing, given := "environment", "repository"
		if !query.Has("repository") {
			missing, given = given, missing
		}
		writeError(w, http.StatusBadRequest, errorDetail{
			Code:    "invalid_request",
			Message: "repository and environment must be given together",
			Fields:  []fieldProblem{{Field: missing, Message: "is required with " + given}},
		})
		return
	}
	rc := memory.ResolutionContext{Repository: query.Get("repository"), Environment: query.Get("environment"), Target: targetQuery(query)}
	g, err := a.svc.CompositionGraph(r.Context(), r.PathValue("id"), number, rc)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, wire.NewGraphNode(g))
}

// versionNumber parses the {version} path segment, writing the error
// response if it is not a number.
func versionNumber(w http.ResponseWriter, r *http.Request) (int, bool) {
	number, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errorDetail{
			Code:    "invalid_request",
			Message: "version must be a version number",
			Fields:  []fieldProblem{{Field: "version", Message: "must be a version number of at least 1"}},
		})
		return 0, false
	}
	return number, true
}

// fail maps a domain error to its response. Anything unrecognised is an
// internal failure: it is logged, and the client only learns that it happened.
func (a *api) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, detail, ok := wire.Classify(err)
	if !ok {
		a.logger.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	}
	writeError(w, status, detail)
}

func (a *api) respond(w http.ResponseWriter, r *http.Request, status int, v any) {
	if err := writeJSON(w, status, v); err != nil {
		a.logger.ErrorContext(r.Context(), "encode response", "method", r.Method, "path", r.URL.Path, "error", err)
	}
}

// decode reads a JSON request body into dst, rejecting unknown and duplicate
// members. On failure it writes the error response and returns false.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, errorDetail{Code: "unsupported_media_type", Message: "Content-Type must be application/json"})
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, errorDetail{
				Code:    "request_too_large",
				Message: fmt.Sprintf("request body exceeds %d bytes", MaxRequestBytes),
			})
			return false
		}
		writeError(w, http.StatusBadRequest, errorDetail{Code: "invalid_request", Message: "could not read request body"})
		return false
	}
	if err := wire.Decode(body, dst); err != nil {
		writeError(w, http.StatusBadRequest, errorDetail{Code: "invalid_request", Message: err.Error()})
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, detail errorDetail) {
	_ = writeJSON(w, status, wire.ErrorBody{Error: detail})
}

// writeJSON writes v as the response body. If v cannot be encoded it sends a
// generic internal error instead and returns the encoding error.
func writeJSON(w http.ResponseWriter, status int, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":{"code":"internal","message":"internal error"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n')) //nolint:gosec // G705: the body is JSON, served as application/json with nosniff.
	return err
}

func procedurePath(id string) string {
	return "/v1/procedures/" + url.PathEscape(id)
}

func versionPath(procedureID string, number int) string {
	return procedurePath(procedureID) + "/versions/" + strconv.Itoa(number)
}

func bindingPath(id string) string {
	return "/v1/bindings/" + url.PathEscape(id)
}

func revisionPath(bindingID string, number int) string {
	return bindingPath(bindingID) + "/revisions/" + strconv.Itoa(number)
}
