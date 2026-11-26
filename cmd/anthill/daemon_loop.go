package main

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// staleCheckInterval is how often the daemon looks for questions pending
// for longer than the question-staleness setting.
const staleCheckInterval = 5 * time.Minute

// runLoop is the daemon's work after start: it catches up with what
// happened while it was down, then reacts to each poll. Polls of
// different connections are handled one at a time, so that each change
// event is handled once and in order.
type runLoop struct {
	s      *services
	store  ports.SessionIndex
	outbox *app.OutboxDispatcher
	clock  ports.Clock
	watch  *proposalWatch
	logger *slog.Logger

	// mu serializes the work after each poll.
	mu sync.Mutex
	// drained collects the PRs with a change event in the current drain.
	drained []domain.Ref
}

func newRunLoop(s *services, store ports.StateStore, clock ports.Clock, notifier ports.Notifier, logger *slog.Logger) *runLoop {
	l := &runLoop{s: s, store: store, clock: clock, watch: newProposalWatch(notifier), logger: logger}
	// Each change event goes to Claiming, for a force-released claim,
	// and to the change reactor (design D7).
	l.outbox = app.NewOutboxDispatcher(store, clock, func(ctx context.Context, e ports.OutboxEvent) error {
		l.noteEvent(e)
		if err := s.claiming.HandleChange(ctx, e.Event); err != nil {
			return err
		}
		return s.reactor.Handle(ctx, e)
	})
	return l
}

// detach is the end of the daemon's run, once runAll has returned and
// before the store is closed: the supervisor and the shepherd let go of
// their agents (AntSupervisor.Detach, Shepherd.Detach), and detach waits
// for the background work still in progress, so that none of it uses the
// store once it is closed. The wait is bounded: no agent is waited for,
// apart from the StopGrace of those being stopped.
func (l *runLoop) detach() {
	l.s.supervisor.Detach()
	l.s.shepherd.Detach()
	l.s.supervisor.Wait()
	l.s.shepherd.Wait()
}

// services returns the loops the daemon runs until shutdown, for runAll.
func (l *runLoop) services() []func(context.Context) error {
	l.s.scheduler.SetAfterPoll(l.afterPoll)
	return []func(context.Context) error{
		func(ctx context.Context) error {
			l.catchUp(ctx)
			l.s.scheduler.Run(ctx)
			return nil
		},
		func(ctx context.Context) error {
			l.s.claiming.Run(ctx)
			return nil
		},
		func(ctx context.Context) error {
			l.s.supervisor.RunTimekeeper(ctx, 0)
			return nil
		},
		func(ctx context.Context) error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-l.clock.After(staleCheckInterval):
					l.report(ctx, "checking stale questions", l.s.conversation.CheckStale(ctx))
				}
			}
		},
	}
}

// catchUp runs once at start, before the first poll: it rebuilds missing
// ant records, wraps up the runs the restart interrupted, starts queued
// ants, handles the change events left in the outbox, triggers the
// shepherd on the PRs of this instance's ants, and evaluates the whole
// proposal list (spec ant-execution, "Restart reconciliation";
// change-reaction, "Changes are not lost across restarts").
func (l *runLoop) catchUp(ctx context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rep, err := l.s.recovery.Recover(ctx); err != nil {
		l.report(ctx, "recovering the session index", err)
	} else if len(rep.Rebuilt) > 0 {
		l.logger.Info("ant records rebuilt", "count", len(rep.Rebuilt))
	}
	l.report(ctx, "reconciling the ants", l.s.supervisor.Reconcile(ctx))
	l.report(ctx, "reconciling the stored changes", l.s.reactor.Reconcile(ctx))
	prs := l.drain(ctx)
	ants, err := l.store.Ants(ctx)
	l.report(ctx, "listing the ants", err)
	for _, a := range ants {
		if a.PR != nil && !a.State().Final() && !slices.Contains(prs, *a.PR) {
			prs = append(prs, *a.PR)
		}
	}
	l.trigger(ctx, prs)
	l.evaluate(ctx, "", l.s.console)
}

// afterPoll handles one poll of a connection: the change events it found,
// the PRs that ended, the shepherd on the PRs that changed, and the
// proposals of the connection's repositories (spec task-eligibility,
// "Proposal list": eligibility is re-evaluated after every poll).
func (l *runLoop) afterPoll(ctx context.Context, conn app.Connection, _ app.PollReport) {
	l.mu.Lock()
	defer l.mu.Unlock()
	prs := l.drain(ctx)
	l.report(ctx, "checking for ended PRs", l.s.closure.Check(ctx))
	l.trigger(ctx, prs)
	if c, ok := l.s.consoles[conn.Name]; ok {
		l.evaluate(ctx, conn.Name, c)
	}
}

// drain handles the pending change events and triggers the ants they
// were routed to. It returns the PRs that had an event, in event order.
func (l *runLoop) drain(ctx context.Context) []domain.Ref {
	l.drained = nil
	_, err := l.outbox.Drain(ctx)
	l.report(ctx, "handling change events", err)
	l.report(ctx, "triggering ants after their changes", l.s.reactor.Flush(ctx))
	return l.drained
}

// noteEvent records the PR of a change event for the shepherd; the
// outbox handler calls it before handling the event.
func (l *runLoop) noteEvent(e ports.OutboxEvent) {
	if ref := e.Event.Ref; ref.Kind == domain.KindPR && !slices.Contains(l.drained, ref) {
		l.drained = append(l.drained, ref)
	}
}

// trigger asks the shepherd what comes next on each PR.
func (l *runLoop) trigger(ctx context.Context, prs []domain.Ref) {
	slices.SortFunc(prs, func(a, b domain.Ref) int { return strings.Compare(a.Full(), b.Full()) })
	for _, pr := range prs {
		l.report(ctx, "triggering the shepherd on "+pr.Full(), l.s.shepherd.Trigger(ctx, pr))
	}
}

// evaluate builds a proposal list and notifies the new proposals.
func (l *runLoop) evaluate(ctx context.Context, connection string, c *app.Console) {
	list, err := c.Proposals(ctx)
	if err != nil {
		l.report(ctx, "evaluating the proposals", err)
		return
	}
	for _, w := range list.Warnings {
		l.logger.Warn("proposal list incomplete", "reason", w)
	}
	l.watch.update(ctx, connection, list)
}

// report logs the error of background work, unless the daemon is
// stopping: once ctx is done, an error is the stop's doing, whatever its
// form, such as a cancelled query or a killed git. An error from before
// the stop is still logged.
func (l *runLoop) report(ctx context.Context, what string, err error) {
	if err != nil && ctx.Err() == nil {
		l.logger.Error(what+" failed", "error", err)
	}
}
