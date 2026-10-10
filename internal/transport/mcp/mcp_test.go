package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"io"
	"log/slog"
	"net/http"
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
	readTools = []string{"get_binding", "get_binding_revision", "get_execution", "get_feedback", "get_graph", "get_procedure", "get_repository", "get_verification", "get_version",
		"list_bindings", "list_executions", "list_feedback", "list_procedures", "list_repositories", "list_verifications", "resolve_binding"}
	writeTools = []string{"add_repository_alias", "create_binding", "create_procedure", "record_execution", "record_procedure_origin", "register_repository",
		"report_feedback", "revise_binding", "revise_procedure"}
)

func TestToolsAndResourcesAreAdvertised(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if v := h.session.InitializeResult().ProtocolVersion; v != mcptransport.ProtocolVersion {
		t.Fatalf("negotiated protocol %q, want %q", v, mcptransport.ProtocolVersion)
	}
	if instr := h.session.InitializeResult().Instructions; !strings.Contains(instr, "record_execution") || !strings.Contains(instr, "report_feedback") {
		t.Fatalf("instructions do not describe the loop: %q", instr)
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

// Feedback 01a122e7-abd8: an agent learned the reporter format only from a rejected call.
func TestReportFeedbackDescribesTheReporterFormat(t *testing.T) {
	h := newHarness(t)
	var desc string
	for tool, err := range h.session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name == "report_feedback" {
			desc = tool.Description
		}
	}
	for _, want := range []string{"reporter", "canonical-key form", "copilot.vscode"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("report_feedback's description does not mention %q: %q", want, desc)
		}
	}
	h.ok(t, "report_feedback", `{"kind":"suggestion","summary":"s","details":"d","reporter":"copilot.vscode"}`)
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
	// Selection evidence names its commit; target verification is per commit.
	if r := h.ok(t, "resolve_binding", `{"binding_id":"`+binding+`","environment":"ci.linux","commit":"`+commit+`","inputs":{}}`); !strings.Contains(r,
		`"selection_evidence":{"execution_id":"`+parentRun+`","repository":"github.com/o/r","commit":"`+commit+`","environment":{"name":"ci.linux"}},"target_verification":{`) ||
		!strings.Contains(r, `"verified":true,"latest_execution_id":"`+parentRun+`"`) {
		t.Fatalf("resolve_binding at the recorded commit: %s", r)
	}
	other := strings.Repeat("b", 40)
	if r := h.ok(t, "resolve_binding", `{"binding_id":"`+binding+`","environment":"ci.linux","commit":"`+other+`","inputs":{}}`); !strings.Contains(r, `"verified_by":"`+parentRun+`"`) ||
		!strings.Contains(r, `"verified":false,"execution_ids":[]`) || strings.Contains(r, `"verified":true`) {
		t.Fatalf("resolve_binding at an unseen commit: %s", r)
	}
	// The parent's evidence fixes the leaf at version 1, although version 2 is the latest.
	g := h.ok(t, "get_graph", `{"procedure_id":"`+parent+`","version":1,"repository":"github.com/o/r","environment":"ci.linux"}`)
	if !strings.Contains(g, `"selected_by":"evidence","inputs":{},"node":{"procedure_id":"`+leaf+`","canonical_key":"demo.leaf","version":1,"scope":"unspecified","verified_by":"`+child+`"`) {
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

// TestChildInputsThroughTools: record_execution refuses a child that ran
// with inputs other than its reference maps (ADR-0024), and get_verification
// reports a stored mismatch without verifying the parent.
func TestChildInputsThroughTools(t *testing.T) {
	h := newHarness(t)
	leaf := field(t, h.ok(t, "create_procedure", `{"canonical_key":"demo.leaf","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}`), "id")
	parent := field(t, h.ok(t, "create_procedure", `{"canonical_key":"demo.parent","philosophy":"p","method":"m","contract":{},"instructions":{},`+
		`"references":[{"name":"leaf","procedure_id":"`+leaf+`","version_policy":{"contextual":{}},"inputs":{"mode":{"value":"strict"}}}],"revision_reason":"r"}`), "id")
	wrong := field(t, h.ok(t, "record_execution", run(leaf, 1, "succeeded", "")), "id")

	e := h.fail(t, "record_execution", run(parent, 1, "succeeded", `[{"reference":"leaf","execution_id":"`+wrong+`"}]`), "invalid_request")
	if got := e.fields(); !slices.Equal(got, []string{"children[0].execution_id"}) || !strings.Contains(e.Fields[0].Message, `maps the parent's inputs to {"mode":"strict"}`) {
		t.Fatalf("mismatched child: %+v", e)
	}

	// A link stored before the rule reads back and never verifies.
	historical := memory.Execution{ID: "historical", ExecutionRecord: memory.ExecutionRecord{
		ProcedureID: parent, Version: 1, Repository: "github.com/o/r", Commit: commit,
		Environment: memory.Environment{Name: "ci.linux", Attributes: jsontext.Value(`{}`)},
		Inputs:      jsontext.Value(`{}`), Outcome: memory.OutcomeSucceeded, Evidence: jsontext.Value(`{"log":"ok"}`),
		Children: []memory.ChildExecution{{Reference: "leaf", ExecutionID: wrong}},
	}}
	if err := h.store.CreateExecution(context.Background(), &historical); err != nil {
		t.Fatal(err)
	}
	h.ok(t, "get_execution", `{"id":"historical"}`)
	if v := h.ok(t, "get_verification", `{"execution_id":"historical"}`); !strings.Contains(v,
		`"verified":false,"problems":[{"code":"child_inputs_mismatch","reference":"leaf","execution_id":"`+wrong+`"}]`) {
		t.Fatalf("historical verification: %s", v)
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
	if e := h.fail(t, "resolve_binding", `{"binding_id":"x","environment":"ci.linux","commit":"0123456"}`, "invalid_request"); !slices.Equal(e.fields(), []string{"commit", "inputs"}) {
		t.Fatalf("invalid target: fields %v", e.fields())
	}
	if e := h.fail(t, "get_graph", `{"procedure_id":"x","version":1,"commit":"`+commit+`","inputs":{}}`, "invalid_request"); !slices.Equal(e.fields(), []string{"repository", "environment"}) {
		t.Fatalf("target without a context: fields %v", e.fields())
	}
}

func TestFeedbackThroughTools(t *testing.T) {
	h := newHarness(t)
	p := h.ok(t, "create_procedure", `{"canonical_key":"demo.fb","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}`)

	reported := h.ok(t, "report_feedback", `{"kind":"problem","summary":"get_graph needs both repository and environment",`+
		`"details":"I passed only repository.\nThe error said so.","reporter":"copilot.vscode","context":{"tool":"get_graph","b":1,"a":2}}`)
	if !strings.Contains(reported, `"context":{"tool":"get_graph","b":1,"a":2}`) {
		t.Fatalf("context member order not kept: %s", reported)
	}
	id := field(t, reported, "id")
	if got := h.ok(t, "get_feedback", `{"id":"`+id+`"}`); got != reported {
		t.Fatalf("get_feedback differs from report_feedback:\n%s\n%s", got, reported)
	}
	suggestion := h.ok(t, "report_feedback", `{"kind":"suggestion","summary":"s","details":"d","reporter":"copilot.vscode"}`)
	if !strings.Contains(suggestion, `"context":{}`) {
		t.Fatalf("absent context: %s", suggestion)
	}
	if got := h.ok(t, "list_feedback", `{}`); got != `{"feedback":[`+reported+`,`+suggestion+`]}` {
		t.Fatalf("list_feedback = %s", got)
	}
	if got := h.ok(t, "list_feedback", `{"kind":"suggestion"}`); got != `{"feedback":[`+suggestion+`]}` {
		t.Fatalf("list_feedback suggestion = %s", got)
	}

	if e := h.fail(t, "report_feedback", `{"kind":"bug","summary":"a\nb","details":" ","reporter":"Copilot","context":[]}`, "invalid_request"); !slices.Equal(e.fields(), []string{"kind", "summary", "details", "reporter", "context"}) {
		t.Fatalf("invalid report: fields %v", e.fields())
	}
	h.fail(t, "report_feedback", `{"kind":"problem","summary":"s","details":"d","reporter":"r","status":"open"}`, "invalid_request")
	if e := h.fail(t, "list_feedback", `{"kind":"bug"}`, "invalid_request"); !slices.Equal(e.fields(), []string{"kind"}) {
		t.Fatalf("invalid kind: %+v", e)
	}
	h.fail(t, "get_feedback", `{"id":"missing"}`, "not_found")

	if got := h.ok(t, "get_procedure", `{"canonical_key":"demo.fb"}`); got != p {
		t.Fatalf("reporting feedback changed the procedure:\n%s\n%s", got, p)
	}
}

// The ADR-0019 to ADR-0021 tools have the HTTP API's names, rules and errors.
func TestRepositoriesScopesAndPagesThroughTools(t *testing.T) {
	h := newHarness(t)
	repo := h.ok(t, "register_repository", `{"identifier":"github.com/o/a","name":"A"}`)
	a := field(t, repo, "id")
	h.fail(t, "register_repository", `{"identifier":"github.com/o/a","name":"Again"}`, "repository_identifier_exists")
	aliased := h.ok(t, "add_repository_alias", `{"repository_id":"`+a+`","identifier":"mirror.example/o/a","reason":"A's mirror."}`)
	if got := h.ok(t, "get_repository", `{"identifier":"mirror.example/o/a"}`); got != aliased {
		t.Fatalf("get_repository by alias = %s, want %s", got, aliased)
	}
	if e := h.fail(t, "get_repository", `{"id":"`+a+`","identifier":"github.com/o/a"}`, "invalid_request"); !slices.Equal(e.fields(), []string{"identifier"}) {
		t.Fatalf("id and identifier: %+v", e)
	}
	if got := h.ok(t, "list_repositories", `{"limit":1}`); got != `{"repositories":[`+aliased+`]}` {
		t.Fatalf("list_repositories = %s", got)
	}

	shared := field(t, h.ok(t, "create_procedure", `{"canonical_key":"go.build","philosophy":"p","method":"m","goal":"Build a Go module.",`+
		`"applicability":{"shared":{}},"contract":{},"instructions":{},"revision_reason":"r"}`), "id")
	local := h.ok(t, "create_procedure", `{"canonical_key":"a.release","origin":{"repository_id":"`+a+`","reason":"Written for A."},`+
		`"philosophy":"p","method":"m","applicability":{"repository":"`+a+`"},"contract":{},"instructions":{},`+
		`"references":[{"name":"build","procedure_id":"`+shared+`","version_policy":{"contextual":{}},"inputs":{}}],"revision_reason":"r"}`)
	if !strings.Contains(local, `"scope":"local","applicability":{"repository":"`+a+`"},"origin":{"repository_id":"`+a+`","reason":"Written for A."`) {
		t.Fatalf("create_procedure local = %s", local)
	}
	if e := h.fail(t, "create_procedure", `{"canonical_key":"x.y","philosophy":"p","method":"m","applicability":{"shared":{}},"contract":{},"instructions":{},`+
		`"references":[{"name":"r","procedure_id":"`+field(t, local, "id")+`","version_policy":{"pin":1},"inputs":{}}],"revision_reason":"r"}`, "invalid_request"); !slices.Equal(e.fields(), []string{"references[0].procedure_id"}) {
		t.Fatalf("shared parent of a local child: %+v", e)
	}
	h.fail(t, "record_procedure_origin", `{"procedure_id":"`+field(t, local, "id")+`","repository_id":"`+a+`","reason":"Again."}`, "origin_exists")
	if e := h.fail(t, "record_procedure_origin", `{"procedure_id":"`+shared+`","repository_id":"missing","reason":"r"}`, "invalid_request"); !slices.Equal(e.fields(), []string{"repository_id"}) {
		t.Fatalf("origin in an unregistered repository: %+v", e)
	}
	if e := h.fail(t, "create_binding", `{"repository":"github.com/o/b","name":"release","procedure_id":"`+field(t, local, "id")+`",`+
		`"inputs":{},"version_policy":{"contextual":{}},"revision_reason":"r"}`, "invalid_request"); !slices.Equal(e.fields(), []string{"procedure_id"}) {
		t.Fatalf("binding a local procedure elsewhere: %+v", e)
	}

	inB := h.ok(t, "list_procedures", `{"repository":"github.com/o/b"}`)
	if !strings.Contains(inB, `"canonical_key":"go.build"`) || strings.Contains(inB, `"canonical_key":"a.release"`) {
		t.Fatalf("list_procedures in B = %s", inB)
	}
	first := h.ok(t, "list_procedures", `{"limit":1}`)
	next := field(t, first, "next")
	if next == "" || !strings.Contains(first, `"canonical_key":"a.release"`) {
		t.Fatalf("first page = %s", first)
	}
	if second := h.ok(t, "list_procedures", `{"limit":1,"after":"`+next+`"}`); !strings.Contains(second, `"canonical_key":"go.build"`) || strings.Contains(second, `"next"`) {
		t.Fatalf("second page = %s", second)
	}
	if e := h.fail(t, "list_procedures", `{"after":"`+next+`"}`, "invalid_request"); !slices.Equal(e.fields(), []string{"after"}) {
		t.Fatalf("after without limit: %+v", e)
	}

	report := h.ok(t, "report_feedback", `{"kind":"problem","summary":"s","details":"d","reporter":"copilot.vscode",`+
		`"subject":{"type":"procedure","procedure_id":"`+shared+`","version":1},"repository":"mirror.example/o/a"}`)
	if got := h.ok(t, "list_feedback", `{"subject_type":"procedure","subject_id":"`+shared+`","subject_version":1,"repository":"github.com/o/a"}`); got != `{"feedback":[`+report+`]}` {
		t.Fatalf("list_feedback by subject and repository = %s", got)
	}
	if e := h.fail(t, "report_feedback", `{"kind":"problem","summary":"s","details":"d","reporter":"r","subject":{"type":"procedure","procedure_id":"missing"}}`, "invalid_request"); !slices.Equal(e.fields(), []string{"subject.procedure_id"}) {
		t.Fatalf("unknown subject: %+v", e)
	}
	if got := h.ok(t, "list_executions", `{}`); got != `{"executions":[]}` {
		t.Fatalf("list_executions without filters = %s", got)
	}
}

// TestIdentitySnapshotsAndLabelsThroughTools checks the ADR-0022 and
// ADR-0023 fields and arguments through MCP, as HTTP serves them.
func TestIdentitySnapshotsAndLabelsThroughTools(t *testing.T) {
	h := newHarness(t)
	leaf := field(t, h.ok(t, "create_procedure", `{"canonical_key":"demo.leaf","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}`), "id")
	h.ok(t, "create_procedure", `{"canonical_key":"demo.other","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}`)
	runID := field(t, h.ok(t, "record_execution", run(leaf, 1, "succeeded", "")), "id")
	repo := field(t, h.ok(t, "register_repository", `{"identifier":"github.com/o/a","name":"A"}`), "id")
	h.ok(t, "add_repository_alias", `{"repository_id":"`+repo+`","identifier":"github.com/o/r","reason":"Renamed."}`)

	if e := h.ok(t, "get_execution", `{"id":"`+runID+`"}`); !strings.Contains(e, `"repository":"github.com/o/r","repository_id":"`+repo+`"`) {
		t.Fatalf("get_execution = %s", e)
	}
	g := h.ok(t, "get_graph", `{"procedure_id":"`+leaf+`","version":1,"repository":"github.com/o/a","environment":"ci.linux","commit":"`+commit+`","inputs":{}}`)
	if !strings.Contains(g, `"scope":"unspecified"`) || !strings.Contains(g, `"repository":"github.com/o/a","repository_id":"`+repo+`"`) ||
		!strings.Contains(g, `"verified":true,"latest_execution_id":"`+runID+`"`) {
		t.Fatalf("get_graph under the canonical identifier = %s", g)
	}

	first := h.ok(t, "list_procedures", `{"limit":1,"snapshot":true}`)
	next := field(t, first, "next")
	h.ok(t, "create_procedure", `{"canonical_key":"demo.a","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}`)
	if second := h.ok(t, "list_procedures", `{"limit":1,"snapshot":true,"after":"`+next+`"}`); !strings.Contains(second, `"canonical_key":"demo.other"`) || strings.Contains(second, `"next"`) {
		t.Fatalf("second snapshot page = %s", second)
	}
	if e := h.fail(t, "list_procedures", `{"limit":1,"after":"`+next+`"}`, "invalid_request"); !slices.Equal(e.fields(), []string{"after"}) {
		t.Fatalf("a snapshot cursor in a live traversal: %+v", e)
	}
	if e := h.fail(t, "list_bindings", `{"repository":"github.com/o/a","snapshot":true}`, "invalid_request"); !slices.Equal(e.fields(), []string{"snapshot"}) {
		t.Fatalf("snapshot without limit: %+v", e)
	}
}

func TestCacheableResultsAreImmediatelyStale(t *testing.T) {
	h := newHarness(t)
	p := field(t, h.ok(t, "create_procedure", `{"canonical_key":"demo.ttl","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}`), "id")
	uri := "polaroid://procedures/" + p
	for _, c := range []struct{ method, name, params string }{
		{"server/discover", "", `{}`},
		{"tools/list", "", `{}`},
		{"resources/templates/list", "", `{}`},
		{"resources/read", uri, `{"uri":"` + uri + `"}`},
	} {
		var params map[string]any
		if err := jsonv2.Unmarshal([]byte(c.params), &params); err != nil {
			t.Fatal(err)
		}
		params["_meta"] = map[string]any{"io.modelcontextprotocol/protocolVersion": mcptransport.ProtocolVersion, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
		_, body := h.raw(t, mcptransport.ProtocolVersion, c.method, c.name, params)
		var out struct {
			Result map[string]jsontext.Value `json:"result"`
		}
		if err := jsonv2.Unmarshal(body, &out); err != nil {
			t.Fatalf("%s: %v; body %s", c.method, err, body)
		}
		if ttl, ok := out.Result["ttlMs"]; !ok || string(ttl) != "0" {
			t.Errorf("%s: ttlMs = %s (present %v), want 0", c.method, ttl, ok)
		}
	}
}

// raw sends one JSON-RPC request exactly as given; version "" sends no
// Mcp-Protocol-Version header.
func (h *harness) raw(t *testing.T, version, method, name string, params map[string]any) (http.Header, []byte) {
	t.Helper()
	body, err := jsonv2.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if version != "" {
		req.Header.Set("Mcp-Protocol-Version", version)
	}
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Header.Set("X-Test-Status", strconv.Itoa(resp.StatusCode))
	return resp.Header, out
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

// A 2025-11-25 client uses the initialize handshake, but still gets no
// session and exactly the same tools and results (ADR-0016).
func TestHandshakeProtocolWorksWithoutSessions(t *testing.T) {
	h := newHarness(t)
	old := h.connect(t, mcptransport.HandshakeProtocolVersion)
	if v := old.InitializeResult().ProtocolVersion; v != mcptransport.HandshakeProtocolVersion {
		t.Fatalf("negotiated %q, want %q", v, mcptransport.HandshakeProtocolVersion)
	}
	if old.ID() != "" {
		t.Fatalf("session ID %q, want none", old.ID())
	}
	var tools int
	for _, err := range old.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		tools++
	}
	if tools != len(readTools)+len(writeTools) {
		t.Fatalf("%d tools at %s, want %d", tools, mcptransport.HandshakeProtocolVersion, len(readTools)+len(writeTools))
	}

	call := func(s *sdk.ClientSession, name, args string) string {
		t.Helper()
		res, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: json.RawMessage(args)})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return textOf(t, res)
	}
	created := call(old, "create_procedure", `{"canonical_key":"demo.old","philosophy":"p","method":"m","contract":{"z":1,"a":2},"instructions":{},"revision_reason":"r"}`)
	id := field(t, created, "id")
	if got := h.ok(t, "get_procedure", `{"id":"`+id+`"}`); got != created {
		t.Fatalf("a 2026-07-28 client reads something else:\n%s\n%s", got, created)
	}
	fb := call(old, "report_feedback", `{"kind":"suggestion","summary":"s","details":"d","reporter":"old.client"}`)
	if got := h.ok(t, "get_feedback", `{"id":"`+field(t, fb, "id")+`"}`); got != fb {
		t.Fatalf("feedback differs across revisions:\n%s\n%s", got, fb)
	}
	if got := call(old, "get_procedure", `{"canonical_key":"no.such"}`); !strings.Contains(got, `"code":"not_found"`) {
		t.Fatalf("error at %s: %s", mcptransport.HandshakeProtocolVersion, got)
	}

	header, body := h.raw(t, "", "initialize", "", map[string]any{
		"protocolVersion": mcptransport.HandshakeProtocolVersion, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "raw", "version": "1"},
	})
	if sid := header.Get("Mcp-Session-Id"); sid != "" {
		t.Fatalf("initialize issued session %q; body %s", sid, body)
	}
	if !strings.Contains(string(body), `"protocolVersion":"`+mcptransport.HandshakeProtocolVersion+`"`) {
		t.Fatalf("initialize = %s", body)
	}
}

// No client is ever served at a revision older than 2025-11-25. A request
// naming one is refused; an older initialize is answered with 2025-11-25,
// which the client accepts or disconnects from (MCP lifecycle).
func TestOlderProtocolsAreNeverServed(t *testing.T) {
	h := newHarness(t)
	for _, v := range []string{"2025-06-18", "2024-11-05"} {
		_, body := h.raw(t, "", "initialize", "", map[string]any{
			"protocolVersion": v, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "raw", "version": "1"},
		})
		if !strings.Contains(string(body), `"protocolVersion":"`+mcptransport.HandshakeProtocolVersion+`"`) {
			t.Errorf("initialize at %s = %s, want the counter-offer %s", v, body, mcptransport.HandshakeProtocolVersion)
		}
		header, body := h.raw(t, v, "tools/list", "", map[string]any{})
		if status := header.Get("X-Test-Status"); status != "400" || !strings.Contains(string(body), "Unsupported protocol version") {
			t.Errorf("tools/list at %s: status %s, body %s; want 400 Unsupported protocol version", v, status, body)
		}
		if s := h.connect(t, v); s.InitializeResult().ProtocolVersion != mcptransport.HandshakeProtocolVersion {
			t.Errorf("a client asking for %s was served at %q", v, s.InitializeResult().ProtocolVersion)
		}
	}
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
