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

// FixPassRunner runs one fix pass (spec pr-shepherd, "Fix agent pass",
// "Anthill verifies, pushes and replies"). The shepherd calls Run from a
// goroutine it owns, with the round's review claim held, and asks the
// playbook again when it returns. Its ctx ends when the PR is merged or
// closed: the pass then stops its agent and pushes nothing.
type FixPassRunner interface {
	Run(ctx context.Context, f FixStart) (FixOutcome, error)
}

// FixStart is what a fix pass needs.
type FixStart struct {
	// PR is the PR as read before the pass; its HeadCommit is Pass.Head.
	PR   domain.PullRequest
	Pass domain.StartFixPass
	// Round is this instance's round on the head commit, or 0 when
	// another instance ran it.
	Round ports.RoundID
	// Workspace is the developer's clone, the repository and its
	// effective settings, and Runner the review runner.
	Workspace ToolWorkspace
	Runner    domain.Runner
	// Instance is this instance's id, for markers and trailers.
	Instance string
}

// FixOutcome is how a fix pass ended.
type FixOutcome struct {
	// Pushed is the head commit the pass pushed, or "" when it pushed
	// nothing.
	Pushed string
	// Fixed and Reclassified count the findings resolved and those left
	// to developers.
	Fixed, Reclassified int
	// Failure says why a pass pushed nothing, or is "".
	Failure domain.FixFailure
	// Usage is what the fix agent's runner reported.
	Usage ports.Usage
}

// FixTokens issues and revokes the tool token of a fix agent; AntTools
// implements it.
type FixTokens interface {
	Issue(s ToolSession) (string, error)
	Attach(token string, ch RunChannel)
	Revoke(token string)
}

// FixPassDeps are the ports and services a fix pass uses.
type FixPassDeps struct {
	Git      ports.SourceControl
	Executor ports.Executor
	Tokens   FixTokens
	Verifier ports.Verifier
	CodeHost CodeHostRoute
	Clock    ports.Clock
	// Telemetry switches on the fix agent's own telemetry when export is
	// on (spec observability). Optional.
	Telemetry *Telemetry
}

// FixPassOptions are the settings of fix passes.
type FixPassOptions struct {
	// Root is the worktree root, where each pass has its temporary
	// worktree.
	Root string
	// MCPCommand returns the command line of the ant tools server for a
	// tool token (design D5).
	MCPCommand func(token string) []string
	// Environ is Anthill's own environment, from which the agent's is
	// made (domain.AgentEnv).
	Environ []string
}

// FixPass runs fix passes and takes the fix agents' finish_fixes (it is
// the ant tools' FixSink). A pass:
//  1. checks out the reviewed head commit in a temporary detached
//     worktree, whose commits carry the pass's trailers;
//  2. starts the fix agent through the executor with the findings, each
//     by its thread, and waits for it to end, at most
//     domain.FixAgentMinutes; the agent commits and reports with
//     finish_fixes, and never pushes or comments;
//  3. runs the verification command on the result;
//  4. pushes the commits to the PR branch, never with force, only while
//     the branch is still at the reviewed head;
//  5. replies on each fixed finding's thread with its commit link and
//     resolves it, and on each skipped one with the agent's reason,
//     reclassifying it needs-developer.
//
// The fallbacks are all or nothing (spec pr-shepherd, "Failed auto-fix
// falls back to developer"): when the agent does not report, the
// verification fails or the push is rejected, no commit is pushed, and
// every finding of the pass is reclassified needs-developer with why on
// its thread. The worktree is removed in every case.
type FixPass struct {
	deps FixPassDeps
	opts FixPassOptions

	mu sync.Mutex
	// reports takes the finish_fixes of the pass in progress on a PR.
	reports map[domain.Ref]chan FixReport
}

var (
	_ FixPassRunner = (*FixPass)(nil)
	_ FixSink       = (*FixPass)(nil)
	_ FixTokens     = (*AntTools)(nil)
)

// NewFixPass returns the fix pass service.
func NewFixPass(deps FixPassDeps, opts FixPassOptions) *FixPass {
	return &FixPass{deps: deps, opts: opts, reports: map[domain.Ref]chan FixReport{}}
}

// ErrNoFixPass is returned by FinishFixes when no fix pass runs on the
// PR on this instance.
var ErrNoFixPass = errors.New("no fix pass in progress on this PR")

// FinishFixes takes a fix agent's results (FixSink). The ant tools have
// checked them against the findings and the pass's commits.
func (p *FixPass) FinishFixes(_ context.Context, r FixReport) error {
	p.mu.Lock()
	ch, ok := p.reports[r.PR]
	p.mu.Unlock()
	if !ok {
		return ErrNoFixPass
	}
	select {
	case ch <- r:
		return nil
	default:
		return domain.RefuseTool("%s", MessageAlreadyReported)
	}
}

