package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// RoundTimeout is how long a round's review agent may take before the
// executor ends it. A round ended so, without a report, posts nothing and
// is tried again on the next trigger.
const RoundTimeout = 30 * time.Minute

// RoundTokens issues the tool token of a round's review agent; AntTools
// implements it.
type RoundTokens interface {
	Issue(s ToolSession) (string, error)
	Attach(token string, ch RunChannel)
	Revoke(token string)
}

var (
	_ RoundTokens = (*AntTools)(nil)
	_ RoundRunner = (*ShepherdRounds)(nil)
	_ ReviewSink  = (*ShepherdRounds)(nil)
)

// ErrNoReview is a round whose review agent ended without calling
// submit_review.
var ErrNoReview = errors.New("the review agent ended without submit_review")

// ShepherdRoundsDeps are the ports and services the rounds use.
type ShepherdRoundsDeps struct {
	// Route finds the clone of a PR's repository, and CodeHost its code
	// host.
	Route    WorkspaceRoute
	CodeHost CodeHostRoute
	Git      ports.SourceControl
	Executor ports.Executor
	Tokens   RoundTokens
	Clock    ports.Clock
}

// ShepherdRoundsOptions are the settings of the rounds.
type ShepherdRoundsOptions struct {
	// Root is the effective worktree root, under which each round has its
	// temporary worktree (domain.RoundWorktreePath).
	Root string
	// MCPCommand returns the command line of the ant tools server for a
	// token, as for runs (design D5).
	MCPCommand func(token string) []string
	// Environ is Anthill's own environment, from which the agent's is
	// made (domain.AgentEnv).
	Environ []string
}

// ShepherdRounds runs shepherd rounds (spec pr-shepherd, "Review rounds",
// "Findings are posted as PR comments", "Findings are not repeated"). It
// is the shepherd's RoundRunner and the ant tools' ReviewSink: a round's
// review agent hands its submit_review to SubmitReview, and Run posts it
// once the agent's process has ended.
type ShepherdRounds struct {
	deps ShepherdRoundsDeps
	opts ShepherdRoundsOptions

	mu sync.Mutex
	// reports holds, by round, the report of each round in progress:
	// nil until its agent calls submit_review.
	reports map[ports.RoundID]*ReviewReport
}

// NewShepherdRounds returns the round runner.
func NewShepherdRounds(deps ShepherdRoundsDeps, opts ShepherdRoundsOptions) *ShepherdRounds {
	return &ShepherdRounds{deps: deps, opts: opts, reports: map[ports.RoundID]*ReviewReport{}}
}

// SubmitReview takes a review agent's submit_review for a round in
// progress. The round posts it once the agent's process has ended.
func (r *ShepherdRounds) SubmitReview(_ context.Context, rep ReviewReport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.reports[rep.Round]
	if !ok {
		return fmt.Errorf("round %d on %s is not in progress", rep.Round, rep.PR)
	}
	if cur != nil {
		return domain.RefuseTool("%s", MessageAlreadyReported)
	}
	r.reports[rep.Round] = &rep
	return nil
}

// Run runs one round: a temporary detached worktree of the head commit,
// a review agent in a new session through the executor, and, once it has
// reported through submit_review and ended, the inline comments and the
// summary comment. The worktree is removed and the token revoked however
// the round ends. A round whose agent gives no report, or whose ctx is
// cancelled, such as when the PR is merged, posts nothing.
func (r *ShepherdRounds) Run(ctx context.Context, start RoundStart) (RoundResult, error) {
	rec := start.Round
	ws, err := r.deps.Route(ctx, rec.PR)
	if err != nil {
		return RoundResult{}, fmt.Errorf("finding the repository of %s: %w", rec.PR, err)
	}
	if err := r.deps.Git.Fetch(ctx, ws.Clone); err != nil {
		return RoundResult{}, fmt.Errorf("fetching: %w", err)
	}
	path := domain.RoundWorktreePath(r.opts.Root, ws.Repo, rec.PR.Number, int64(rec.ID))
	if err := r.deps.Git.AddDetachedWorktree(ctx, ws.Clone, path, rec.Head); err != nil {
		return RoundResult{}, fmt.Errorf("checking out %s: %w", rec.Head, err)
	}
	defer func() {
		// The worktree is temporary; a failure to remove it leaves it to
		// worktree cleanup.
		_ = r.deps.Git.RemoveWorktree(context.WithoutCancel(ctx), ws.Clone, path, true)
	}()

	report, usage, err := r.review(ctx, start, ws, path)
	if err != nil {
		return RoundResult{Usage: usage}, err
	}
	findings, err := r.post(ctx, start, *report)
	return RoundResult{Findings: findings, Usage: usage}, err
}

