package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Spec ant-tools, "Agent tries gh": anthill hook pre-tool-use blocks gh
// pr create with exit code 2 and the tool to use on standard error, and
// lets other commands through.
func TestPreToolUseHook(t *testing.T) {
	te := newTestEnv(t)
	input := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"gh pr create --fill"}}`
	code, stdout, stderr := te.commandWithInput(input, "hook", "pre-tool-use")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "call the open_pull_request tool instead") {
		t.Errorf("gh pr create: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	input = `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"go test ./..."}}`
	if code, stdout, stderr := te.commandWithInput(input, "hook", "pre-tool-use"); code != 0 || stdout != "" || stderr != "" {
		t.Errorf("go test: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, _, _ := te.commandWithInput("", "hook"); code != 2 {
		t.Errorf("hook without a subcommand: code %d, want 2", code)
	}
}

// Design D5: anthill hook post-tool-use takes the run's queued notices
// once and prints them as context; with none, an unknown token or no
// daemon, it prints nothing and exits with 0.
func TestPostToolUseHook(t *testing.T) {
	te := newTestEnv(t)
	if code, stdout, stderr := te.command("hook", "post-tool-use", "--ant", "ant_x"); code != 0 || stdout != "" || stderr != "" {
		t.Errorf("without daemon: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	tools, ant := serveAntTools(t, te)
	token, err := tools.Issue(app.ToolSession{
		Role: domain.RoleTaskRun, Ant: ant, Run: 1,
		Channel: app.RunChannel{Capabilities: ports.Capabilities{Hooks: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, stdout, _ := te.command("hook", "post-tool-use", "--ant", token); code != 0 || stdout != "" {
		t.Errorf("no notice: code %d, stdout %q", code, stdout)
	}
	means, err := tools.Deliver(context.Background(), ant, "12 minutes left")
	if err != nil || means != domain.DeliverHook {
		t.Fatalf("Deliver = %q, %v; want the hook", means, err)
	}
	code, stdout, _ := te.command("hook", "post-tool-use", "--ant", token)
	var out struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); code != 0 || err != nil {
		t.Fatalf("code %d, stdout %q: %v", code, stdout, err)
	}
	if out.HookSpecificOutput.HookEventName != "PostToolUse" || out.HookSpecificOutput.AdditionalContext != "Anthill notice: 12 minutes left" {
		t.Errorf("output = %+v", out)
	}
	if _, stdout, _ := te.command("hook", "post-tool-use", "--ant", token); stdout != "" {
		t.Errorf("the notice was given twice: %q", stdout)
	}
	if code, stdout, stderr := te.command("hook", "post-tool-use", "--ant", "ant_unknown"); code != 0 || stdout != "" || stderr != "" {
		t.Errorf("unknown token: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
