package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// ClassifierTimeout is how long a classifier call may take before the
// executor ends it, and the change counts as actionable (design D8).
const ClassifierTimeout = 2 * time.Minute

// ErrNoVerdict is a classifier call that ended without a reply the
// change reactor can read.
var ErrNoVerdict = errors.New("the classifier gave no verdict")

// Classifier classifies a human comment or review with a short agent
// call through the executor (design D8, D21): a new session of the ant's
// runner, with no ant tools and no worktree, given the item and the
// comment, ended at ClassifierTimeout. Its verdict is the agent's last
// message.
type Classifier struct {
	exec    ports.Executor
	clock   ports.Clock
	environ []string
}

// NewClassifier returns the classifier. environ is Anthill's own
// environment, from which the agent's is made (domain.AgentEnv).
func NewClassifier(exec ports.Executor, clock ports.Clock, environ []string) *Classifier {
	return &Classifier{exec: exec, clock: clock, environ: environ}
}

// Classify runs one classifier call. It fails when the call cannot
// start, ends at its deadline, exits with an error, or gives no verdict;
// the change reactor then counts the change as actionable.
func (c *Classifier) Classify(ctx context.Context, ant ports.AntRecord, item domain.WorkItem, e domain.ChangeEvent) (domain.Relevance, error) {
	x, err := c.exec.Start(ctx, ports.ExecSpec{
		Runner:   domain.Runner(ant.Runner),
		Run:      ports.RunSpec{Brief: domain.ClassifierPrompt(item, e), Env: domain.AgentEnv(c.environ)},
		Deadline: c.clock.Now().Add(ClassifierTimeout),
	})
	if err != nil {
		return "", fmt.Errorf("starting the classifier: %w", err)
	}
	var (
		reply string
		last  ports.ExecEvent
	)
	for ev := range x.Events {
		// The last message comes again each time it changes; the last
		// one wins.
		if ev.Kind == ports.ExecLastMessage {
			reply = ev.Message
		}
		if ev.Final() {
			last = ev
		}
	}
	switch {
	case last.Kind == ports.ExecEndedAtDeadline:
		return "", fmt.Errorf("the classifier took longer than %s", ClassifierTimeout)
	case last.Exit.Code != 0 || last.Exit.Crashed:
		return "", fmt.Errorf("the classifier exited with code %d", last.Exit.Code)
	}
	rel, ok := domain.ParseClassifierReply(reply)
	if !ok {
		return "", ErrNoVerdict
	}
	return rel, nil
}
