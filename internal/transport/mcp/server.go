// Package mcp exposes the memory service as an MCP server over stateless
// streamable HTTP (ADR-0014). Tools take flat arguments named after the
// record fields and return the same record shapes as the HTTP API; the
// contract is docs/architecture/mcp.md.
package mcp

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// ProtocolVersion is the preferred MCP revision: the sessionless one.
const ProtocolVersion = "2026-07-28"

// HandshakeProtocolVersion is the older revision also served, through the
// initialize handshake but still without sessions (ADR-0016).
const HandshakeProtocolVersion = "2025-11-25"

const instructions = `Polaroid stores procedures: versioned, shared instructions for tasks. Typical loop:
1. Find what applies to your task before writing anything: list_bindings for your repository's bindings (a binding that covers the task is resolved in step 2), and discover_procedures with the task in your own words and your repository. Discovery is lexical: it ranks latest versions by shared words and explains each score, which is a ranking value, not a confidence or a verification. Fetch plausible candidates with get_procedure or get_version and read their contracts; whether one fits is your judgment. Procedure records may describe this in more detail: follow one that discovery finds for adopting or reusing procedures. list_procedures with repository lists everything that applies there. A procedure's scope (shared, local to one registered repository, or unspecified: no declaration) is its latest version's; q searches canonical keys and goals. get_repository finds a registered repository by any of its identifiers, and evidence recorded under any of them counts for all.
2. Resolve what to run: resolve_binding for a repository's binding, or get_graph with repository and environment. Every edge says how its version was selected (pin, evidence or latest), and every node names that version's own scope; read the selected version's contract before reuse. selection_evidence names the execution, and the commit, that made a version a candidate; it does not verify your checkout. Pass commit and inputs to see target_verification for the exact commit and inputs you will run.
3. Follow the selected versions' instructions with your own tools. If target_verification is false or absent, the work is not verified there until you run it and record it. A reference with a condition applies only when the condition holds for your task: decide each one yourself (Polaroid never evaluates conditions), run the applicable ones, and pass your decisions to get_graph or resolve_binding to see target_verification.
4. Record each run with record_execution, children first, then the parent with "children" linking them. A child must have run with the inputs its reference maps from the parent's. List a decision for every conditional reference: applicable with its child, or not applicable with a concrete rationale and no child. Required references cannot be skipped.
5. Improve a procedure with revise_procedure, passing the version you read as base_version; a version_conflict means re-read and try again. Before creating or revising one, check the proposal with suggest_duplicates (exclude_procedure_id when revising): key_collision means the key is taken, and suggestions are overlapping procedures to read and reuse, bind or revise if they fit; the decision is yours. A new procedure declares its applicability (shared, or local to a repository) and origin.
6. When Polaroid itself gets in your way (a confusing error, a missing capability, a tool that misbehaved) or you see how it could serve you better, say so with report_feedback, naming its subject (the service, a repository, procedure version, binding or execution).
Lists take limit and return next to continue after, with the same other arguments; list_procedures and list_bindings also take snapshot to page a fixed snapshot. Tool errors carry an error object with a stable code (invalid_request, not_found, version_conflict, ...).`

// NewHandler returns the MCP endpoint over svc. It applies the same
// cross-origin protection as the HTTP API; wrap it with a loopback-host
// check when listening on loopback.
func NewHandler(svc *memory.Service, logger *slog.Logger) http.Handler {
	server := newServer(svc, logger)
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{
		Stateless:           true,
		JSONResponse:        true,
		Logger:              logger,
		MaxRequestBodyBytes: wire.MaxRequestBytes,
	})
	return http.NewCrossOriginProtection().Handler(h)
}

func newServer(svc *memory.Service, logger *slog.Logger) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "polaroid", Title: "Polaroid procedural memory", Version: "v1"}, &sdk.ServerOptions{
		Instructions:              instructions,
		Logger:                    logger,
		Capabilities:              &sdk.ServerCapabilities{},
		SupportedProtocolVersions: []string{ProtocolVersion, HandshakeProtocolVersion},
		// Tools and records change with upgrades and writes, so clients must
		// never serve a cached discover, list or read result (SEP-2549).
		SetCacheable: func(_ context.Context, _ sdk.Request, c *sdk.Cacheable) { c.TTLMs = 0 },
	})
	t := &tools{svc: svc, logger: logger}
	t.register(s)
	t.registerResources(s)
	return s
}

type tools struct {
	svc    *memory.Service
	logger *slog.Logger
}

// Free-form JSON objects are advertised as objects; everything else is
// inferred from the argument types.
var schemaOptions = &jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
	reflect.TypeFor[jsontext.Value](): {Type: "object"},
}}

