package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// runnerSet returns a fake runner for each of names.
func runnerSet(names ...domain.Runner) (map[domain.Runner]ports.AgentRunner, map[domain.Runner]*testkit.AgentRunner) {
	runners := map[domain.Runner]ports.AgentRunner{}
	fakes := map[domain.Runner]*testkit.AgentRunner{}
	for _, n := range names {
		f := testkit.NewAgentRunner(ports.Capabilities{Resume: true})
		runners[n], fakes[n] = f, f
	}
	return runners, fakes
}

// Spec ant-execution, "Label overrides repository default": the
// repository's runner is Claude Code, the item has agent:codex, and the
// ant is started with Codex.
func TestSelectLabelOverridesRepositoryDefault(t *testing.T) {
	all, fakes := runnerSet(domain.RunnerClaudeCode, domain.RunnerCodex)
	runners := app.NewRunners(all)
	repo := domain.ConfigLayers{LocalEntry: &domain.SettingValues{Runner: ptr(domain.RunnerClaudeCode)}}.Resolve()

	settings, _ := repo.ForItem([]string{"anthill", "agent:codex"})
	name, runner, err := runners.Select(context.Background(), settings)
	if err != nil || name != domain.RunnerCodex || runner != ports.AgentRunner(fakes[domain.RunnerCodex]) {
		t.Errorf("with agent:codex: %q, %v; want codex", name, err)
	}
	settings, _ = repo.ForItem([]string{"anthill"})
	if name, runner, err := runners.Select(context.Background(), settings); err != nil || name != domain.RunnerClaudeCode || runner != ports.AgentRunner(fakes[domain.RunnerClaudeCode]) {
		t.Errorf("without label: %q, %v; want claude-code", name, err)
	}
	if name, _, err := runners.Select(context.Background(), domain.Defaults()); err != nil || name != domain.RunnerClaudeCode {
		t.Errorf("default: %q, %v; want claude-code", name, err)
	}
}

// Spec ant-execution, "Runner not installed": selecting a runner whose
// CLI is missing, or that this build has no adapter for, fails with
// "runner codex not available". Taking no claim is checked where
// acceptance is built (task group 14), which calls Select first.
func TestSelectRunnerNotInstalled(t *testing.T) {
	all, fakes := runnerSet(domain.RunnerClaudeCode, domain.RunnerCodex)
	notFound := errors.New(`exec: "codex": executable file not found in $PATH`)
	fakes[domain.RunnerCodex].Unavailable = notFound
	settings, _ := domain.Defaults().ForItem([]string{"agent:codex"})

	_, runner, err := app.NewRunners(all).Select(context.Background(), settings)
	var na *app.RunnerNotAvailableError
	if runner != nil || !errors.As(err, &na) || err.Error() != "runner codex not available" || !errors.Is(err, notFound) {
		t.Errorf("Select = %v, %v; want runner codex not available", runner, err)
	}

	only, _ := runnerSet(domain.RunnerClaudeCode)
	if _, _, err := app.NewRunners(only).Select(context.Background(), settings); err == nil || err.Error() != "runner codex not available" {
		t.Errorf("without an adapter: %v", err)
	}
}

// Spec pr-shepherd, "Review runner": reviews use the review runner, which
// defaults to the ant runner; without it installed there is no review.
func TestSelectReview(t *testing.T) {
	all, fakes := runnerSet(domain.RunnerClaudeCode, domain.RunnerCodex)
	runners := app.NewRunners(all)
	settings := domain.ConfigLayers{RepositoryFile: &domain.SettingValues{ReviewRunner: ptr(domain.RunnerCodex)}}.Resolve()
	if name, _, err := runners.SelectReview(context.Background(), settings); err != nil || name != domain.RunnerCodex {
		t.Errorf("review runner = %q, %v; want codex", name, err)
	}
	if name, _, err := runners.SelectReview(context.Background(), domain.Defaults()); err != nil || name != domain.RunnerClaudeCode {
		t.Errorf("default review runner = %q, %v; want the ant runner", name, err)
	}
	fakes[domain.RunnerCodex].Unavailable = errors.New("not installed")
	if _, _, err := runners.SelectReview(context.Background(), settings); err == nil || err.Error() != "runner codex not available" {
		t.Errorf("review runner not installed: %v", err)
	}
}