// review runs the review agent and returns its report.
func (r *ShepherdRounds) review(ctx context.Context, start RoundStart, ws ToolWorkspace, path string) (*ReviewReport, ports.Usage, error) {
	rec := start.Round
	r.mu.Lock()
	r.reports[rec.ID] = nil
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.reports, rec.ID)
		r.mu.Unlock()
	}()
	token, err := r.deps.Tokens.Issue(ToolSession{Role: domain.RoleReviewAgent, PR: rec.PR, Round: rec.ID, Worktree: path})
	if err != nil {
		return nil, ports.Usage{}, err
	}
	defer r.deps.Tokens.Revoke(token)
	var mcp []string
	if r.opts.MCPCommand != nil {
		mcp = r.opts.MCPCommand(token)
	}
	brief := domain.RenderReviewBrief(domain.ReviewBriefInput{
		PR:   start.PR,
		Base: ws.Clone.Remote + "/" + start.PR.TargetBranch,
		Head: rec.Head,
		Open: openFindings(start.Earlier),
	})
	x, err := r.deps.Executor.Start(ctx, ports.ExecSpec{
		Runner: start.Runner,
		Run: ports.RunSpec{
			Worktree:   path,
			Brief:      brief,
			Budget:     RoundTimeout,
			MCPCommand: mcp,
			ToolToken:  token,
			Env:        domain.AgentEnv(r.opts.Environ),
		},
		Deadline: r.deps.Clock.Now().Add(RoundTimeout),
	})
	if err != nil {
		return nil, ports.Usage{}, fmt.Errorf("starting the review agent: %w", err)
	}
	r.deps.Tokens.Attach(token, RunChannel{Capabilities: x.Capabilities, Exec: x.ID})
	last, usage := r.watch(ctx, x)
	r.mu.Lock()
	report := r.reports[rec.ID]
	r.mu.Unlock()
	switch {
	case ctx.Err() != nil:
		return nil, usage, ctx.Err()
	case report != nil:
		return report, usage, nil
	case last.Kind == ports.ExecEndedAtDeadline:
		return nil, usage, fmt.Errorf("%w: ended after %s", ErrNoReview, RoundTimeout)
	}
	return nil, usage, fmt.Errorf("%w: exited with code %d", ErrNoReview, last.Exit.Code)
}

// watch reads the agent's events until its process has ended, and ends
// it when ctx is cancelled. It returns the last event and the usage.
func (r *ShepherdRounds) watch(ctx context.Context, x ports.Execution) (ports.ExecEvent, ports.Usage) {
	var (
		last    ports.ExecEvent
		usage   ports.Usage
		stopped bool
	)
	done := ctx.Done()
	for {
		select {
		case ev, ok := <-x.Events:
			if !ok {
				return last, usage
			}
			// A final event without usage keeps what was reported.
			if ev.Kind == ports.ExecUsage || ev.Final() && (ev.Usage.Tokens != nil || ev.Usage.CostUSD != nil) {
				usage = ev.Usage
			}
			if ev.Final() {
				last = ev
			}
		case <-done:
			done = nil
			if !stopped {
				stopped = true
				// Stop returns once the process has ended; its events
				// still end the channel.
				go func() { _ = r.deps.Executor.Stop(x.ID, StopGrace) }()
			}
		}
	}
}

// openFindings are the findings of earlier rounds still open, once each.
func openFindings(rounds []domain.PRRound) []domain.Finding {
	var out []domain.Finding
	seen := map[string]bool{}
	for _, round := range rounds {
		for _, f := range round.Findings {
			key := f.Fingerprint() + "\x00" + f.Thread
			if f.Resolved || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, f.Finding)
		}
	}
	return out
}