// add registers a tool whose arguments decode strictly into In, exactly as
// HTTP request bodies do, so free-form objects keep their member order.
func add[In any](s *sdk.Server, t *tools, name, description string, readOnly bool, run func(context.Context, In) (any, error)) {
	schema, err := jsonschema.For[In](schemaOptions)
	if err != nil {
		panic(fmt.Sprintf("mcp: schema for %s: %v", name, err))
	}
	annotations := &sdk.ToolAnnotations{ReadOnlyHint: readOnly, OpenWorldHint: new(false)}
	if !readOnly {
		annotations.DestructiveHint = new(false)
	}
	s.AddTool(&sdk.Tool{Name: name, Description: description, InputSchema: schema, Annotations: annotations},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			args := []byte(req.Params.Arguments)
			if len(args) == 0 {
				args = []byte("{}")
			}
			var in In
			if err := wire.Decode(args, &in); err != nil {
				return t.failure(wire.ErrorDetail{Code: "invalid_request", Message: err.Error()}), nil
			}
			out, err := run(ctx, in)
			if err != nil {
				_, detail, ok := wire.Classify(err)
				if !ok {
					t.logger.ErrorContext(ctx, "mcp tool failed", "tool", name, "error", err)
				}
				return t.failure(detail), nil
			}
			body, err := json.Marshal(out)
			if err != nil {
				t.logger.ErrorContext(ctx, "mcp encode result", "tool", name, "error", err)
				return t.failure(wire.Internal), nil
			}
			return &sdk.CallToolResult{
				Content:           []sdk.Content{&sdk.TextContent{Text: string(body)}},
				StructuredContent: rawJSON(body),
			}, nil
		})
}

// failure is a tool error carrying the HTTP API's error body.
func (t *tools) failure(detail wire.ErrorDetail) *sdk.CallToolResult {
	body, err := json.Marshal(wire.ErrorBody{Error: detail})
	if err != nil {
		body = []byte(`{"error":{"code":"internal","message":"internal error"}}`)
	}
	return &sdk.CallToolResult{
		IsError:           true,
		Content:           []sdk.Content{&sdk.TextContent{Text: string(body)}},
		StructuredContent: rawJSON(body),
	}
}

// rawJSON embeds already-encoded JSON verbatim in the SDK's result.
type rawJSON []byte

func (r rawJSON) MarshalJSON() ([]byte, error) { return r, nil }

// required returns an invalid_request error naming field.
func required(field string) error {
	return &memory.ValidationError{Problems: []memory.FieldProblem{{Field: field, Message: "is required"}}}
}

const resourceType = "application/json"

func (t *tools) registerResources(s *sdk.Server) {
	s.AddResourceTemplate(&sdk.ResourceTemplate{
		Name: "procedure", Title: "Procedure history", MIMEType: resourceType,
		URITemplate: "polaroid://procedures/{id}",
		Description: "A procedure with all of its versions, as get_procedure returns it.",
	}, t.readResource)
	s.AddResourceTemplate(&sdk.ResourceTemplate{
		Name: "procedure-version", Title: "Procedure version", MIMEType: resourceType,
		URITemplate: "polaroid://procedures/{id}/versions/{version}",
		Description: "One version of a procedure, as get_version returns it.",
	}, t.readResource)
	s.AddResourceTemplate(&sdk.ResourceTemplate{
		Name: "binding", Title: "Binding history", MIMEType: resourceType,
		URITemplate: "polaroid://bindings/{id}",
		Description: "A repository binding with all of its revisions, as get_binding returns it.",
	}, t.readResource)
}

func (t *tools) readResource(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	uri := req.Params.URI
	var body any
	var err error
	switch parts := strings.Split(strings.TrimPrefix(uri, "polaroid://"), "/"); {
	case len(parts) == 2 && parts[0] == "procedures":
		var h memory.History
		h, err = t.svc.Procedure(ctx, parts[1])
		body = wire.NewHistory(h)
	case len(parts) == 4 && parts[0] == "procedures" && parts[2] == "versions":
		n, convErr := strconv.Atoi(parts[3])
		if convErr != nil {
			return nil, sdk.ResourceNotFoundError(uri)
		}
		var v memory.Version
		v, err = t.svc.Version(ctx, parts[1], n)
		body = wire.NewVersion(v)
	case len(parts) == 2 && parts[0] == "bindings":
		var h memory.BindingHistory
		h, err = t.svc.Binding(ctx, parts[1])
		body = wire.NewBindingHistory(h)
	default:
		return nil, sdk.ResourceNotFoundError(uri)
	}
	if err != nil {
		if _, _, ok := wire.Classify(err); ok {
			return nil, sdk.ResourceNotFoundError(uri)
		}
		t.logger.ErrorContext(ctx, "mcp resource failed", "uri", uri, "error", err)
		return nil, fmt.Errorf("internal error")
	}
	text, err := json.Marshal(body)
	if err != nil {
		t.logger.ErrorContext(ctx, "mcp encode resource", "uri", uri, "error", err)
		return nil, fmt.Errorf("internal error")
	}
	return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: uri, MIMEType: resourceType, Text: string(text)}}}, nil
}
