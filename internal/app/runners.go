package app

import (
	"context"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Runners holds the agent runners this build supports, and picks the one
// for an ant or a review round.
type Runners struct {
	runners map[domain.Runner]ports.AgentRunner
}

// NewRunners returns the selection over runners.
func NewRunners(runners map[domain.Runner]ports.AgentRunner) *Runners {
	m := make(map[domain.Runner]ports.AgentRunner, len(runners))
	for name, r := range runners {
		m[name] = r
	}
	return &Runners{runners: m}
}

// RunnerNotAvailableError is returned when the selected runner's CLI
// cannot be started on this machine, or this build has no adapter for
// it. Acceptance then fails before any claim is taken (spec
// ant-execution, "Runner not installed").
type RunnerNotAvailableError struct {
	Runner domain.Runner
	// Err is why, or nil when there is no adapter.
	Err error
}

func (e *RunnerNotAvailableError) Error() string {
	return fmt.Sprintf("runner %s not available", e.Runner)
}

func (e *RunnerNotAvailableError) Unwrap() error {
	return e.Err
}

// Select returns the runner of an ant. settings are the repository's
// effective settings with the item's labels applied (Effective.ForItem),
// so the runner comes from the agent: label, then the local repository
// entry, then the global configuration, then the default (spec
// ant-execution, "Agent runners are interchangeable").
func (r *Runners) Select(ctx context.Context, settings domain.Effective) (domain.Runner, ports.AgentRunner, error) {
	return r.available(ctx, settings.Runner)
}

// SelectReview returns the runner of review rounds and auto-fixes: the
// repository's review runner, which defaults to its ant runner (spec
// pr-shepherd, "Review runner"). An instance without it takes no review
// claim.
func (r *Runners) SelectReview(ctx context.Context, settings domain.Effective) (domain.Runner, ports.AgentRunner, error) {
	return r.available(ctx, settings.ReviewRunner)
}

func (r *Runners) available(ctx context.Context, name domain.Runner) (domain.Runner, ports.AgentRunner, error) {
	runner, ok := r.runners[name]
	if !ok {
		return name, nil, &RunnerNotAvailableError{Runner: name}
	}
	if err := runner.Available(ctx); err != nil {
		return name, nil, &RunnerNotAvailableError{Runner: name, Err: err}
	}
	return name, runner, nil
}
