package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Reasons and notification titles of the finish handling.
const (
	// ReasonFinishPrefix starts the reason of a run that ended with
	// finish and of the ant's state after it: "finish: complete".
	ReasonFinishPrefix = "finish: "
)

// finishReason is the reason recorded for a finish with status st.
func finishReason(st domain.FinishStatus) string {
	return ReasonFinishPrefix + string(st)
}

// finishOutcome is the outcome of a run that ended with finish.
func finishOutcome(st domain.FinishStatus) ports.RunOutcome {
	switch st {
	case domain.FinishPartial:
		return ports.OutcomePartial
	case domain.FinishNoChange:
		return ports.OutcomeNoChange
	}
	return ports.OutcomeComplete
}

// Finish ends the run whose agent called finish (spec ant-tools, "Closing
// tools end the run"): the finish tool has already stored the finish with
// the move to finishing. Finish records how the run ends and stops its
// process tree in the background, so the tool's result reaches the agent
// first, and returns. Once the process has ended, the run is recorded and
// the playbook carries out the finish (see exited): the PR, the claim and
// the ant's next state.
func (s *AntSupervisor) Finish(_ context.Context, f FinishRequest) error {
	return s.endActiveRun(f.Ant, f.Run, finishOutcome(f.Status), finishReason(f.Status))
}

// EndRun ends the run whose agent asked a question, with its session
// saved (spec agent-conversation, "Ants ask questions through the
// ticket"): the conversation tools have already moved the ant to
// awaiting-input. The run is recorded as stopped with reason once its
// process has ended, as with Finish.
func (s *AntSupervisor) EndRun(_ context.Context, ant ports.AntID, run ports.RunID, reason string) error {
	return s.endActiveRun(ant, run, ports.OutcomeStopped, reason)
}

// endActiveRun records how the ant's active run ends, then stops its
// process tree in the background. It fails when run is not the ant's
// active run.
func (s *AntSupervisor) endActiveRun(ant ports.AntID, run ports.RunID, outcome ports.RunOutcome, reason string) error {
	s.mu.Lock()
	var r *activeRun
	for _, a := range s.runs {
		if a.ant == ant && a.run.ID == run && a.exec != "" {
			r = a
			break
		}
	}
	if r == nil {
		s.mu.Unlock()
		return fmt.Errorf("ant %d: run %d is not active", ant, run)
	}
	r.outcome, r.reason = outcome, reason
	s.mu.Unlock()
	s.stopInBackground(r.exec)
	return nil
}

// stopInBackground stops a process tree without waiting for it. Its last
// event reaches the run's event reader, which records the end.
func (s *AntSupervisor) stopInBackground(exec ports.ExecID) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.report(s.deps.Executor.Stop(exec, StopGrace))
	}()
}

// openPR carries out an OpenPR decision: a task run finished complete or
// partial with new commits but did not open the ant PR, so Anthill pushes
// the branch and opens it, titled after the ticket, with the finish
// summary and the open points as its body (spec ant-execution, "Finish
// without opening a PR"; "Partial completion"). Its effect: the ant has
// a PR. A PR already open from the ant branch, such as one opened before
// a crash, is taken instead of opening a second.
func (s *AntSupervisor) openPR(ctx context.Context, t *antTurn, d domain.OpenPR) (bool, error) {
	rec := t.rec
	if _, err := s.pushBranch(ctx, rec, t.ws); err != nil {
		return false, err
	}
	host, err := s.deps.CodeHost(ctx, t.ws.Repo)
	if err != nil {
		return false, err
	}
	pr, err := s.openAntPR(ctx, host, rec, t.ws, d.Draft, domain.FinishPRBody(d.Summary, d.OpenPoints))
	if err != nil {
		return false, err
	}
	rec.PR = &pr.Ref
	if err := s.store.SaveAnt(ctx, rec); err != nil {
		return false, fmt.Errorf("storing the PR of ant %d: %w", rec.ID, err)
	}
	return true, nil
}

