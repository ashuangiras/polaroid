// Package mcp serves Git-native procedural knowledge over stateless streamable HTTP.
package mcp

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// ProtocolVersion is the preferred sessionless MCP revision.
const ProtocolVersion = "2026-07-28"

// HandshakeProtocolVersion is also supported without sessions.
const HandshakeProtocolVersion = "2025-11-25"

// NewAgentHandlerV8 serves the ten Git-native agent tools.
func NewAgentHandlerV8(agent AgentKnowledge, resolver AgentResolver, guide AgentGuideV7, drafter AgentFieldDrafterV8, logger *slog.Logger) http.Handler {
	return handler(newAgentServerV8(agent, resolver, guide, drafter, logger), logger)
}

func handler(server *sdk.Server, logger *slog.Logger) http.Handler {
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, Logger: logger, MaxRequestBodyBytes: wire.MaxRequestBytes,
	})
	return http.NewCrossOriginProtection().Handler(h)
}

func baseServer(instructions string, logger *slog.Logger) *sdk.Server {
	return sdk.NewServer(&sdk.Implementation{Name: "polaroid", Title: "Polaroid procedural memory", Version: "v1"}, &sdk.ServerOptions{
		Instructions:              instructions,
		Logger:                    logger,
		Capabilities:              &sdk.ServerCapabilities{},
		SupportedProtocolVersions: []string{ProtocolVersion, HandshakeProtocolVersion},
		SetCacheable:              func(_ context.Context, _ sdk.Request, c *sdk.Cacheable) { c.TTLMs = 0 },
	})
}

type tools struct {
	agent          AgentKnowledge
	resolver       AgentResolver
	guideV7        AgentGuideV7
	fieldDrafterV8 AgentFieldDrafterV8
	logger         *slog.Logger
}

var schemaOptions = &jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
	reflect.TypeFor[jsontext.Value](): {Type: "object"},
}}

// add retains strict HTTP-equivalent argument decoding and error mapping.
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
			body, err := json.Marshal(out, json.Deterministic(true))
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

type rawJSON []byte

func (r rawJSON) MarshalJSON() ([]byte, error) { return r, nil }