// postedFinding is a finding with a thread, posted by this round or
// earlier, against which later findings are matched.
type postedFinding struct {
	prior   domain.PriorFinding
	comment string
	url     string
}

// post posts the round's findings and its summary. A finding that
// matches an open thread, from an earlier round or this one, is not
// posted again: the summary links to the thread (spec pr-shepherd,
// "Findings are not repeated"). A finding with a file and lines gets an
// inline comment; one without, or whose inline comment the code host
// refuses, such as for lines outside the change, is only in the summary.
func (r *ShepherdRounds) post(ctx context.Context, start RoundStart, rep ReviewReport) ([]domain.Finding, error) {
	rec := start.Round
	host, err := r.deps.CodeHost(ctx, rec.PR.Repo)
	if err != nil {
		return nil, err
	}
	pr, _, err := host.PullRequest(ctx, rec.PR)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rec.PR, err)
	}
	var open []postedFinding
	for _, p := range domain.PriorFindings(pr.Threads) {
		c := threadOpener(pr.Threads, p.ThreadID)
		open = append(open, postedFinding{prior: p, comment: c.ID, url: c.URL})
	}
	antPR := domain.IsAntPR(pr, start.Settings.BranchPrefix)
	var (
		listed []domain.SummaryFinding
		marked []domain.RoundFinding
	)
	for _, f := range rep.Findings {
		if p, ok := matchPosted(f, open); ok {
			listed = append(listed, domain.SummaryFinding{Finding: f, URL: p.url, Repeat: true})
			marked = append(marked, domain.NewRoundFinding(f, p.comment))
			continue
		}
		if !f.Known() || f.StartLine == 0 {
			listed = append(listed, domain.SummaryFinding{Finding: f})
			marked = append(marked, domain.NewRoundFinding(f, ""))
			continue
		}
		c, err := host.AddReviewComment(ctx, rec.PR, ports.ReviewComment{
			Path: f.File, StartLine: f.StartLine, EndLine: f.EndLine,
			Comment: domain.AnthillComment{
				Role: domain.RoleShepherd,
				Body: domain.RenderFindingComment(f),
				Marker: domain.Marker{
					Version: domain.MarkerVersion, Instance: start.Instance, Kind: domain.CommentFinding,
					Ref: rec.PR.String(), FindingFields: f.MarkerFields(),
				},
			},
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			listed = append(listed, domain.SummaryFinding{Finding: f})
			marked = append(marked, domain.NewRoundFinding(f, ""))
			continue
		}
		open = append(open, postedFinding{prior: domain.PriorFinding{FindingFields: *f.MarkerFields()}, comment: c.ID, url: c.URL})
		listed = append(listed, domain.SummaryFinding{Finding: f, URL: c.URL})
		marked = append(marked, domain.NewRoundFinding(f, c.ID))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, err = host.AddComment(ctx, rec.PR, domain.AnthillComment{
		Role: domain.RoleShepherd,
		Body: domain.RenderRoundSummary(domain.RoundSummaryInput{Head: rec.Head, AntPR: antPR, Findings: listed, Assessment: rep.Summary}),
		Marker: domain.Marker{
			Version: domain.MarkerVersion, Instance: start.Instance, Kind: domain.CommentSummary, Ref: rec.PR.String(),
			RoundFields: &domain.RoundFields{Head: rec.Head, Requested: rec.Requested, Findings: marked},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("posting the summary of round %d on %s: %w", rec.ID, rec.PR, err)
	}
	return rep.Findings, nil
}

// matchPosted returns the open thread that f repeats, if any.
func matchPosted(f domain.Finding, open []postedFinding) (postedFinding, bool) {
	for _, p := range open {
		if f.Matches(p.prior) {
			return p, true
		}
	}
	return postedFinding{}, false
}

// threadOpener is the first comment of a review thread.
func threadOpener(threads []domain.ReviewThread, id string) domain.Comment {
	for _, t := range threads {
		if t.ID == id && len(t.Comments) > 0 {
			return t.Comments[0]
		}
	}
	return domain.Comment{}
}
