// Package mcpserver is the ant tools' MCP server: the process an agent's
// runner starts as anthill mcp --ant <token> and talks to over stdio
// (spec ant-tools, "Tools are delivered as a local stdio MCP server";
// design D5). It holds no tool logic: it lists the tools the daemon
// gives for the token, and relays every call to the daemon.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool is one tool as the daemon describes it.
type Tool struct {
	Name        string
	Description string
	// InputSchema is the JSON Schema of the tool's arguments.
	InputSchema json.RawMessage
}

// Result is the daemon's answer to one call.
type Result struct {
	Text string
	// Data is the structured result, or empty.
	Data json.RawMessage
	// IsError is set when the call was refused or failed.
	IsError bool
	// Notices are messages for the agent attached to this result.
	Notices []string
}

// Relay reaches the daemon for one token.
type Relay interface {
	// Tools lists the token's tools.
	Tools(ctx context.Context) ([]Tool, error)
	// Call executes one call. An error means the daemon could not be
	// reached, not that the call was refused.
	Call(ctx context.Context, tool string, args json.RawMessage) (Result, error)
}

// NoticePrefix starts the text of each notice in a tool result.
const NoticePrefix = "Anthill notice: "

// New returns an MCP server with the relay's tools. It fails when the
// daemon cannot be reached or does not know the token, so that the run
// does not start without its tools (spec ant-tools, "MCP server fails to
// start").
func New(ctx context.Context, relay Relay, version string) (*mcp.Server, error) {
	tools, err := relay.Tools(ctx)
	if err != nil {
		return nil, err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "anthill", Version: version}, nil)
	for _, t := range tools {
		if !isObjectSchema(t.InputSchema) {
			return nil, fmt.Errorf("tool %s: the input schema is not an object schema", t.Name)
		}
		server.AddTool(&mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}, handler(relay, t.Name))
	}
	return server, nil
}

// Serve builds the server with New and runs it on transport until the
// client disconnects or ctx is cancelled.
func Serve(ctx context.Context, relay Relay, version string, transport mcp.Transport) error {
	server, err := New(ctx, relay, version)
	if err != nil {
		return err
	}
	return server.Run(ctx, transport)
}

// handler relays the calls of one tool. A refusal, and a daemon that
// cannot be reached, are tool results with IsError set, so the agent sees
// the reason instead of a protocol error.
func handler(relay Relay, name string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := relay.Call(ctx, name, req.Params.Arguments)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "Anthill could not be reached: " + err.Error()}},
				IsError: true,
			}, nil
		}
		out := &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: res.Text}},
			IsError: res.IsError,
		}
		if !res.IsError && bytes.HasPrefix(bytes.TrimSpace(res.Data), []byte("{")) {
			out.StructuredContent = res.Data
		}
		for _, n := range res.Notices {
			out.Content = append(out.Content, &mcp.TextContent{Text: NoticePrefix + n})
		}
		return out, nil
	}
}

func isObjectSchema(raw json.RawMessage) bool {
	var s struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &s) == nil && s.Type == "object"
}
