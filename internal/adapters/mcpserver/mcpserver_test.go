package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/matteorosani/anthill/internal/adapters/mcpserver"
)

// fakeRelay answers like the daemon would, and records the calls.
type fakeRelay struct {
	tools    []mcpserver.Tool
	toolsErr error
	result   mcpserver.Result
	callErr  error
	calls    []string
	args     []json.RawMessage
}

func (r *fakeRelay) Tools(context.Context) ([]mcpserver.Tool, error) {
	return r.tools, r.toolsErr
}

func (r *fakeRelay) Call(_ context.Context, tool string, args json.RawMessage) (mcpserver.Result, error) {
	r.calls = append(r.calls, tool)
	r.args = append(r.args, args)
	return r.result, r.callErr
}

var schema = json.RawMessage(`{"type":"object","properties":{"minutes":{"type":"integer"}},"required":["minutes"]}`)

// connect starts the server for relay on an in-memory transport and
// returns a client session.
func connect(t *testing.T, relay mcpserver.Relay) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	serverT, clientT := mcp.NewInMemoryTransports()
	server, err := mcpserver.New(ctx, relay, "test")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("server: %v", err)
		}
	})
	return session
}

func TestListsTheRelayedTools(t *testing.T) {
	relay := &fakeRelay{tools: []mcpserver.Tool{
		{Name: "time_remaining", Description: "Tell the time left.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "request_extension", Description: "Ask for more time.", InputSchema: schema},
	}}
	res, err := connect(t, relay).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 2 || res.Tools[0].Name != "request_extension" && res.Tools[1].Name != "request_extension" {
		t.Fatalf("tools = %+v", res.Tools)
	}
	for _, tool := range res.Tools {
		if tool.Name == "request_extension" {
			if tool.Description != "Ask for more time." {
				t.Errorf("description = %q", tool.Description)
			}
			got, _ := json.Marshal(tool.InputSchema)
			var want, have any
			_ = json.Unmarshal(schema, &want)
			_ = json.Unmarshal(got, &have)
			if string(mustJSON(want)) != string(mustJSON(have)) {
				t.Errorf("schema = %s, want %s", got, schema)
			}
		}
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestRelaysCalls(t *testing.T) {
	relay := &fakeRelay{
		tools:  []mcpserver.Tool{{Name: "request_extension", InputSchema: schema}},
		result: mcpserver.Result{Text: `{"status":"approved"}`, Data: json.RawMessage(`{"status":"approved"}`), Notices: []string{"12 minutes remaining"}},
	}
	session := connect(t, relay)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_extension", Arguments: map[string]any{"minutes": 10}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || len(res.Content) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; text != `{"status":"approved"}` {
		t.Errorf("text = %q", text)
	}
	if text := res.Content[1].(*mcp.TextContent).Text; text != "Anthill notice: 12 minutes remaining" {
		t.Errorf("notice = %q", text)
	}
	if string(relay.args[0]) != `{"minutes":10}` {
		t.Errorf("relayed arguments = %s", relay.args[0])
	}

	// A refusal is a tool error the agent can read.
	relay.result = mcpserver.Result{Text: "waiting for the extension decision", IsError: true}
	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_extension", Arguments: map[string]any{"minutes": 10}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || res.Content[0].(*mcp.TextContent).Text != "waiting for the extension decision" || res.StructuredContent != nil {
		t.Errorf("refusal = %+v", res)
	}

	// So is a daemon that went away.
	relay.callErr = errors.New("daemon not running")
	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_extension", Arguments: map[string]any{"minutes": 10}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || res.Content[0].(*mcp.TextContent).Text != "Anthill could not be reached: daemon not running" {
		t.Errorf("unreachable = %+v", res)
	}
}

// Spec ant-tools, "MCP server fails to start": without its tools the
// server does not start.
func TestServerFailsWithoutTools(t *testing.T) {
	_, err := mcpserver.New(context.Background(), &fakeRelay{toolsErr: errors.New("unknown or revoked tool token")}, "test")
	if err == nil || err.Error() != "unknown or revoked tool token" {
		t.Errorf("New = %v, want the relay's error", err)
	}
	_, err = mcpserver.New(context.Background(), &fakeRelay{tools: []mcpserver.Tool{{Name: "x", InputSchema: json.RawMessage(`{"type":"string"}`)}}}, "test")
	if err == nil {
		t.Error("New accepted a non-object input schema")
	}
}
