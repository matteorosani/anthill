package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/control"
	"github.com/matteorosani/anthill/internal/app"
)

// The control methods. Each console command calls one of them, and
// anthill mcp calls the ant tools methods.
const (
	methodStatus = "status"
	// methodAntTools lists the tools of a tool token.
	methodAntTools = "ant.tools"
	// methodAntCall executes one ant-tool call.
	methodAntCall = "ant.call"
)

// codeUnknownToken is the JSON-RPC error code of ant.tools for a token
// the daemon does not know, in the range JSON-RPC leaves to servers.
const codeUnknownToken = -32001

// statusResult is the result of the status method.
type statusResult struct {
	Version   string    `json:"version"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	// Uptime is in whole seconds.
	Uptime int64 `json:"uptime"`
}

// antToolsParams are the params of ant.tools.
type antToolsParams struct {
	Token string `json:"token"`
}

// antTool is one tool in the result of ant.tools.
type antTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// antToolsResult is the result of ant.tools.
type antToolsResult struct {
	Tools []antTool `json:"tools"`
}

// antCallParams are the params of ant.call.
type antCallParams struct {
	Token     string          `json:"token"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// antCallResult is the result of ant.call. A refused call is a result
// with is_error set, not a JSON-RPC error, so the agent gets the reason.
type antCallResult struct {
	Text    string          `json:"text"`
	Data    json.RawMessage `json:"data,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	Notices []string        `json:"notices,omitempty"`
}

// registerAPI registers the handler of every control method on srv.
func registerAPI(srv *control.Server, health *app.Health, tools *app.AntTools) {
	srv.Handle(methodStatus, func(context.Context, json.RawMessage, control.Stream) (any, error) {
		s := health.Status()
		return statusResult{
			Version:   s.Version,
			PID:       s.PID,
			StartedAt: s.StartedAt,
			Uptime:    int64(s.Uptime / time.Second),
		}, nil
	})
	srv.Handle(methodAntTools, func(_ context.Context, raw json.RawMessage, _ control.Stream) (any, error) {
		var p antToolsParams
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		defs, err := tools.Tools(p.Token)
		if errors.Is(err, app.ErrUnknownToken) {
			return nil, &control.Error{Code: codeUnknownToken, Message: err.Error()}
		}
		if err != nil {
			return nil, err
		}
		out := antToolsResult{Tools: []antTool{}}
		for _, d := range defs {
			out.Tools = append(out.Tools, antTool{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema})
		}
		return out, nil
	})
	srv.Handle(methodAntCall, func(ctx context.Context, raw json.RawMessage, _ control.Stream) (any, error) {
		var p antCallParams
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		res := tools.Call(ctx, p.Token, p.Tool, p.Arguments)
		out := antCallResult{Text: res.Text, IsError: res.IsError, Notices: res.Notices}
		if res.Data != nil {
			data, err := json.Marshal(res.Data)
			if err != nil {
				return nil, err
			}
			out.Data = data
		}
		return out, nil
	})
}

// decodeParams decodes a method's params, refusing missing or invalid
// ones as invalid params.
func decodeParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return &control.Error{Code: control.CodeInvalidParams, Message: "params are required"}
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return &control.Error{Code: control.CodeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	return nil
}
