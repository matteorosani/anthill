package app_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

const fixClone = "/clones/shop"

// fixSink hands finish_fixes to the fix passes, which are made after the
// ant tools.
type fixSink struct{ fixes **app.FixPass }

func (s fixSink) FinishFixes(ctx context.Context, r app.FixReport) error {
	return (*s.fixes).FinishFixes(ctx, r)
}

// fixEnv runs fix passes with real ant tools, a fake executor whose fix
// agent the test plays, a fake verifier and a fake GitHub. PR 34 is a
// human PR from feature/login with two auto-fixable findings, each in
// its thread.
type fixEnv struct {
	t        *testing.T
	ctx      context.Context
	clock    *testkit.Clock
	gh       *testkit.Provider
	git      *testkit.SourceControl
	clone    ports.Clone
	exec     *testkit.Executor
	verifier *testkit.Verifier
	tools    *app.AntTools
	fixes    *app.FixPass
	settings domain.Effective
	pr       domain.Ref
	head     string
	threads  []string
}

var (
	unusedImportFinding = domain.Finding{
		Location: domain.Location{File: "src/auth.ts", StartLine: 3}, Severity: domain.SeverityNit,
		Classification: domain.AutoFixable, Message: "unused import", SuggestedFix: "remove it",
	}
	unusedVarFinding = domain.Finding{
		Location: domain.Location{File: "src/auth.ts", StartLine: 9}, Severity: domain.SeverityNit,
		Classification: domain.AutoFixable, Message: "unused variable",
	}
)

func newFixEnv(t *testing.T) *fixEnv {
	t.Helper()
	e := &fixEnv{t: t, ctx: context.Background(), clock: testkit.NewClock(t0)}
	e.gh = testkit.NewProvider(e.clock)
	e.gh.AddWriter(shop, "mrosani", "lbianchi")
	e.git = testkit.NewSourceControl()
	e.clone = e.git.AddClone(fixClone, shop, "main")
	e.exec = testkit.NewExecutor(nil)
	e.verifier = &testkit.Verifier{}
	e.settings = domain.Defaults()
	e.settings.VerificationCommand = "go test ./..."
	client := e.gh.Client("mrosani")
	codeHost := func(context.Context, domain.Repository) (ports.CodeHostPort, error) { return client, nil }
	route := func(context.Context, domain.Ref) (app.ToolWorkspace, error) {
		return app.ToolWorkspace{Clone: e.clone, Repo: shop, Settings: e.settings}, nil
	}
	e.tools = app.NewAntTools(app.AntToolsDeps{
		Store: testkit.NewStateStore(), Claims: reviewClaims{}, Clock: e.clock, Executor: e.exec,
		Workspace: route, Git: e.git, CodeHost: codeHost, Fixes: fixSink{&e.fixes},
	}, app.AntToolsOptions{})
	e.fixes = app.NewFixPass(app.FixPassDeps{
		Git: e.git, Executor: e.exec, Tokens: e.tools, Verifier: e.verifier, CodeHost: codeHost, Clock: e.clock,
	}, app.FixPassOptions{
		Root:       "/worktrees",
		MCPCommand: func(token string) []string { return []string{"anthill", "mcp", token} },
	})

	e.head = e.git.SetRemoteBranch(fixClone, "feature/login", domain.BranchOwner{}, "Human work").Hash
	e.pr = domain.Ref{Repo: shop, Kind: domain.KindPR, Number: 34}
	e.gh.PutPullRequest(domain.PullRequest{
		Ref: e.pr, Title: "Fix login", SourceBranch: "feature/login", TargetBranch: "main", Author: "ada",
		HeadCommit: e.head, URL: "https://github.com/acme/shop/pull/34", Labels: []string{domain.LabelAutofix},
	})
	for _, f := range []domain.Finding{unusedImportFinding, unusedVarFinding} {
		c, err := client.AddReviewComment(e.ctx, e.pr, ports.ReviewComment{Path: f.File, StartLine: f.StartLine, Comment: domain.AnthillComment{
			Role: domain.RoleShepherd, Body: f.Message,
			Marker: domain.Marker{Version: domain.MarkerVersion, Instance: instanceA, Kind: domain.CommentFinding, Ref: e.pr.String(), FindingFields: f.MarkerFields()},
		}})
		if err != nil {
			t.Fatal(err)
		}
		e.threads = append(e.threads, "thread-"+c.ID)
	}
	return e
}