// FixWorktreeBranch is the name under the worktree root of the temporary
// worktree of a fix pass on PR n at head.
func FixWorktreeBranch(n int, head string) string {
	if len(head) > 12 {
		head = head[:12]
	}
	return fmt.Sprintf("anthill-fix/pr-%d-%s", n, head)
}

// Run runs a fix pass (FixPassRunner).
func (p *FixPass) Run(ctx context.Context, f FixStart) (FixOutcome, error) {
	pr := f.PR
	clone := f.Workspace.Clone
	path := domain.WorktreePath(p.opts.Root, f.Workspace.Repo, FixWorktreeBranch(pr.Ref.Number, f.Pass.Head))
	if err := p.deps.Git.AddDetachedWorktree(ctx, clone, path, f.Pass.Head); err != nil {
		return FixOutcome{}, fmt.Errorf("checking out %s for a fix pass: %w", f.Pass.Head, err)
	}
	defer func() {
		if err := p.deps.Git.RemoveWorktree(context.WithoutCancel(ctx), clone, path, true); err != nil && !errors.Is(err, ports.ErrNotFound) {
			_ = err // The next pass on the same head fails to check out and says why.
		}
	}()
	trailers := domain.Trailers{Item: pr.Ref.String(), Agent: string(f.Runner), Instance: f.Instance}
	if err := p.deps.Git.SetTrailers(ctx, clone, path, trailers); err != nil {
		return FixOutcome{}, err
	}

	report, usage, err := p.runAgent(ctx, f, path)
	if err != nil {
		return FixOutcome{Usage: usage}, err
	}
	out, err := p.settle(ctx, f, path, report)
	out.Usage = usage
	return out, err
}

// runAgent starts the fix agent and returns its report once its process
// has ended, or nil when it ended without one.
func (p *FixPass) runAgent(ctx context.Context, f FixStart, path string) (*FixReport, ports.Usage, error) {
	pr := f.PR.Ref
	ch := make(chan FixReport, 1)
	p.mu.Lock()
	if _, busy := p.reports[pr]; busy {
		p.mu.Unlock()
		return nil, ports.Usage{}, fmt.Errorf("a fix pass already runs on %s", pr)
	}
	p.reports[pr] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.reports, pr)
		p.mu.Unlock()
	}()

	token, err := p.deps.Tokens.Issue(ToolSession{
		Role: domain.RoleFixAgent, PR: pr, Round: f.Round,
		Worktree: path, Branch: f.Pass.Head, Findings: f.Pass.Threads,
	})
	if err != nil {
		return nil, ports.Usage{}, err
	}
	defer p.deps.Tokens.Revoke(token)
	var mcp []string
	if p.opts.MCPCommand != nil {
		mcp = p.opts.MCPCommand(token)
	}
	budget := domain.FixAgentMinutes * time.Minute
	exec, err := p.deps.Executor.Start(ctx, ports.ExecSpec{
		Runner: f.Runner,
		Run: ports.RunSpec{
			Worktree:   path,
			Brief:      domain.FixBrief(f.PR, f.Pass.Findings, f.Pass.Threads),
			Budget:     budget,
			MCPCommand: mcp,
			ToolToken:  token,
			Env:        domain.AgentEnv(p.opts.Environ),
			Telemetry:  p.deps.Telemetry.PRAgentTelemetry(ctx, pr, f.Instance),
		},
		Deadline: p.deps.Clock.Now().Add(budget),
	})
	if err != nil {
		return nil, ports.Usage{}, fmt.Errorf("starting the fix agent on %s: %w", pr, err)
	}
	p.deps.Tokens.Attach(token, RunChannel{Capabilities: exec.Capabilities, Exec: exec.ID})

	var usage ports.Usage
	stopped := false
	for {
		select {
		case ev, open := <-exec.Events:
			if !open {
				return p.report(ch), usage, ctx.Err()
			}
			if ev.Kind == ports.ExecUsage || ev.Final() {
				usage = ev.Usage
			}
		case <-ctx.Done():
			if !stopped {
				stopped = true
				// The PR ended: stop the agent and push nothing.
				go func() { _ = p.deps.Executor.Stop(exec.ID, StopGrace) }()
			}
		}
	}
}

// report returns the agent's report, if it gave one.
func (p *FixPass) report(ch chan FixReport) *FixReport {
	select {
	case r := <-ch:
		return &r
	default:
		return nil
	}
}

