package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/matteorosani/anthill/internal/adapters/control"
	"github.com/matteorosani/anthill/internal/adapters/mcpserver"
	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// heldClaims lists the held claims of a fake store.
type heldClaims struct{ store *testkit.StateStore }

func (h heldClaims) Held(ctx context.Context) ([]ports.HeldClaim, error) {
	return h.store.HeldClaims(ctx)
}

// serveAntTools serves the control interface of te with ant tools over
// fakes: the ant of acme/shop#12 has an active run and holds its claim,
// and the review claim on PR 34 is held. It returns the service.
func serveAntTools(t *testing.T, te *testEnv) (*app.AntTools, ports.AntID) {
	t.Helper()
	ctx := context.Background()
	repo := domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "shop"}
	item := domain.Ref{Repo: repo, Kind: domain.KindItem, Number: 12}
	pr := domain.Ref{Repo: repo, Kind: domain.KindPR, Number: 34}
	gh := testkit.NewProvider(te.clock)
	gh.PutItem(domain.WorkItem{Ref: item, Title: "Fix login", State: domain.StateOpen})
	store := testkit.NewStateStore()
	ant, err := domain.NewAnt(item, domain.AntStarting, time.Hour, domain.DefaultBudgetPolicy(), te.clock.Now(), "accepted")
	if err != nil {
		t.Fatal(err)
	}
	if err := ant.Transition(domain.AntRunning, te.clock.Now(), "runner started"); err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateAnt(ctx, ports.AntRecord{Item: item, Base: time.Hour, Budget: ant.Budget, History: ant.History(), Branch: "ant/gh-12-fix-login", BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []ports.HeldClaim{{Ref: item, Kind: domain.ClaimTask}, {Ref: pr, Kind: domain.ClaimReview}} {
		if err := store.SaveClaim(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	tools := app.NewAntTools(app.AntToolsDeps{
		Store:    store,
		Claims:   heldClaims{store},
		Tracker:  func(context.Context, domain.Ref) (ports.TrackerPort, error) { return gh.Client("mrosani"), nil },
		CodeHost: func(context.Context, domain.Repository) (ports.CodeHostPort, error) { return gh.Client("mrosani"), nil },
		Clock:    te.clock,
	}, app.AntToolsOptions{})

	srv := control.NewServer(testLogger(t))
	registerAPI(srv, app.NewHealth(te.clock, version, 1), tools)
	ln, err := control.Listen(te.address)
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(sctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return tools, id
}

// connectAgent starts the MCP server of anthill mcp for token on an
// in-memory transport, relaying to the daemon at te's address, and
// connects an MCP client to it, as an agent's runner does.
func connectAgent(t *testing.T, te *testEnv, token string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	relay := &controlRelay{client: control.NewClient(te.address), token: token}
	server, err := mcpserver.New(ctx, relay, version)
	if err != nil {
		t.Fatal(err)
	}
	serverT, clientT := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, serverT) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1"}, nil).Connect(ctx, clientT, nil)
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

// Task 12.1: an MCP client lists the tools of each role, relayed from the
// daemon through anthill mcp.
func TestMCPListsTheToolsOfEachRole(t *testing.T) {
	te := newTestEnv(t)
	tools, ant := serveAntTools(t, te)
	repo := domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "shop"}
	pr := domain.Ref{Repo: repo, Kind: domain.KindPR, Number: 34}
	for _, role := range domain.AgentRoles {
		s := app.ToolSession{Role: role, Ant: ant, Run: 1}
		if !role.IsRun() {
			s = app.ToolSession{Role: role, PR: pr, Round: 1}
		}
		token, err := tools.Issue(s)
		if err != nil {
			t.Fatal(err)
		}
		res, err := connectAgent(t, te, token).ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tool := range res.Tools {
			names = append(names, tool.Name)
			if tool.Description == "" || tool.InputSchema == nil {
				t.Errorf("%s: %s has no description or schema", role, tool.Name)
			}
		}
		slices.Sort(names)
		want := domain.ToolsFor(role)
		slices.Sort(want)
		if !slices.Equal(names, want) {
			t.Errorf("%s tools = %v, want %v", role, names, want)
		}
	}
}

// A call goes through the control interface and comes back with its
// result or its refusal.
func TestMCPRelaysACall(t *testing.T) {
	te := newTestEnv(t)
	tools, ant := serveAntTools(t, te)
	token, err := tools.Issue(app.ToolSession{Role: domain.RoleTaskRun, Ant: ant, Run: 1})
	if err != nil {
		t.Fatal(err)
	}
	session := connectAgent(t, te, token)
	te.clock.Advance(20 * time.Minute)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: domain.ToolTimeRemaining, Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, `"remaining_minutes":40`) {
		t.Errorf("time_remaining = %+v", res.Content[0])
	}

	tools.Revoke(token)
	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: domain.ToolTimeRemaining, Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || res.Content[0].(*mcp.TextContent).Text != "unknown or revoked tool token" {
		t.Errorf("call after revoke = %+v", res.Content[0])
	}
}

// Task 13.2: the fake agent, run headless as a separate process, starts
// anthill mcp as a runner does and calls a tool through it.
func TestFakeAgentCallsToolsThroughAnthillMCP(t *testing.T) {
	te := newTestEnv(t)
	tools, ant := serveAntTools(t, te)
	token, err := tools.Issue(app.ToolSession{Role: domain.RoleTaskRun, Ant: ant, Run: 1})
	if err != nil {
		t.Fatal(err)
	}
	te.clock.Advance(20 * time.Minute)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, testkit.FakeAgentArgs(testkit.AgentScript{
		MCPCommand: []string{exe, anthillFlag, "mcp", "--ant", token, "--address", te.address},
		Steps: []testkit.AgentStep{
			{Op: testkit.StepTool, Tool: domain.ToolTimeRemaining, Args: json.RawMessage(`{}`)},
		},
	})...)
	cmd.Dir = t.TempDir()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("fake agent: %v: %s", err, stderr.String())
	}
	var call *testkit.AgentEvent
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var e testkit.AgentEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("output line %q: %v", line, err)
		}
		if e.Event == "tool" {
			call = &e
		}
	}
	if call == nil || call.IsError || !strings.Contains(call.Text, `"remaining_minutes":40`) {
		t.Errorf("tool event = %+v; stderr: %s", call, stderr.String())
	}
}

// anthill mcp exits with an error, before serving anything, when the
// daemon does not know the token or is not running.
func TestMCPCommandWithoutTools(t *testing.T) {
	te := newTestEnv(t)
	code, _, stderr := te.command("mcp", "--ant", "ant_unknown")
	if code != 1 || !strings.Contains(stderr, "ant tools unavailable: the daemon is not running") {
		t.Errorf("without daemon: code %d, stderr %q", code, stderr)
	}
	serveAntTools(t, te)
	code, _, stderr = te.command("mcp", "--ant", "ant_unknown")
	if code != 1 || !strings.Contains(stderr, "ant tools unavailable: unknown or revoked tool token") {
		t.Errorf("unknown token: code %d, stderr %q", code, stderr)
	}
	code, _, stderr = te.command("mcp")
	if code != 2 || !strings.Contains(stderr, "mcp needs --ant <token>") {
		t.Errorf("no token: code %d, stderr %q", code, stderr)
	}
}

// testLogger discards what the control server logs.
func testLogger(*testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