// run runs a fix pass on both findings while agent plays the fix agent,
// and returns how it ended.
func (e *fixEnv) run(ctx context.Context, agent func(x *testkit.FakeExecution)) (app.FixOutcome, error) {
	e.t.Helper()
	p, _ := e.gh.PullRequest(e.pr)
	start := app.FixStart{
		PR:        p,
		Pass:      domain.StartFixPass{Head: e.head, Findings: []domain.Finding{unusedImportFinding, unusedVarFinding}, Threads: e.threads},
		Round:     1,
		Workspace: app.ToolWorkspace{Clone: e.clone, Repo: shop, Settings: e.settings},
		Runner:    domain.RunnerClaudeCode,
		Instance:  instanceA,
	}
	type outcome struct {
		out app.FixOutcome
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		out, err := e.fixes.Run(ctx, start)
		done <- outcome{out, err}
	}()
	synctest.Wait()
	if execs := e.exec.Executions(); len(execs) > 0 && agent != nil {
		agent(execs[len(execs)-1])
	}
	synctest.Wait()
	o := <-done
	return o.out, o.err
}

// report calls finish_fixes as the agent of x, then ends its process as
// Anthill's stop does.
func (e *fixEnv) report(x *testkit.FakeExecution, results ...map[string]string) app.ToolResult {
	e.t.Helper()
	raw, _ := json.Marshal(map[string]any{"results": results})
	res := e.tools.Call(e.ctx, x.Spec.Run.ToolToken, domain.ToolFinishFixes, raw)
	synctest.Wait()
	if !x.Ended() {
		x.End(ports.ExecExited, ports.Exit{})
	}
	return res
}

func fixed(thread, commit string) map[string]string {
	return map[string]string{"finding": thread, "status": "fixed", "commit": commit}
}

func skipped(thread, reason string) map[string]string {
	return map[string]string{"finding": thread, "status": "skipped", "reason": reason}
}

// thread returns a thread of the PR.
func (e *fixEnv) thread(id string) domain.ReviewThread {
	e.t.Helper()
	p, _ := e.gh.PullRequest(e.pr)
	for _, th := range p.Threads {
		if th.ID == id {
			return th
		}
	}
	e.t.Fatalf("no thread %s", id)
	return domain.ReviewThread{}
}

// lastReply is the text of the thread's last comment.
func (e *fixEnv) lastReply(id string) string {
	th := e.thread(id)
	return th.Comments[len(th.Comments)-1].Body
}

// remote is the PR branch's commits on the remote.
func (e *fixEnv) remote() []string {
	var out []string
	for _, c := range e.git.RemoteCommits(fixClone, "feature/login") {
		out = append(out, c.Hash)
	}
	return out
}

// fixHeads are the heads recorded by fix pass status comments.
func (e *fixEnv) fixHeads() []string {
	p, _ := e.gh.PullRequest(e.pr)
	var out []string
	for _, c := range p.Comments {
		if m, ok := domain.FindMarker(c.Body); ok && m.Kind == domain.CommentStatus && strings.HasPrefix(m.ID, app.AutofixMarkerPrefix) {
			out = append(out, strings.TrimPrefix(m.ID, app.AutofixMarkerPrefix))
		}
	}
	return out
}

// wantNothingPushed checks the fallback of every failure: the branch is
// as the author left it, and each thread says the fix needs a developer
// and stays open.
func (e *fixEnv) wantNothingPushed(remote []string, why domain.FixFailure) {
	e.t.Helper()
	if got := e.remote(); !slices.Equal(got, remote) {
		e.t.Errorf("remote branch = %v, want %v", got, remote)
	}
	for _, id := range e.threads {
		th := e.thread(id)
		last := th.Comments[len(th.Comments)-1]
		if !domain.Reclassified(last, id) || !strings.Contains(last.Body, string(why)) || !strings.Contains(last.Body, "needs a developer") || th.Resolved {
			e.t.Errorf("thread %s: resolved %v, last reply %q", id, th.Resolved, last.Body)
		}
	}
	if w := e.git.Worktrees(fixClone); len(w) != 0 {
		e.t.Errorf("worktrees left: %v", w)
	}
}

