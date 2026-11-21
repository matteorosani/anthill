package main

import (
	"context"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// late lets a service reach services made after it. The ant tools hand
// finishes, reviews and fix results to the supervisor, the shepherd's
// rounds and the fix pass, which need the ant tools for their tool
// tokens; the conversation hands commands to the supervisor and the
// shepherd, which need the conversation; and Claiming hands lost claims
// to the supervisor and the shepherd, which claim through it. buildDaemon
// fills late once every service is made, before anything runs.
type late struct {
	supervisor *app.AntSupervisor
	shepherd   *app.Shepherd
	rounds     *app.ShepherdRounds
	fixPass    *app.FixPass
}

var (
	_ app.FinishHandler      = (*late)(nil)
	_ app.RunEnder           = (*late)(nil)
	_ app.ReviewSink         = (*late)(nil)
	_ app.FixSink            = (*late)(nil)
	_ app.AntCommands        = (*late)(nil)
	_ app.ReviewRequests     = (*late)(nil)
	_ app.FixRequests        = (*late)(nil)
	_ ports.ClaimLossHandler = (*late)(nil)
)

// Finish hands an ant's finish to the supervisor.
func (l *late) Finish(ctx context.Context, f app.FinishRequest) error {
	return l.supervisor.Finish(ctx, f)
}

// EndRun ends a run after its agent asked a question.
func (l *late) EndRun(ctx context.Context, ant ports.AntID, run ports.RunID, reason string) error {
	return l.supervisor.EndRun(ctx, ant, run, reason)
}

// Trigger, Stop and Retry carry out the commands on an ant.
func (l *late) Trigger(ctx context.Context, id ports.AntID) error {
	return l.supervisor.Trigger(ctx, id)
}

func (l *late) Stop(ctx context.Context, id ports.AntID, reason string) error {
	return l.supervisor.Stop(ctx, id, reason)
}

func (l *late) Retry(ctx context.Context, id ports.AntID) error {
	return l.supervisor.Retry(ctx, id)
}

// SubmitReview hands a review agent's report to its round.
func (l *late) SubmitReview(ctx context.Context, r app.ReviewReport) error {
	return l.rounds.SubmitReview(ctx, r)
}

// FinishFixes hands a fix agent's results to its fix pass.
func (l *late) FinishFixes(ctx context.Context, r app.FixReport) error {
	return l.fixPass.FinishFixes(ctx, r)
}

// CanReview, RequestRound and OptInFix carry out /anthill review and
// /anthill fix on a PR.
func (l *late) CanReview(ctx context.Context, pr domain.Ref) error {
	return l.shepherd.CanReview(ctx, pr)
}

func (l *late) RequestRound(ctx context.Context, pr domain.Ref, commentID string) error {
	return l.shepherd.RequestRound(ctx, pr, commentID)
}

func (l *late) OptInFix(ctx context.Context, pr domain.Ref, thread, commandComment string) (bool, error) {
	return l.shepherd.OptInFix(ctx, pr, thread, commandComment)
}

// ClaimLost ends the work behind a lost claim (spec work-claiming,
// "Holder returns after expiry" and "Manual override"): the active run of
// a task claim's ant, or the round or fix pass of a review claim's PR.
func (l *late) ClaimLost(ctx context.Context, ref domain.Ref, kind domain.ClaimKind, reason string) error {
	if kind == domain.ClaimReview {
		return l.shepherd.StopPR(ctx, ref)
	}
	return l.supervisor.ClaimLost(ctx, ref, kind, reason)
}
