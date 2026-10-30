package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/matteorosani/anthill/internal/adapters/control"
	"github.com/matteorosani/anthill/internal/adapters/mcpserver"
)

func newMCPCommand(env *environment) *cobra.Command {
	var token, address string
	cmd := &cobra.Command{
		Use:   "mcp --ant <token>",
		Short: "Serve an agent's ant tools over stdio (started by Anthill, not by hand)",
		Long: "Serve the ant tools of one run as an MCP server on stdin and stdout. " +
			"Anthill adds this command to the agent's configuration when it starts a run; " +
			"every call is relayed to the daemon, which checks the token.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if token == "" {
				return &usageError{errors.New("mcp needs --ant <token>")}
			}
			if address == "" {
				if env.err != nil {
					return env.err
				}
				address = env.address
			}
			relay := &controlRelay{client: control.NewClient(address), token: token}
			return mcpserver.Serve(cmd.Context(), relay, version, &mcp.StdioTransport{})
		},
	}
	cmd.Flags().StringVar(&token, "ant", "", "the run's tool token")
	cmd.Flags().StringVar(&address, "address", "", "the daemon's control address (for tests)")
	_ = cmd.Flags().MarkHidden("address")
	return cmd
}

// controlRelay relays the MCP server's calls to the daemon over the
// control interface, with the run's token.
type controlRelay struct {
	client *control.Client
	token  string
}

// Tools asks the daemon for the token's tools.
func (r *controlRelay) Tools(ctx context.Context) ([]mcpserver.Tool, error) {
	var res antToolsResult
	err := r.client.Call(ctx, methodAntTools, antToolsParams{Token: r.token}, &res)
	if errors.Is(err, control.ErrNotRunning) {
		return nil, errors.New("ant tools unavailable: the daemon is not running")
	}
	if err != nil {
		return nil, fmt.Errorf("ant tools unavailable: %w", err)
	}
	tools := make([]mcpserver.Tool, len(res.Tools))
	for i, t := range res.Tools {
		tools[i] = mcpserver.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
	}
	return tools, nil
}

// Call relays one call.
func (r *controlRelay) Call(ctx context.Context, tool string, args json.RawMessage) (mcpserver.Result, error) {
	var res antCallResult
	if err := r.client.Call(ctx, methodAntCall, antCallParams{Token: r.token, Tool: tool, Arguments: args}, &res); err != nil {
		return mcpserver.Result{}, err
	}
	return mcpserver.Result{Text: res.Text, Data: res.Data, IsError: res.IsError, Notices: res.Notices}, nil
}