// Spec pr-shepherd, "Fix agent makes two fixes" and "Verified fixes":
// the fix agent works in a detached worktree of the reviewed head with
// finish_fixes only, makes one commit per finding, and Anthill verifies,
// pushes both commits, and answers and resolves each thread with its
// commit link.
func TestFixPassVerifiedFixes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newFixEnv(t)
		var commits []string
		out, err := e.run(e.ctx, func(x *testkit.FakeExecution) {
			spec := x.Spec
			path := domain.WorktreePath("/worktrees", shop, app.FixWorktreeBranch(34, e.head))
			if spec.Run.Worktree != path || e.git.WorktreeCommit(path) != e.head || spec.Runner != domain.RunnerClaudeCode ||
				!spec.Deadline.Equal(t0.Add(domain.FixAgentMinutes*time.Minute)) || spec.Session != "" {
				t.Errorf("fix agent spec = %+v", spec)
			}
			for _, want := range []string{"id " + e.threads[0], "id " + e.threads[1], "unused import", "finish_fixes"} {
				if !strings.Contains(spec.Run.Brief, want) {
					t.Errorf("brief lacks %q: %s", want, spec.Run.Brief)
				}
			}
			defs, err := e.tools.Tools(spec.Run.ToolToken)
			if err != nil || len(defs) != 1 || defs[0].Name != domain.ToolFinishFixes {
				t.Errorf("fix agent tools = %+v, %v", defs, err)
			}
			if tr := e.git.Trailers(path); tr.Item != e.pr.String() || tr.Instance != instanceA || tr.Agent != string(domain.RunnerClaudeCode) {
				t.Errorf("trailers = %+v", tr)
			}
			commits = append(commits, e.git.Commit(path, "Remove the unused import").Hash, e.git.Commit(path, "Remove the unused variable").Hash)
			if r := e.report(x, fixed(e.threads[0], commits[0]), fixed(e.threads[1], commits[1])); r.IsError {
				t.Errorf("finish_fixes = %q", r.Text)
			}
		})
		if err != nil || out.Pushed != commits[1] || out.Fixed != 2 || out.Reclassified != 0 || out.Failure != "" {
			t.Fatalf("Run() = %+v, %v", out, err)
		}
		if got := e.remote(); len(got) < 2 || !slices.Equal(got[len(got)-2:], commits) {
			t.Errorf("remote branch = %v, want the two fixes on top", got)
		}
		if calls := e.verifier.Calls(); len(calls) != 1 || calls[0].Command != "go test ./..." || calls[0].Timeout != domain.VerificationMinutes*time.Minute {
			t.Errorf("verification = %+v", calls)
		}
		for i, id := range e.threads {
			th := e.thread(id)
			link := "https://github.com/acme/shop/pull/34/commits/" + commits[i]
			if !th.Resolved || !strings.Contains(e.lastReply(id), link) {
				t.Errorf("thread %s: resolved %v, reply %q", id, th.Resolved, e.lastReply(id))
			}
		}
		// The push is recorded, so that it starts no round.
		if got := e.fixHeads(); !slices.Equal(got, []string{commits[1]}) {
			t.Errorf("fix heads = %v", got)
		}
		if w := e.git.Worktrees(fixClone); len(w) != 0 {
			t.Errorf("worktrees left: %v", w)
		}
	})
}

// Spec ant-tools, "One fix skipped": the fixed finding is pushed and
// resolved, and the skipped one is reclassified needs-developer with the
// agent's reason.
func TestFixPassOneFixSkipped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newFixEnv(t)
		out, err := e.run(e.ctx, func(x *testkit.FakeExecution) {
			c := e.git.Commit(x.Spec.Run.Worktree, "Remove the unused import")
			e.report(x, fixed(e.threads[0], c.Hash), skipped(e.threads[1], "needs an API decision"))
		})
		if err != nil || out.Fixed != 1 || out.Reclassified != 1 || out.Pushed == "" {
			t.Fatalf("Run() = %+v, %v", out, err)
		}
		if !e.thread(e.threads[0]).Resolved {
			t.Error("the fixed thread is not resolved")
		}
		th := e.thread(e.threads[1])
		last := th.Comments[len(th.Comments)-1]
		if th.Resolved || !domain.Reclassified(last, e.threads[1]) || !strings.Contains(last.Body, "needs an API decision") {
			t.Errorf("skipped thread: resolved %v, reply %q", th.Resolved, last.Body)
		}
	})
}

// Spec pr-shepherd, "Fix breaks the build": nothing is pushed, and each
// thread says the automatic fix failed and needs a developer.
func TestFixPassVerificationFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newFixEnv(t)
		e.verifier.Set(ports.Verification{Exit: ports.Exit{Code: 2}}, nil)
		before := e.remote()
		out, err := e.run(e.ctx, func(x *testkit.FakeExecution) {
			c1 := e.git.Commit(x.Spec.Run.Worktree, "Remove the unused import")
			c2 := e.git.Commit(x.Spec.Run.Worktree, "Remove the unused variable")
			e.report(x, fixed(e.threads[0], c1.Hash), fixed(e.threads[1], c2.Hash))
		})
		if err != nil || out.Failure != domain.FixVerifyFailed || out.Pushed != "" || out.Reclassified != 2 {
			t.Fatalf("Run() = %+v, %v", out, err)
		}
		e.wantNothingPushed(before, domain.FixVerifyFailed)
		if !strings.Contains(e.lastReply(e.threads[0]), "exit code 2") {
			t.Errorf("reply = %q", e.lastReply(e.threads[0]))
		}
		if got := e.fixHeads(); len(got) != 0 {
			t.Errorf("fix heads = %v", got)
		}
	})
}

