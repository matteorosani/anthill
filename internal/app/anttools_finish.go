package app

import (
	"context"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
)

// finishArgs are the arguments of finish.
type finishArgs struct {
	Status     domain.FinishStatus `json:"status"`
	Summary    string              `json:"summary"`
	OpenPoints []string            `json:"open_points"`
}

// acceptedResult is the result of the tools that end a run or a round's
// agent.
type acceptedResult struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// MessageFinishAccepted is what finish returns.
const MessageFinishAccepted = "finish accepted: the run ends now"

// finish checks the status against the branch's new commits on a task
// run, moves the ant to finishing, and hands the finish to ant
// supervision, which ends the run and acts on its outcome (spec
// ant-tools, "Finish tool").
func (t *AntTools) finish(ctx context.Context, c *toolCall) (any, error) {
	var args finishArgs
	if err := decodeArgs(c.args, &args); err != nil {
		return nil, err
	}
	rec := c.ant
	ws, err := t.deps.Workspace(ctx, rec.Item)
	if err != nil {
		return nil, err
	}
	commits, err := t.deps.Git.NewCommits(ctx, ws.Clone, rec.Worktree, rec.BaseBranch)
	if err != nil {
		return nil, fmt.Errorf("counting the new commits: %w", err)
	}
	if err := domain.CheckFinish(c.session.Role, args.Status, len(commits)); err != nil {
		return nil, err
	}
	ant, err := restoreAnt(rec)
	if err != nil {
		return nil, err
	}
	if err := toState(ant, domain.AntFinishing, t.clock.Now(), "finish: "+string(args.Status)); err != nil {
		return nil, err
	}
	end := context.WithoutCancel(ctx)
	if err := t.store.SaveAnt(end, withAnt(rec, ant)); err != nil {
		return nil, err
	}
	points := make([]string, len(args.OpenPoints))
	for i, p := range args.OpenPoints {
		points[i] = t.safe(c, p)
	}
	err = t.deps.Finishes.Finish(end, FinishRequest{
		Ant:        c.session.Ant,
		Run:        c.session.Run,
		Role:       c.session.Role,
		Status:     args.Status,
		Summary:    t.summary(c, args.Summary),
		OpenPoints: points,
		NewCommits: len(commits),
	})
	if err != nil {
		return nil, fmt.Errorf("handing over the finish: %w", err)
	}
	return acceptedResult{Status: "accepted", Message: MessageFinishAccepted}, nil
}

// reviewArgs are the arguments of submit_review.
type reviewArgs struct {
	Summary  string `json:"summary"`
	Findings []struct {
		File           string `json:"file"`
		StartLine      int    `json:"start_line"`
		EndLine        int    `json:"end_line"`
		Severity       string `json:"severity"`
		Classification string `json:"classification"`
		Message        string `json:"message"`
		SuggestedFix   string `json:"suggested_fix"`
	} `json:"findings"`
}

// MessageAlreadyReported refuses a second submit_review or finish_fixes
// from the same agent.
const MessageAlreadyReported = "already reported: this agent's result was handed over"

// submitReview checks the findings and hands the round's result to the
// shepherd, which posts it (spec ant-tools, "Review tool"). The text is
// made safe to post here.
func (t *AntTools) submitReview(ctx context.Context, c *toolCall) (any, error) {
	var args reviewArgs
	if err := decodeArgs(c.args, &args); err != nil {
		return nil, err
	}
	findings := make([]domain.Finding, len(args.Findings))
	for i, f := range args.Findings {
		findings[i] = domain.Finding{
			Location:       domain.Location{File: f.File, StartLine: f.StartLine, EndLine: f.EndLine},
			Severity:       domain.Severity(f.Severity),
			Classification: domain.Classification(f.Classification),
			Message:        t.safe(c, f.Message),
			SuggestedFix:   t.safe(c, f.SuggestedFix),
		}
		if err := findings[i].Validate(); err != nil {
			return nil, domain.RefuseTool("findings[%d]: %s", i, err)
		}
	}
	report := ReviewReport{PR: c.session.PR, Round: c.session.Round, Summary: t.summary(c, args.Summary), Findings: findings}
	if err := t.report(c, func() error { return t.deps.Reviews.SubmitReview(context.WithoutCancel(ctx), report) }); err != nil {
		return nil, err
	}
	return acceptedResult{Status: "submitted", Message: fmt.Sprintf("review submitted with %d findings", len(findings))}, nil
}

// fixArgs are the arguments of finish_fixes.
type fixArgs struct {
	Results []struct {
		Finding string `json:"finding"`
		Status  string `json:"status"`
		Commit  string `json:"commit"`
		Reason  string `json:"reason"`
	} `json:"results"`
}

// finishFixes checks the fix agent's results against the findings it
// was given and the commits of its pass, then hands them to the
// shepherd, which verifies, pushes and replies (spec ant-tools, "Fix
// report tool").
func (t *AntTools) finishFixes(ctx context.Context, c *toolCall) (any, error) {
	var args fixArgs
	if err := decodeArgs(c.args, &args); err != nil {
		return nil, err
	}
	s := c.session
	ws, err := t.deps.Workspace(ctx, s.PR)
	if err != nil {
		return nil, err
	}
	commits, err := t.deps.Git.NewCommits(ctx, ws.Clone, s.Worktree, s.Branch)
	if err != nil {
		return nil, fmt.Errorf("listing the fix commits: %w", err)
	}
	hashes := make([]string, len(commits))
	for i, cm := range commits {
		hashes[i] = cm.Hash
	}
	given := make([]domain.FixResult, len(args.Results))
	for i, r := range args.Results {
		given[i] = domain.FixResult{Finding: r.Finding, Fixed: r.Status == "fixed", Commit: r.Commit, Reason: t.safe(c, r.Reason)}
	}
	results, err := domain.ResolveFixResults(s.Findings, given, hashes)
	if err != nil {
		return nil, err
	}
	report := FixReport{PR: s.PR, Round: s.Round, Results: results}
	if err := t.report(c, func() error { return t.deps.Fixes.FinishFixes(context.WithoutCancel(ctx), report) }); err != nil {
		return nil, err
	}
	fixed := 0
	for _, r := range results {
		if r.Fixed {
			fixed++
		}
	}
	return acceptedResult{Status: "submitted", Message: fmt.Sprintf("%d fixed and %d skipped: Anthill verifies the fixes before pushing", fixed, len(results)-fixed)}, nil
}

// report hands a review or fix agent's result over once.
func (t *AntTools) report(c *toolCall, handOver func() error) error {
	t.mu.Lock()
	if t.done[c.key] {
		t.mu.Unlock()
		return domain.RefuseTool("%s", MessageAlreadyReported)
	}
	t.done[c.key] = true
	t.mu.Unlock()
	if err := handOver(); err != nil {
		t.mu.Lock()
		delete(t.done, c.key)
		t.mu.Unlock()
		return err
	}
	return nil
}
