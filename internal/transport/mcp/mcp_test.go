package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	mcptransport "github.com/ashuangiras/polaroid/internal/transport/mcp"
)

// harness runs the real MCP handler over a real SQLite file and HTTP, and
// talks to it with the SDK's client.
type harness struct {
	url     string
	store   *sqlite.Store
	logs    *syncBuffer
	session *sdk.ClientSession
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "polaroid.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	logs := &syncBuffer{}
	srv := httptest.NewServer(mcptransport.NewHandler(memory.NewService(store), slog.New(slog.NewTextHandler(logs, nil))))
	t.Cleanup(srv.Close)
	h := &harness{url: srv.URL, store: store, logs: logs}
	h.session = h.connect(t, "")
	return h
}

// connect opens a client session; version "" means the SDK's latest.
func (h *harness) connect(t *testing.T, version string) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "polaroid-test", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(),
		&sdk.StreamableClientTransport{Endpoint: h.url, DisableStandaloneSSE: true, MaxRetries: -1},
		&sdk.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// call invokes a tool with raw JSON arguments, sent exactly as written.
func (h *harness) call(t *testing.T, name, args string) *sdk.CallToolResult {
	t.Helper()
	res, err := h.session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: json.RawMessage(args)})
	if err != nil {
		t.Fatalf("%s: protocol error %v", name, err)
	}
	return res
}

// ok calls a tool that must succeed and returns its JSON text.
func (h *harness) ok(t *testing.T, name, args string) string {
	t.Helper()
	res := h.call(t, name, args)
	text := textOf(t, res)
	if res.IsError {
		t.Fatalf("%s(%s) failed: %s", name, args, text)
	}
	return text
}

