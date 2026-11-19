package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// WrapUp is how an ant whose run ended without finishing is wrapped up:
// after an exit or a crash (spec ant-execution, "Exit without
// finishing"), at the end of its budget (spec ant-timebox, "Budget
// exhaustion"), or after a daemon restart (spec ant-execution, "Restart
// resilience").
type WrapUp struct {
	// Reason is why the run ended, such as ReasonExitedWithoutFinish.
	Reason string
	// Summary is the text of the ticket comment, already safe to post: the
	// failure summary, or the progress summary. It is also the body of a
	// draft PR opened with OpenDraft.
	Summary string
	// OpenPoints are listed in the body of the draft PR, opened or not.
	OpenPoints []string
	// OpenDraft opens a draft PR when the branch has new commits and no
	// ant PR is open, as at the end of the budget. Otherwise no PR is
	// opened.
	OpenDraft bool
}

// WrappedUp is what a wrap-up did.
type WrappedUp struct {
	// Pushed is the number of new commits on the pushed branch, 0 when
	// nothing was pushed.
	Pushed int
	// PR is the ant PR, now a draft, or nil. Opened reports that the
	// wrap-up opened it.
	PR     *domain.Ref
	Opened bool
}

// wrapUp wraps up an ant whose run ended without finishing. The caller
// holds the ant's lock and has already moved the ant to its new state,
// such as failed or timed-out, and recorded the run; wrapUp does not
// change the state. It:
//  1. pushes the ant branch when it has new commits;
//  2. makes the open ant PR a draft, so that no review round starts on
//     half-done work, or opens a draft PR when w.OpenDraft is set and
//     there are new commits;
//  3. posts a summary comment on the ticket, with the PR link;
//  4. parks the task claim.
//
// Each step is done even when an earlier one failed, except that no PR
// is opened for a branch that was not pushed, and the errors are joined.
// The claim is parked even when the workspace or the code host cannot
// be found.
// run is the run that ended. It keys the ticket comment, so that a
// wrap-up done again edits the comment instead of posting another. It
// returns the ant's record, with the PR it opened.
func (s *AntSupervisor) wrapUp(ctx context.Context, rec ports.AntRecord, run ports.RunID, w WrapUp) (ports.AntRecord, WrappedUp, error) {
	var (
		done WrappedUp
		errs []error
	)
	ws, err := s.deps.Route(ctx, rec.Item)
	if err != nil {
		_, park := s.park(ctx, rec)
		return rec, done, errors.Join(fmt.Errorf("finding the workspace of %s: %w", rec.Item, err), park)
	}
	pushed, err := s.pushBranch(ctx, rec, ws)
	errs = append(errs, err)
	if err == nil {
		done.Pushed = pushed
	}
	host, err := s.deps.CodeHost(ctx, ws.Repo)
	if err != nil {
		_, park := s.park(ctx, rec)
		return rec, done, errors.Join(append(errs, err, park)...)
	}
	link := ""
	switch {
	case rec.PR != nil:
		draft := true
		u := ports.PullRequestUpdate{Draft: &draft}
		if len(w.OpenPoints) > 0 {
			if pr, _, err := host.PullRequest(ctx, *rec.PR); err != nil {
				errs = append(errs, fmt.Errorf("reading %s: %w", *rec.PR, err))
			} else {
				body := domain.WithOpenPoints(pr.Body, w.OpenPoints)
				u.Body = &body
			}
		}
		pr, err := host.UpdatePullRequest(ctx, *rec.PR, u)
		if err != nil {
			errs = append(errs, fmt.Errorf("making %s a draft: %w", *rec.PR, err))
		} else {
			link = pr.URL
		}
		done.PR = rec.PR
	case w.OpenDraft && done.Pushed > 0:
		pr, err := s.openAntPR(ctx, host, rec, ws, true, domain.FinishPRBody(w.Summary, w.OpenPoints))
		if err != nil {
			errs = append(errs, err)
			break
		}
		rec.PR, done.PR, done.Opened, link = &pr.Ref, &pr.Ref, true, pr.URL
		if err := s.store.SaveAnt(ctx, rec); err != nil {
			errs = append(errs, fmt.Errorf("storing the PR of ant %d: %w", rec.ID, err))
		}
	}
	errs = append(errs, s.wrapUpComment(ctx, rec, run, w, done, link))
	if _, err := s.park(ctx, rec); err != nil {
		errs = append(errs, err)
	}
	return rec, done, errors.Join(errs...)
}

// wrapUpComment posts the wrap-up's summary comment on the ticket.
func (s *AntSupervisor) wrapUpComment(ctx context.Context, rec ports.AntRecord, run ports.RunID, w WrapUp, done WrappedUp, link string) error {
	tracker, err := s.deps.Tracker(ctx, rec.Item)
	if err != nil {
		return err
	}
	instance, err := s.instance(ctx, rec.Item)
	if err != nil {
		return err
	}
	if link == "" && done.PR != nil {
		link = done.PR.String()
	}
	body := domain.WrapUpComment(domain.WrapUpNote{
		Reason: w.Reason,
		Text:   w.Summary,
		Branch: rec.Branch,
		Pushed: done.Pushed > 0,
		PR:     link,
		Draft:  done.PR != nil,
		Opened: done.Opened,
	})
	return s.postOnce(ctx, tracker, rec.Item, instance, fmt.Sprintf("wrap-up-%d-%d", rec.ID, run), body)
}

// endedWithoutFinish wraps up an ant whose run ended without finish, now
// in state: failed after an exit or a crash (spec ant-execution, "Exit
// without finishing"), or timed-out at the end of its budget (spec
// ant-timebox, "Budget exhaustion"), which opens a draft PR when the
// branch has new commits. The summary of a failed ant is the agent's last
// message, or the run's last progress report when the agent gave no
// message; that of a timed-out ant is the progress report. The worktree
// is kept, and the queen is told so that she can retry, continue or
// abandon.
func (s *AntSupervisor) endedWithoutFinish(ctx context.Context, rec ports.AntRecord, r *activeRun, state domain.AntState, reason string) error {
	w := WrapUp{Reason: reason, Summary: s.deps.Tokens.Progress(r.token)}
	kind, title := domain.NotifyFailed, "Run exited without finish"
	if state == domain.AntTimedOut {
		w.OpenDraft = true
		kind, title = domain.NotifyTimedOut, "Ant timed out"
	} else if strings.TrimSpace(r.lastMessage) != "" {
		w.Summary = s.deps.Tokens.Summary(rec.Worktree, r.lastMessage)
	}
	_, _, err := s.wrapUp(ctx, rec, r.run.ID, w)
	s.notifyAnt(ctx, kind, rec.Item, title, reason)
	return err
}