// settle verifies, pushes and replies after the agent, or falls back to
// developers.
func (p *FixPass) settle(ctx context.Context, f FixStart, path string, report *FixReport) (FixOutcome, error) {
	host, err := p.deps.CodeHost(ctx, f.PR.Ref.Repo)
	if err != nil {
		return FixOutcome{}, err
	}
	r := fixReplies{host: host, pr: f.PR, instance: f.Instance}
	if report == nil {
		return r.fail(ctx, f.Pass.Threads, domain.FixNotReported, "")
	}
	var fixed, skipped []domain.FixResult
	for _, res := range report.Results {
		if res.Fixed {
			fixed = append(fixed, res)
		} else {
			skipped = append(skipped, res)
		}
	}
	out := FixOutcome{}
	if len(fixed) > 0 {
		pushed, failure, detail, err := p.verifyAndPush(ctx, f, path)
		if err != nil {
			return FixOutcome{}, err
		}
		if failure != "" {
			return r.fail(ctx, f.Pass.Threads, failure, detail)
		}
		out.Pushed = pushed
		for _, res := range fixed {
			if err := r.fixed(ctx, res.Finding, res.Commit); err != nil {
				return out, err
			}
			out.Fixed++
		}
	}
	for _, res := range skipped {
		if err := r.reclassify(ctx, res.Finding, domain.SkippedText(res.Reason)); err != nil {
			return out, err
		}
		out.Reclassified++
	}
	return out, nil
}

// verifyAndPush runs the verification command and pushes the pass's
// commits. A failure says why nothing was pushed; an error is a fault.
func (p *FixPass) verifyAndPush(ctx context.Context, f FixStart, path string) (pushed string, failure domain.FixFailure, detail string, err error) {
	settings := f.Workspace.Settings
	v, err := p.deps.Verifier.Verify(ctx, path, settings.VerificationCommand, domain.VerificationMinutes*time.Minute)
	switch {
	case err != nil:
		return "", "", "", fmt.Errorf("running the verification command on %s: %w", f.PR.Ref, err)
	case v.TimedOut:
		return "", domain.FixVerifyTimedOut, fmt.Sprintf("after %d minutes", domain.VerificationMinutes), nil
	case !v.Passed():
		return "", domain.FixVerifyFailed, fmt.Sprintf("exit code %d", v.Exit.Code), nil
	}
	commits, err := p.deps.Git.NewCommits(ctx, f.Workspace.Clone, path, f.Pass.Head)
	if err != nil || len(commits) == 0 {
		return "", domain.FixDidNotApply, "", nil //nolint:nilerr // Commits that cannot be read are not pushed.
	}
	head := commits[len(commits)-1].Hash
	target := ports.PushTarget{Branch: f.PR.SourceBranch, Expect: f.Pass.Head}
	if f.PR.Fork {
		target.Repo, target.URL = f.PR.ForkRepo, f.PR.ForkURL
	} else {
		def, err := p.deps.Git.RemoteDefaultBranch(ctx, f.Workspace.Clone)
		if err != nil {
			return "", "", "", fmt.Errorf("finding the remote's default branch: %w", err)
		}
		policy := domain.PushRequest{
			Kind: domain.PushAutoFix, Branch: f.PR.SourceBranch, Allowed: f.PR.SourceBranch, OptedIn: true,
			Base: f.PR.TargetBranch, Default: def, Protected: settings.ProtectedBranches,
		}
		if err := domain.CheckPush(policy); err != nil {
			return "", domain.FixPushRefused, "", nil
		}
	}
	// The record that this head is a fix pass's push goes first, so that
	// no instance starts a round on it, even after a crash right after
	// the push.
	host, err := p.deps.CodeHost(ctx, f.PR.Ref.Repo)
	if err != nil {
		return "", "", "", err
	}
	if _, err := host.AddComment(ctx, f.PR.Ref, domain.AnthillComment{
		Role: domain.RoleShepherd,
		Body: fmt.Sprintf("Pushed automatic fixes as `%s`, verified with the repository's verification command.", short(head)),
		Marker: domain.Marker{
			Version: domain.MarkerVersion, ID: AutofixMarkerPrefix + head, Instance: f.Instance,
			Kind: domain.CommentStatus, Ref: f.PR.Ref.String(),
		},
	}); err != nil {
		return "", "", "", fmt.Errorf("recording the fix pass push on %s: %w", f.PR.Ref, err)
	}
	if _, err := p.deps.Git.PushTo(ctx, f.Workspace.Clone, path, target); err != nil {
		if errors.Is(err, ports.ErrConflict) {
			return "", domain.FixBranchMoved, "", nil
		}
		// Any other refusal, such as by the repository's pre-push hook,
		// also leaves the findings to developers.
		// Its message is not posted: it could name local paths.
		return "", domain.FixPushRefused, "", nil
	}
	return head, "", "", nil
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
