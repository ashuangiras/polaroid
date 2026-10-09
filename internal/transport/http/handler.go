// Package http exposes the memory service as Polaroid's JSON HTTP API.
//
// It owns the wire contract documented in docs/architecture/http-api.md:
// request parsing, JSON shape checks, domain calls, and the mapping of
// results and errors to responses. Internal failures are logged and reported
// to clients without detail.
package http

import (
	"encoding/json/jsontext"
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
)

// MaxRequestBytes bounds every request body.
const MaxRequestBytes = 1 << 20

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
	a.mux.HandleFunc("GET /v1/procedures/{id}/versions/{version}", a.getVersion)
	a.mux.HandleFunc("POST /v1/bindings", a.createBinding)
	a.mux.HandleFunc("GET /v1/bindings", a.listBindings)
	a.mux.HandleFunc("GET /v1/bindings/{id}", a.getBinding)
	a.mux.HandleFunc("POST /v1/bindings/{id}/revisions", a.reviseBinding)
	a.mux.HandleFunc("GET /v1/bindings/{id}/revisions/{revision}", a.getBindingRevision)

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
	h, err := a.svc.CreateProcedure(r.Context(), memory.NewProcedure{
		CanonicalKey: body.CanonicalKey,
		Definition:   body.Version.definition(),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", procedurePath(h.Procedure.ID))
	a.respond(w, r, http.StatusCreated, newHistoryBody(h))
}

func (a *api) listProcedures(w http.ResponseWriter, r *http.Request) {
	procedures, err := a.svc.ListProcedures(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	body := procedureListBody{Procedures: make([]procedureBody, len(procedures))}
	for i, p := range procedures {
		body.Procedures[i] = newProcedureBody(p)
	}
	a.respond(w, r, http.StatusOK, body)
}

func (a *api) getProcedure(w http.ResponseWriter, r *http.Request) {
	h, err := a.svc.Procedure(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, newHistoryBody(h))
}

func (a *api) getProcedureByKey(w http.ResponseWriter, r *http.Request) {
	h, err := a.svc.ProcedureByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, newHistoryBody(h))
}

func (a *api) reviseProcedure(w http.ResponseWriter, r *http.Request) {
	var body reviseProcedureBody
	if !decode(w, r, &body) {
		return
	}
	v, err := a.svc.ReviseProcedure(r.Context(), r.PathValue("id"), memory.Revision{
		BaseVersion: body.BaseVersion,
		Definition:  body.Version.definition(),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", versionPath(v.ProcedureID, v.Number))
	a.respond(w, r, http.StatusCreated, newVersionBody(v))
}

func (a *api) getVersion(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errorDetail{
			Code:    "invalid_request",
			Message: "version must be a version number",
			Fields:  []fieldProblem{{Field: "version", Message: "must be a version number of at least 1"}},
		})
		return
	}
	v, err := a.svc.Version(r.Context(), r.PathValue("id"), number)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, newVersionBody(v))
}

// fail maps a domain error to its response. Anything unrecognised is an
// internal failure: it is logged, and the client only learns that it happened.
func (a *api) fail(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *memory.ValidationError
	var conflict *memory.VersionConflictError
	var revisionConflict *memory.RevisionConflictError
	switch {
	case errors.As(err, &invalid):
		detail := errorDetail{Code: "invalid_request", Message: invalid.Error()}
		for _, p := range invalid.Problems {
			detail.Fields = append(detail.Fields, fieldProblem(p))
		}
		writeError(w, http.StatusBadRequest, detail)
	case errors.As(err, &conflict):
		writeError(w, http.StatusConflict, errorDetail{Code: "version_conflict", Message: conflict.Error(), LatestVersion: conflict.LatestVersion})
	case errors.As(err, &revisionConflict):
		writeError(w, http.StatusConflict, errorDetail{Code: "revision_conflict", Message: revisionConflict.Error(), LatestRevision: revisionConflict.LatestRevision})
	case errors.Is(err, memory.ErrCanonicalKeyExists):
		writeError(w, http.StatusConflict, errorDetail{Code: "canonical_key_exists", Message: err.Error()})
	case errors.Is(err, memory.ErrBindingExists):
		writeError(w, http.StatusConflict, errorDetail{Code: "binding_exists", Message: err.Error()})
	case errors.Is(err, memory.ErrNotFound):
		writeError(w, http.StatusNotFound, errorDetail{Code: "not_found", Message: err.Error()})
	default:
		a.logger.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, errorDetail{Code: "internal", Message: "internal error"})
	}
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
	if err := json.Unmarshal(body, dst, json.RejectUnknownMembers(true)); err != nil {
		writeError(w, http.StatusBadRequest, errorDetail{Code: "invalid_request", Message: describeJSONError(err)})
		return false
	}
	return true
}

// describeJSONError explains a decoding failure in terms of the request
// document, without exposing Go type names.
func describeJSONError(err error) string {
	var syntax *jsontext.SyntacticError
	if errors.As(err, &syntax) {
		where := ""
		if syntax.JSONPointer != "" {
			where = fmt.Sprintf(" in %q", string(syntax.JSONPointer))
		}
		return fmt.Sprintf("malformed JSON at byte offset %d%s: %v", syntax.ByteOffset, where, syntax.Err)
	}
	var semantic *json.SemanticError
	if errors.As(err, &semantic) {
		where := "request body"
		if semantic.JSONPointer != "" {
			where = fmt.Sprintf("%q", string(semantic.JSONPointer))
		}
		switch {
		case errors.Is(semantic.Err, json.ErrUnknownName):
			return fmt.Sprintf("unknown field %s", where)
		case semantic.Err != nil:
			return fmt.Sprintf("invalid value for %s: %v", where, semantic.Err)
		default:
			return fmt.Sprintf("%s has the wrong JSON type (%s)", where, kindName(semantic.JSONKind))
		}
	}
	return "malformed JSON"
}

func kindName(k jsontext.Kind) string {
	switch k {
	case jsontext.KindBeginObject:
		return "object"
	case jsontext.KindBeginArray:
		return "array"
	case jsontext.KindString:
		return "string"
	case jsontext.KindNumber:
		return "number"
	case jsontext.KindTrue, jsontext.KindFalse:
		return "boolean"
	case jsontext.KindNull:
		return "null"
	default:
		return "unknown"
	}
}

func writeError(w http.ResponseWriter, status int, detail errorDetail) {
	_ = writeJSON(w, status, errorBody{Error: detail})
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