// openAntPR opens the ant PR from the pushed ant branch, titled after the
// ticket, with the closing reference and the opt-in label, or returns the
// one already open from the branch.
func (s *AntSupervisor) openAntPR(ctx context.Context, host ports.CodeHostPort, rec ports.AntRecord, ws ToolWorkspace, draft bool, body string) (domain.PullRequest, error) {
	page, err := host.PullRequests(ctx, ws.Repo, "")
	if err != nil {
		return domain.PullRequest{}, fmt.Errorf("listing the open pull requests: %w", err)
	}
	for _, pr := range page.PullRequests {
		if pr.SourceBranch == rec.Branch {
			return pr, nil
		}
	}
	tracker, err := s.deps.Tracker(ctx, rec.Item)
	if err != nil {
		return domain.PullRequest{}, err
	}
	item, _, err := tracker.Item(ctx, rec.Item)
	if err != nil {
		return domain.PullRequest{}, fmt.Errorf("reading %s: %w", rec.Item, err)
	}
	pr, err := host.OpenPullRequest(ctx, ports.NewPullRequest{
		Repo:   ws.Repo,
		Source: rec.Branch,
		Target: rec.BaseBranch,
		Title:  item.Title,
		Body:   domain.WithClosingReference(body, rec.Item, ws.Repo),
		Draft:  draft,
		Labels: []string{ws.Settings.OptInLabel},
		Item:   &rec.Item,
	})
	if err != nil {
		return domain.PullRequest{}, fmt.Errorf("opening the pull request: %w", err)
	}
	return pr, nil
}

// completeFinish carries out a CompleteFinish decision: after partial the
// PR becomes a draft whatever the agent chose and its body lists the open
// points; after complete a draft that an earlier partial or timed-out run
// forced is marked ready (spec ant-execution, "Draft PRs for incomplete
// work"). A follow-up run's no-change leaves the PR as it is. Anthill runs
// no build or tests ("Complete without local verification"). The task
// claim is parked, and the ant is succeeded with its finish cleared:
// that is the decision's effect.
func (s *AntSupervisor) completeFinish(ctx context.Context, t *antTurn, d domain.CompleteFinish) (bool, error) {
	rec := t.rec
	if rec.PR != nil && (d.MarkDraft || d.MarkReady) {
		if err := s.updateDraft(ctx, *rec.PR, d); err != nil {
			return false, err
		}
	}
	if done, err := s.park(ctx, rec); !done {
		return false, err
	}
	rec.Finish = nil
	rec, err := s.transition(ctx, rec, domain.AntSucceeded, finishReason(d.Status))
	if err != nil {
		return false, err
	}
	s.notifyAnt(ctx, domain.NotifySucceeded, rec.Item, "Ant finished", finishReason(d.Status))
	return true, nil
}

// updateDraft makes the ant PR a draft listing the open points, or marks
// it ready.
func (s *AntSupervisor) updateDraft(ctx context.Context, ref domain.Ref, d domain.CompleteFinish) error {
	host, err := s.deps.CodeHost(ctx, ref.Repo)
	if err != nil {
		return err
	}
	var u ports.PullRequestUpdate
	draft := d.MarkDraft
	u.Draft = &draft
	if d.MarkDraft {
		pr, _, err := host.PullRequest(ctx, ref)
		if err != nil {
			return fmt.Errorf("reading %s: %w", ref, err)
		}
		body := domain.WithOpenPoints(pr.Body, d.OpenPoints)
		u.Body = &body
	}
	if _, err := host.UpdatePullRequest(ctx, ref, u); err != nil {
		return fmt.Errorf("updating %s: %w", ref, err)
	}
	return nil
}

// park parks the ant's task claim. A claim lost meanwhile has already
// ended the ant (spec work-claiming): park then reports false with no
// error, and the trigger reads the ant again.
func (s *AntSupervisor) park(ctx context.Context, rec ports.AntRecord) (bool, error) {
	err := s.deps.Claims.Park(ctx, rec.Item)
	var taken *ClaimTakenError
	switch {
	case errors.As(err, &taken):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("parking the claim on %s: %w", rec.Item, err)
	}
	return true, nil
}

// finishWithNoChange carries out a task run's no-change finish (spec
// ant-execution, "No change needed"): the explanation is posted on the
// ticket, the hold label added and the task claim released. Its effect:
// the ant is done, with its finish cleared. No PR is opened.
func (s *AntSupervisor) finishWithNoChange(ctx context.Context, t *antTurn, d domain.FinishWithNoChange) (bool, error) {
	rec := t.rec
	tracker, err := s.deps.Tracker(ctx, rec.Item)
	if err != nil {
		return false, err
	}
	instance, err := s.instance(ctx, rec.Item)
	if err != nil {
		return false, err
	}
	hold := t.ws.Settings.HoldLabel
	body := domain.MessageNoChangeExplanation(d.Explanation, hold)
	if err := s.postOnce(ctx, tracker, rec.Item, instance, fmt.Sprintf("no-change-%d", rec.ID), body); err != nil {
		return false, err
	}
	if err := tracker.AddLabel(ctx, rec.Item, hold); err != nil {
		return false, fmt.Errorf("adding %s to %s: %w", hold, rec.Item, err)
	}
	if err := s.deps.Claims.Release(ctx, rec.Item, domain.ClaimTask); err != nil {
		return false, fmt.Errorf("releasing the claim on %s: %w", rec.Item, err)
	}
	rec.Finish = nil
	rec, err = s.transition(ctx, rec, domain.AntDone, finishReason(domain.FinishNoChange))
	if err != nil {
		return false, err
	}
	s.notifyAnt(ctx, domain.NotifyNoChange, rec.Item, "No change needed", domain.Truncate(d.Explanation, 200))
	return true, nil
}

