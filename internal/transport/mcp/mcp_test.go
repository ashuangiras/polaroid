package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// harness exercises v8 over real HTTP and SQLite.
type harness struct {
	url     string
	store   *sqlite.Store
	logs    *syncBuffer
	session *sdk.ClientSession
}

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

func (h *harness) call(t *testing.T, name, args string) *sdk.CallToolResult {
	t.Helper()
	res, err := h.session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: json.RawMessage(args)})
	if err != nil {
		t.Fatalf("%s: protocol error %v", name, err)
	}
	return res
}

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
	Code    string `json:"code"`
	Message string `json:"message"`
}

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

func textOf(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("want one content item, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", res.Content[0])
	}
	structured, err := json.Marshal(res.StructuredContent)
	var fromStructured, fromText any
	if err != nil || json.Unmarshal(structured, &fromStructured) != nil || json.Unmarshal([]byte(text.Text), &fromText) != nil ||
		!reflect.DeepEqual(fromStructured, fromText) {
		t.Fatalf("structured content %s differs from text %s", structured, text.Text)
	}
	return text.Text
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

func TestAgentV8ProtocolAndHTTPProtections(t *testing.T) {
	agent, _ := newKnowledgeHarnessV8(t)
	for _, version := range []string{"", "2025-11-25", "2026-07-28"} {
		session := agent.connect(t, version)
		if version != "" && session.InitializeResult().ProtocolVersion != version {
			t.Errorf("negotiated %q, want %q", session.InitializeResult().ProtocolVersion, version)
		}
		var names []string
		for tool, err := range session.Tools(context.Background(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, tool.Name)
		}
		slices.Sort(names)
		if want := []string{"advance", "attach_child", "discover", "feedback", "propose_advance", "propose_publish_capture", "propose_start", "read", "resolve", "start"}; !slices.Equal(names, want) {
			t.Errorf("protocol %q tools = %v, want %v", version, names, want)
		}
		for resource, err := range session.ResourceTemplates(context.Background(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			t.Errorf("unexpected resource template: %s", resource.URITemplate)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, agent.url, bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://untrusted.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin request status = %d", resp.StatusCode)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close cross-origin response: %v", err)
	}
	oversizedReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, agent.url, strings.NewReader(strings.Repeat(" ", wire.MaxRequestBytes+1)))
	if err != nil {
		t.Fatal(err)
	}
	oversizedReq.Header.Set("Content-Type", "application/json")
	oversizedReq.Header.Set("Accept", "application/json, text/event-stream")
	oversized, err := http.DefaultClient.Do(oversizedReq)
	if err != nil {
		t.Fatal(err)
	}
	if oversized.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(oversized.Body)
		t.Errorf("oversized request status = %d, body = %s", oversized.StatusCode, body)
	}
	if err := oversized.Body.Close(); err != nil {
		t.Errorf("close oversized response: %v", err)
	}
	agent.fail(t, "discover", `{"kind":"tools","unknown":true}`, "invalid_request")
	agent.fail(t, "discover", `{"kind":"tools","kind":"tools"}`, "invalid_request")
}