type errorJSON struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	LatestVersion int    `json:"latest_version"`
	Fields        []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"fields"`
}

// fail calls a tool that must fail with code and returns the error body.
func (h *harness) fail(t *testing.T, name, args, code string) errorJSON {
	t.Helper()
	res := h.call(t, name, args)
	text := textOf(t, res)
	var body struct {
		Error errorJSON `json:"error"`
	}
	if !res.IsError || jsonv2.Unmarshal([]byte(text), &body) != nil || body.Error.Code != code {
		t.Fatalf("%s(%s) = isError %v, %s; want error %s", name, args, res.IsError, text, code)
	}
	return body.Error
}

func (e errorJSON) fields() []string {
	var out []string
	for _, f := range e.Fields {
		out = append(out, f.Field)
	}
	return out
}

func textOf(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("want one content item, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", res.Content[0])
	}
	// The client decodes structured content into maps, so compare values; the
	// text keeps the server's member order.
	structured, err := json.Marshal(res.StructuredContent)
	var fromStructured, fromText any
	if err != nil || json.Unmarshal(structured, &fromStructured) != nil || json.Unmarshal([]byte(text.Text), &fromText) != nil ||
		!reflect.DeepEqual(fromStructured, fromText) {
		t.Fatalf("structured content %s differs from text %s", structured, text.Text)
	}
	return text.Text
}

// field extracts one top-level string member of a JSON text.
func field(t *testing.T, text, name string) string {
	t.Helper()
	var m map[string]any
	if err := jsonv2.Unmarshal([]byte(text), &m); err != nil {
		t.Fatal(err)
	}
	s, _ := m[name].(string)
	return s
}

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

var (
	readTools  = []string{"get_binding", "get_binding_revision", "get_execution", "get_graph", "get_procedure", "get_verification", "get_version", "list_bindings", "list_executions", "list_procedures", "list_verifications", "resolve_binding"}
	writeTools = []string{"create_binding", "create_procedure", "record_execution", "revise_binding", "revise_procedure"}
)

func TestToolsAndResourcesAreAdvertised(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if v := h.session.InitializeResult().ProtocolVersion; v != mcptransport.ProtocolVersion {
		t.Fatalf("negotiated protocol %q, want %q", v, mcptransport.ProtocolVersion)
	}
	if !strings.Contains(h.session.InitializeResult().Instructions, "record_execution") {
		t.Fatalf("instructions do not describe the loop: %q", h.session.InitializeResult().Instructions)
	}

	var read, write []string
	for tool, err := range h.session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil || !strings.Contains(string(schema), `"type":"object"`) {
			t.Errorf("%s: input schema %s is not an object schema", tool.Name, schema)
		}
		if tool.Annotations != nil && tool.Annotations.ReadOnlyHint {
			read = append(read, tool.Name)
		} else {
			write = append(write, tool.Name)
		}
	}
	slices.Sort(read)
	slices.Sort(write)
	if !slices.Equal(read, readTools) || !slices.Equal(write, writeTools) {
		t.Fatalf("read-only tools %v, other tools %v", read, write)
	}

	var templates []string
	for rt, err := range h.session.ResourceTemplates(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		templates = append(templates, rt.URITemplate)
	}
	slices.Sort(templates)
	if want := []string{"polaroid://bindings/{id}", "polaroid://procedures/{id}", "polaroid://procedures/{id}/versions/{version}"}; !slices.Equal(templates, want) {
		t.Fatalf("resource templates %v, want %v", templates, want)
	}
}

const commit = "0123456789abcdef0123456789abcdef01234567"

func run(procedureID string, version int, outcome, children string) string {
	args := `{"procedure_id":"` + procedureID + `","version":` + strconv.Itoa(version) +
		`,"repository":"github.com/o/r","commit":"` + commit + `","environment":{"name":"ci.linux","attributes":{}},` +
		`"inputs":{},"outcome":"` + outcome + `","evidence":{"log":"ok"}`
	if children != "" {
		args += `,"children":` + children
	}
	return args + "}"
}

func TestAgentWorkflowThroughTools(t *testing.T) {
	h := newHarness(t)

	created := h.ok(t, "create_procedure", `{"canonical_key":"demo.leaf","philosophy":"p","method":"m",`+
		`"contract":{"z":1, "a":{"y":2,"b":3}},"instructions":{"steps":["one"]},"revision_reason":"Initial version."}`)
	if !strings.Contains(created, `"contract":{"z":1,"a":{"y":2,"b":3}}`) {
		t.Fatalf("contract member order not kept: %s", created)
	}
	leaf := field(t, created, "id")
	if got := h.ok(t, "get_procedure", `{"canonical_key":"demo.leaf"}`); got != created {
		t.Fatalf("get_procedure differs from create_procedure:\n%s\n%s", got, created)
	}
	if got := h.ok(t, "get_procedure", `{"id":"`+leaf+`"}`); got != created {
		t.Fatalf("get_procedure by id differs:\n%s", got)
	}

	revise := `{"procedure_id":"` + leaf + `","base_version":1,"philosophy":"p","method":"m2","contract":{},"instructions":{},"revision_reason":"r"}`
	if v := h.ok(t, "revise_procedure", revise); !strings.Contains(v, `"version":2`) {
		t.Fatalf("revise: %s", v)
	}
	if e := h.fail(t, "revise_procedure", revise, "version_conflict"); e.LatestVersion != 2 {
		t.Fatalf("stale base: %+v", e)
	}
	h.ok(t, "get_version", `{"procedure_id":"`+leaf+`","version":2}`)

	parent := field(t, h.ok(t, "create_procedure", `{"canonical_key":"demo.parent","philosophy":"p","method":"m","contract":{},"instructions":{},`+
		`"references":[{"name":"leaf","procedure_id":"`+leaf+`","version_policy":{"contextual":{}},"inputs":{}}],"revision_reason":"r"}`), "id")
	binding := field(t, h.ok(t, "create_binding", `{"repository":"github.com/o/r","name":"follow","procedure_id":"`+parent+`",`+
		`"inputs":{},"version_policy":{"contextual":{}},"revision_reason":"r"}`), "id")
	h.ok(t, "revise_binding", `{"binding_id":"`+binding+`","base_revision":1,"inputs":{"x":1},"version_policy":{"contextual":{}},"revision_reason":"r2"}`)
	h.ok(t, "get_binding_revision", `{"binding_id":"`+binding+`","revision":2}`)
	if list := h.ok(t, "list_bindings", `{"repository":"github.com/o/r"}`); !strings.Contains(list, binding) {
		t.Fatalf("list_bindings: %s", list)
	}

	child := field(t, h.ok(t, "record_execution", run(leaf, 1, "succeeded", "")), "id")
	parentRun := field(t, h.ok(t, "record_execution", run(parent, 1, "succeeded", `[{"reference":"leaf","execution_id":"`+child+`"}]`)), "id")
	if v := h.ok(t, "get_verification", `{"execution_id":"`+parentRun+`"}`); !strings.Contains(v, `"verified":true`) {
		t.Fatalf("verification: %s", v)
	}
	if r := h.ok(t, "resolve_binding", `{"binding_id":"`+binding+`","environment":"ci.linux"}`); !strings.Contains(r, `"selected_by":"evidence"`) ||
		!strings.Contains(r, `"verified_by":"`+parentRun+`"`) {
		t.Fatalf("resolve_binding: %s", r)
	}
	// The parent's evidence fixes the leaf at version 1, although version 2 is the latest.
	g := h.ok(t, "get_graph", `{"procedure_id":"`+parent+`","version":1,"repository":"github.com/o/r","environment":"ci.linux"}`)
	if !strings.Contains(g, `"selected_by":"evidence","inputs":{},"node":{"procedure_id":"`+leaf+`","canonical_key":"demo.leaf","version":1,"verified_by":"`+child+`"`) {
		t.Fatalf("resolved graph: %s", g)
	}
	h.ok(t, "get_execution", `{"id":"`+parentRun+`"}`)
	h.ok(t, "list_executions", `{"procedure_id":"`+parent+`"}`)
	h.ok(t, "list_verifications", `{"procedure_id":"`+parent+`","version":1}`)
	h.ok(t, "get_binding", `{"id":"`+binding+`"}`)
	if list := h.ok(t, "list_procedures", `{}`); !strings.Contains(list, "demo.parent") {
		t.Fatalf("list_procedures: %s", list)
	}
}

func TestToolArgumentsAreStrict(t *testing.T) {
	h := newHarness(t)
	valid := `"philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"`

	if e := h.fail(t, "create_procedure", `{"canonical_key":"a.b",`+valid+`,"aliases":[]}`, "invalid_request"); !strings.Contains(e.Message, "unknown field") {
		t.Fatalf("unknown member: %+v", e)
	}
	h.fail(t, "create_procedure", `{"canonical_key":"a.b","canonical_key":"c.d",`+valid+`}`, "invalid_request")
	h.fail(t, "get_version", `{"procedure_id":"x","version":"1"}`, "invalid_request")
	if e := h.fail(t, "create_procedure", `{"canonical_key":"a.b","method":"m","contract":{},"instructions":{},"revision_reason":"r"}`, "invalid_request"); !slices.Equal(e.fields(), []string{"philosophy"}) {
		t.Fatalf("missing philosophy: fields %v", e.fields())
	}
	if e := h.fail(t, "create_binding", `{"repository":"Not/Canonical","name":"n","procedure_id":"p","inputs":{},"version_policy":{},"revision_reason":"r"}`, "invalid_request"); !slices.Equal(e.fields(), []string{"repository", "version_policy"}) {
		t.Fatalf("invalid binding: fields %v", e.fields())
	}
	if e := h.fail(t, "get_procedure", `{"id":"x","canonical_key":"y"}`, "invalid_request"); !slices.Equal(e.fields(), []string{"canonical_key"}) {
		t.Fatalf("both selectors: %+v", e)
	}
	if e := h.fail(t, "get_procedure", `{}`, "invalid_request"); !slices.Equal(e.fields(), []string{"id"}) {
		t.Fatalf("no selector: %+v", e)
	}
	h.fail(t, "get_procedure", `{"canonical_key":"no.such"}`, "not_found")
	h.fail(t, "get_graph", `{"procedure_id":"x","version":1,"repository":"github.com/o/r"}`, "invalid_request")
}

func TestResourcesMatchTools(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p := field(t, h.ok(t, "create_procedure", `{"canonical_key":"demo.res","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}`), "id")
	b := field(t, h.ok(t, "create_binding", `{"repository":"scratch","name":"n","procedure_id":"`+p+`","inputs":{},"version_policy":{"pin":1},"revision_reason":"r"}`), "id")

	for uri, want := range map[string]string{
		"polaroid://procedures/" + p:                 h.ok(t, "get_procedure", `{"id":"`+p+`"}`),
		"polaroid://procedures/" + p + "/versions/1": h.ok(t, "get_version", `{"procedure_id":"`+p+`","version":1}`),
		"polaroid://bindings/" + b:                   h.ok(t, "get_binding", `{"id":"`+b+`"}`),
	} {
		res, err := h.session.ReadResource(ctx, &sdk.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
		if len(res.Contents) != 1 || res.Contents[0].Text != want || res.Contents[0].MIMEType != "application/json" {
			t.Fatalf("%s = %+v\nwant %s", uri, res.Contents, want)
		}
	}
	for _, uri := range []string{"polaroid://procedures/missing", "polaroid://procedures/" + p + "/versions/9", "polaroid://bindings/missing"} {
		if _, err := h.session.ReadResource(ctx, &sdk.ReadResourceParams{URI: uri}); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("%s: err = %v, want resource not found", uri, err)
		}
	}
}

func TestOlderProtocolsAreRefused(t *testing.T) {
	h := newHarness(t)
	client := sdk.NewClient(&sdk.Implementation{Name: "old-client", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(),
		&sdk.StreamableClientTransport{Endpoint: h.url, DisableStandaloneSSE: true, MaxRetries: -1},
		&sdk.ClientSessionOptions{ProtocolVersion: "2025-06-18"})
	if err == nil {
		defer func() { _ = session.Close() }()
		_, err = session.CallTool(context.Background(), &sdk.CallToolParams{Name: "list_procedures", Arguments: json.RawMessage(`{}`)})
		if err == nil {
			t.Fatalf("a 2025-06-18 client could call tools (negotiated %q)", session.InitializeResult().ProtocolVersion)
		}
	}
	t.Logf("refused: %v", err)
}

func TestInternalErrorsAreNotExposed(t *testing.T) {
	h := newHarness(t)
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	e := h.fail(t, "list_procedures", `{}`, "internal")
	if e.Message != "internal error" {
		t.Fatalf("message = %q", e.Message)
	}
	if !strings.Contains(h.logs.String(), "database is closed") {
		t.Fatalf("internal error was not logged:\n%s", h.logs)
	}
}
