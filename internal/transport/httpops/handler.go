// Package httpops serves the operator-controlled knowledge surface
// (ADR-0048): explicit status, publish, fetch, validate, activate, push,
// activations and reconciliation operations on Git-native living
// knowledge. It reaches the knowledge path only through
// internal/knowledgeops, never storage or the legacy API directly, and
// nothing here is reachable from agent mode: there is no scheduler, and
// every operation is triggered explicitly by an authorised operator.
//
// Binding to loopback is not treated as sufficient authorisation by
// itself, since other local processes can reach a loopback-bound port too:
// every request must also carry the configured token in the
// Polaroid-Operator-Token header, checked in constant time.
package httpops

import (
	"crypto/subtle"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"

	"github.com/ashuangiras/polaroid/internal/knowledgeops"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// MaxRequestBytes bounds every request body.
const MaxRequestBytes = wire.MaxRequestBytes

// TokenHeader carries the operator token every request must present.
const TokenHeader = "Polaroid-Operator-Token"

type api struct {
	op     *knowledgeops.Operator
	token  string
	logger *slog.Logger
	mux    *http.ServeMux
}

// NewHandler serves the operator knowledge API at /v1/knowledge/..., with
// every request authenticated against token. An empty token refuses every
// request: there is no way to disable authentication.
func NewHandler(op *knowledgeops.Operator, token string, logger *slog.Logger) http.Handler {
	a := &api{op: op, token: token, logger: logger, mux: http.NewServeMux()}
	a.mux.HandleFunc("GET /v1/knowledge/status", a.status)
	a.mux.HandleFunc("POST /v1/knowledge/publications", a.publish)
	a.mux.HandleFunc("POST /v1/knowledge/rollbacks", a.rollback)
	a.mux.HandleFunc("POST /v1/knowledge/sources/{name}/fetch", a.fetch)
	a.mux.HandleFunc("POST /v1/knowledge/sources/{name}/validation", a.validate)
	a.mux.HandleFunc("POST /v1/knowledge/sources/{name}/activation", a.activate)
	a.mux.HandleFunc("POST /v1/knowledge/sources/{name}/push", a.push)
	a.mux.HandleFunc("GET /v1/knowledge/activations", a.activations)
	a.mux.HandleFunc("POST /v1/knowledge/reconciliations/{finding_id}", a.reconcile)
	return a.authenticate(a)
}

func (a *api) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		given := r.Header.Get(TokenHeader)
		if a.token == "" || given == "" || subtle.ConstantTimeCompare([]byte(given), []byte(a.token)) != 1 {
			writeError(w, http.StatusUnauthorized, wire.ErrorDetail{Code: "unauthorized", Message: "the " + TokenHeader + " header is required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) { a.mux.ServeHTTP(w, r) }

func (a *api) status(w http.ResponseWriter, r *http.Request) {
	st, err := a.op.Status(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, st)
}

func (a *api) publish(w http.ResponseWriter, r *http.Request) {
	var body wire.KnowledgePublishRequest
	if !decode(w, r, &body) {
		return
	}
	res, err := a.op.Publish(r.Context(), body)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusCreated, res)
}

func (a *api) rollback(w http.ResponseWriter, r *http.Request) {
	var body wire.KnowledgeRollbackRequest
	if !decode(w, r, &body) {
		return
	}
	res, err := a.op.Rollback(r.Context(), body)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusCreated, res)
}

func (a *api) fetch(w http.ResponseWriter, r *http.Request) {
	var body wire.KnowledgeFetchRequest
	if !decodeOptional(w, r, &body) {
		return
	}
	res, err := a.op.Fetch(r.Context(), r.PathValue("name"), body.Credentials)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, res)
}

func (a *api) validate(w http.ResponseWriter, r *http.Request) {
	res, err := a.op.Validate(r.Context(), r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, res)
}

func (a *api) activate(w http.ResponseWriter, r *http.Request) {
	var body wire.KnowledgeDecision
	if !decode(w, r, &body) {
		return
	}
	res, err := a.op.Activate(r.Context(), r.PathValue("name"), body)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusCreated, res)
}

func (a *api) push(w http.ResponseWriter, r *http.Request) {
	var body wire.KnowledgePushRequest
	if !decodeOptional(w, r, &body) {
		return
	}
	res, err := a.op.Push(r.Context(), r.PathValue("name"), body.Credentials)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, res)
}

func (a *api) activations(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if q := r.URL.Query().Get("limit"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, wire.InvalidRequest("limit must be a number", "limit", "must be a positive number"))
			return
		}
		limit = n
	}
	res, err := a.op.Activations(r.Context(), limit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, res)
}

func (a *api) reconcile(w http.ResponseWriter, r *http.Request) {
	res, err := a.op.Reconcile(r.Context(), r.PathValue("finding_id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, res)
}

// decode reads a JSON request body into dst, rejecting unknown and
// duplicate members.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, wire.ErrorDetail{Code: "unsupported_media_type", Message: "Content-Type must be application/json"})
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, wire.ErrorDetail{
				Code: "request_too_large", Message: fmt.Sprintf("request body exceeds %d bytes", MaxRequestBytes),
			})
			return false
		}
		writeError(w, http.StatusBadRequest, wire.ErrorDetail{Code: "invalid_request", Message: "could not read request body"})
		return false
	}
	if err := wire.Decode(body, dst); err != nil {
		writeError(w, http.StatusBadRequest, wire.ErrorDetail{Code: "invalid_request", Message: err.Error()})
		return false
	}
	return true
}

// decodeOptional is decode, but an absent body leaves dst at its zero
// value: credentials are optional for a source that needs none.
func decodeOptional(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.ContentLength == 0 && r.Header.Get("Content-Type") == "" {
		return true
	}
	return decode(w, r, dst)
}

// fail maps a domain error to its response. Anything unrecognised is an
// internal failure: it is logged, and the client only learns that it
// happened.
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

func writeError(w http.ResponseWriter, status int, detail wire.ErrorDetail) {
	_ = writeJSON(w, status, wire.ErrorBody{Error: detail})
}

// writeJSON writes v as the response body. If v cannot be encoded it sends
// a generic internal error instead and returns the encoding error.
func writeJSON(w http.ResponseWriter, status int, v any) error {
	body, err := json.Marshal(v, json.Deterministic(true))
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