// postOnce posts an Anthill summary comment on an item, or edits the one
// already posted with the same marker id, such as by a decision carried
// out again after a crash.
func (s *AntSupervisor) postOnce(ctx context.Context, tracker ports.TrackerPort, item domain.Ref, instance, id, body string) error {
	ac := domain.AnthillComment{
		Role:   domain.RoleAnt,
		Marker: domain.Marker{Version: domain.MarkerVersion, ID: id, Instance: instance, Kind: domain.CommentSummary, Ref: item.String()},
		Body:   body,
	}
	wi, _, err := tracker.Item(ctx, item)
	if err != nil {
		return fmt.Errorf("reading %s: %w", item, err)
	}
	for _, c := range wi.Comments {
		if m, ok := domain.FindMarker(c.Body); ok && m.ID == id && m.Instance == instance {
			if _, err := tracker.EditComment(ctx, item, c.ID, ac); err != nil {
				return fmt.Errorf("editing the comment on %s: %w", item, err)
			}
			return nil
		}
	}
	if _, err := tracker.AddComment(ctx, item, ac); err != nil {
		return fmt.Errorf("commenting on %s: %w", item, err)
	}
	return nil
}

// pushBranch pushes the ant branch under the branching policy when it has
// new commits, as the push tool does, and returns the number of new
// commits. A branch without new commits is not pushed.
func (s *AntSupervisor) pushBranch(ctx context.Context, rec ports.AntRecord, ws ToolWorkspace) (int, error) {
	if rec.Worktree == "" {
		// An ant rebuilt without a branch has no work to push (spec
		// ant-execution, "Session index recovery").
		return 0, nil
	}
	commits, err := s.deps.Git.NewCommits(ctx, ws.Clone, rec.Worktree, rec.BaseBranch)
	if err != nil {
		return 0, fmt.Errorf("counting the new commits of %s: %w", rec.Item, err)
	}
	if len(commits) == 0 {
		return 0, nil
	}
	instance, err := s.instance(ctx, rec.Item)
	if err != nil {
		return 0, err
	}
	_, err = s.deps.Workspace.Push(ctx, PushRequest{
		Clone:    ws.Clone,
		Worktree: rec.Worktree,
		Owner:    domain.BranchOwner{Item: rec.Item.String(), Instance: instance},
		Policy: domain.PushRequest{
			Kind:      domain.PushAnt,
			Allowed:   rec.Branch,
			Base:      rec.BaseBranch,
			Protected: ws.Settings.ProtectedBranches,
		},
	})
	if err != nil {
		return 0, fmt.Errorf("pushing %s: %w", rec.Branch, err)
	}
	return len(commits), nil
}

// finishSnapshot fills in a finishing ant's finish and, for a complete
// finish, whether its PR is a draft that an earlier run forced (spec
// ant-execution, "Draft completed by a later run"). runs ends with the
// finishing run, which does not count: its own outcome is the finish.
func (s *AntSupervisor) finishSnapshot(ctx context.Context, rec ports.AntRecord, runs []ports.RunRecord, snap *domain.AntSnapshot) error {
	f := *rec.Finish
	snap.Finish = &f
	if snap.PR == nil || f.Status != domain.FinishComplete {
		return nil
	}
	if len(runs) > 0 {
		runs = runs[:len(runs)-1]
	}
	snap.PR.DraftForced = draftForced(runs)
	if !snap.PR.DraftForced {
		return nil
	}
	host, err := s.deps.CodeHost(ctx, snap.PR.Ref.Repo)
	if err != nil {
		return err
	}
	pr, _, err := host.PullRequest(ctx, snap.PR.Ref)
	if err != nil {
		return fmt.Errorf("reading %s: %w", snap.PR.Ref, err)
	}
	snap.PR.Draft = pr.Draft
	return nil
}

// draftForced reports whether the ant PR must stay a draft after these
// runs: one ended partial or timed out, and no later run finished
// complete (spec ant-execution, "Draft PRs for incomplete work").
func draftForced(runs []ports.RunRecord) bool {
	forced := false
	for _, r := range runs {
		switch r.Outcome {
		case ports.OutcomePartial, ports.OutcomeTimedOut:
			forced = true
		case ports.OutcomeComplete:
			forced = false
		}
	}
	return forced
}