// A verification command that runs out of time fails the pass too.
func TestFixPassVerificationTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newFixEnv(t)
		e.verifier.Set(ports.Verification{TimedOut: true, Exit: ports.Exit{Code: -1, Crashed: true}}, nil)
		before := e.remote()
		out, err := e.run(e.ctx, func(x *testkit.FakeExecution) {
			c := e.git.Commit(x.Spec.Run.Worktree, "Remove the unused import")
			e.report(x, fixed(e.threads[0], c.Hash), skipped(e.threads[1], "not sure"))
		})
		if err != nil || out.Failure != domain.FixVerifyTimedOut {
			t.Fatalf("Run() = %+v, %v", out, err)
		}
		e.wantNothingPushed(before, domain.FixVerifyTimedOut)
	})
}

// Spec pr-shepherd, "Author pushed during the fix": Anthill's push is
// rejected, the fix commits are discarded, and the threads say the fix
// needs a developer.
func TestFixPassAuthorPushedDuringTheFix(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newFixEnv(t)
		var theirs []string
		out, err := e.run(e.ctx, func(x *testkit.FakeExecution) {
			c := e.git.Commit(x.Spec.Run.Worktree, "Remove the unused import")
			e.git.SetRemoteBranch(fixClone, "feature/login", domain.BranchOwner{}, "Human work", "Author's push")
			theirs = e.remote()
			e.report(x, fixed(e.threads[0], c.Hash), skipped(e.threads[1], "not sure"))
		})
		if err != nil || out.Failure != domain.FixBranchMoved || out.Pushed != "" {
			t.Fatalf("Run() = %+v, %v", out, err)
		}
		e.wantNothingPushed(theirs, domain.FixBranchMoved)
	})
}

// A fix agent that ends without finish_fixes pushes nothing.
func TestFixPassWithoutReport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newFixEnv(t)
		before := e.remote()
		out, err := e.run(e.ctx, func(x *testkit.FakeExecution) {
			e.git.Commit(x.Spec.Run.Worktree, "Half a fix")
			x.End(ports.ExecExited, ports.Exit{Code: 1})
		})
		if err != nil || out.Failure != domain.FixNotReported {
			t.Fatalf("Run() = %+v, %v", out, err)
		}
		e.wantNothingPushed(before, domain.FixNotReported)
		if calls := e.verifier.Calls(); len(calls) != 0 {
			t.Errorf("verification ran: %+v", calls)
		}
	})
}

// Task 16.6: a fork that allows edits by maintainers takes the push at
// its own URL.
func TestFixPassPushesToAFork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newFixEnv(t)
		const forkURL = "https://github.com/ada/shop.git"
		fork := domain.Repository{Provider: domain.GitHub, Org: "ada", Name: "shop"}
		e.head = e.git.SetForkBranch(fixClone, forkURL, "feature/login", "Contributor work").Hash
		e.gh.EditPullRequest(e.pr, func(pr *domain.PullRequest) {
			pr.HeadCommit, pr.Fork, pr.ForkRepo, pr.ForkURL, pr.MaintainerCanModify = e.head, true, fork, forkURL, true
		})
		var fix string
		out, err := e.run(e.ctx, func(x *testkit.FakeExecution) {
			fix = e.git.Commit(x.Spec.Run.Worktree, "Remove the unused import").Hash
			e.report(x, fixed(e.threads[0], fix), skipped(e.threads[1], "not sure"))
		})
		if err != nil || out.Pushed != fix {
			t.Fatalf("Run() = %+v, %v", out, err)
		}
		if got := e.git.ForkCommits(forkURL, "feature/login"); len(got) == 0 || got[len(got)-1].Hash != fix {
			t.Errorf("fork branch = %v", got)
		}
	})
}

// Task 16.7: a PR merged during a fix pass stops the agent; nothing is
// pushed or posted.
func TestFixPassStoppedWhenThePREnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newFixEnv(t)
		before := e.remote()
		ctx, cancel := context.WithCancel(e.ctx)
		_, err := e.run(ctx, func(x *testkit.FakeExecution) {
			e.git.Commit(x.Spec.Run.Worktree, "Remove the unused import")
			cancel()
			synctest.Wait()
			if !x.Ended() {
				t.Error("the fix agent was not stopped")
			}
		})
		if err == nil {
			t.Fatal("Run() after the PR ended returned no error")
		}
		if got := e.remote(); !slices.Equal(got, before) {
			t.Errorf("remote branch = %v", got)
		}
		for _, id := range e.threads {
			if n := len(e.thread(id).Comments); n != 1 {
				t.Errorf("thread %s has %d comments", id, n)
			}
		}
		if w := e.git.Worktrees(fixClone); len(w) != 0 {
			t.Errorf("worktrees left: %v", w)
		}
	})
}
