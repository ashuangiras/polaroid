package http_test

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	httptransport "github.com/ashuangiras/polaroid/internal/transport/http"
)

// testServer runs the real API over a real SQLite file, wired as polaroidd
// wires it for a loopback listener.
type testServer struct {
	*httptest.Server
	store *sqlite.Store
	logs  *syncBuffer
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	return newTestServerAt(t, filepath.Join(t.TempDir(), "polaroid.db"))
}

func newTestServerAt(t *testing.T, path string) *testServer {
	t.Helper()
	store, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	srv := httptest.NewServer(httptransport.RequireLoopbackHost(httptransport.NewHandler(memory.NewService(store), logger)))
	t.Cleanup(srv.Close)
	return &testServer{Server: srv, store: store, logs: logs}
}

type response struct {
	status int
	header http.Header
	body   []byte
}

// send performs one request. A nil header means a JSON body; otherwise the
// caller controls every header, including Content-Type and Host. It is safe to
// call from any goroutine.
func (s *testServer) send(method, path, body string, header http.Header) (response, error) {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, s.URL+path, rdr)
	if err != nil {
		return response{}, err
	}
	if header == nil && body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range header {
		if k == "Host" {
			req.Host = vs[0]
			continue
		}
		req.Header[k] = vs
	}
	resp, err := s.Client().Do(req)
	if err != nil {
		return response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, header: resp.Header, body: b}, err
}

func (s *testServer) call(t *testing.T, method, path, body string) response {
	t.Helper()
	resp, err := s.send(method, path, body, nil)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// expect fails the test unless resp has the given status and, for errors, the
// given error code. Every response must be JSON.
func expect(t *testing.T, resp response, status int, code string) {
	t.Helper()
	if resp.status != status {
		t.Fatalf("status = %d, want %d; body: %s", resp.status, status, resp.body)
	}
	if ct := resp.header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if code != "" {
		if got := errorOf(t, resp).Code; got != code {
			t.Fatalf("error code = %q, want %q; body: %s", got, code, resp.body)
		}
	}
}

// The structs below restate the documented wire contract. Decoding rejects
// unknown members, so an undocumented response field fails the tests.

type versionJSON struct {
	ProcedureID    string         `json:"procedure_id"`
	Version        int            `json:"version"`
	Philosophy     string         `json:"philosophy"`
	Method         string         `json:"method"`
	Contract       jsontext.Value `json:"contract"`
	Instructions   jsontext.Value `json:"instructions"`
	References     jsontext.Value `json:"references"`
	RevisionReason string         `json:"revision_reason"`
	CreatedAt      time.Time      `json:"created_at"`
}

type procedureJSON struct {
	ID            string    `json:"id"`
	CanonicalKey  string    `json:"canonical_key"`
	CreatedAt     time.Time `json:"created_at"`
	LatestVersion int       `json:"latest_version"`
}

type historyJSON struct {
	ID            string           `json:"id"`
	CanonicalKey  string           `json:"canonical_key"`
	CreatedAt     time.Time        `json:"created_at"`
	LatestVersion int              `json:"latest_version"`
	Versions      []jsontext.Value `json:"versions"`
}

type errorJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fields  []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"fields"`
	LatestVersion  int `json:"latest_version"`
	LatestRevision int `json:"latest_revision"`
	Cycle          []struct {
		ProcedureID string `json:"procedure_id"`
		Version     int    `json:"version"`
		Reference   string `json:"reference"`
	} `json:"cycle"`
}

type bindingJSON struct {
	ID             string    `json:"id"`
	Repository     string    `json:"repository"`
	Name           string    `json:"name"`
	ProcedureID    string    `json:"procedure_id"`
	CreatedAt      time.Time `json:"created_at"`
	LatestRevision int       `json:"latest_revision"`
}

type bindingRevisionJSON struct {
	BindingID      string         `json:"binding_id"`
	Revision       int            `json:"revision"`
	Inputs         jsontext.Value `json:"inputs"`
	VersionPolicy  jsontext.Value `json:"version_policy"`
	RevisionReason string         `json:"revision_reason"`
	CreatedAt      time.Time      `json:"created_at"`
}

type bindingHistoryJSON struct {
	ID             string           `json:"id"`
	Repository     string           `json:"repository"`
	Name           string           `json:"name"`
	ProcedureID    string           `json:"procedure_id"`
	CreatedAt      time.Time        `json:"created_at"`
	LatestRevision int              `json:"latest_revision"`
	Revisions      []jsontext.Value `json:"revisions"`
}

func decodeStrict[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v, json.RejectUnknownMembers(true)); err != nil {
		t.Fatalf("decode %T: %v; body: %s", v, err, b)
	}
	return v
}

func errorOf(t *testing.T, resp response) errorJSON {
	t.Helper()
	return decodeStrict[struct {
		Error errorJSON `json:"error"`
	}](t, resp.body).Error
}

const createBody = `{
  "canonical_key": "go.dependency.add",
  "version": {
    "philosophy": "A dependency is a long-term liability.",
    "method": "Justify, verify the license, pin, record.",
    "contract": { "inputs": { "module": "string" } },
    "instructions": { "steps": ["justify", "license", "pin", "record"] },
    "revision_reason": "Initial version."
  }
}`

func reviseBody(base int, step string) string {
	return fmt.Sprintf(`{
  "base_version": %d,
  "version": {
    "philosophy": "A dependency is a long-term liability.",
    "method": "Justify, verify the license, pin, record.",
    "contract": {"inputs": {"module": "string"}},
    "instructions": {"steps": [%q]},
    "revision_reason": "Corrected the steps."
  }
}`, base, step)
}

// create stores the example procedure and returns its history.
func (s *testServer) create(t *testing.T) historyJSON {
	t.Helper()
	resp := s.call(t, http.MethodPost, "/v1/procedures", createBody)
	expect(t, resp, http.StatusCreated, "")
	return decodeStrict[historyJSON](t, resp.body)
}

// syncBuffer is a bytes.Buffer safe for concurrent writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
