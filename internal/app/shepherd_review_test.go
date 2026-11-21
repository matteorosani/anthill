package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

const roundClone = "/clones/shop"

// reviewClaims holds the review claim of every PR, as the shepherd does
// while a round runs.
type reviewClaims struct{}

func (reviewClaims) Held(context.Context) ([]ports.HeldClaim, error) {
	return []ports.HeldClaim{{Ref: domain.Ref{Repo: shop, Kind: domain.KindPR, Number: 34}, Kind: domain.ClaimReview, Instance: instanceA}}, nil
}

// reviewSink hands submit_review to the rounds, which are made after the
// ant tools.
type reviewSink struct{ rounds **app.ShepherdRounds }

func (s reviewSink) SubmitReview(ctx context.Context, r app.ReviewReport) error {
	return (*s.rounds).SubmitReview(ctx, r)
}

// roundEnv runs rounds with real ant tools, a fake executor whose agent
// the test plays, and a fake GitHub.
type roundEnv struct {
	t      *testing.T
	ctx    context.Context
	clock  *testkit.Clock
	gh     *testkit.Provider
	git    *testkit.SourceControl
	exec   *testkit.Executor
	tools  *app.AntTools
	rounds *app.ShepherdRounds
	pr     domain.Ref
	nextID ports.RoundID
}

func newRoundEnv(t *testing.T, branch string, opts ...func(*app.ShepherdRoundsDeps)) *roundEnv {
	t.Helper()
	e := &roundEnv{t: t, ctx: context.Background(), clock: testkit.NewClock(t0)}
	e.gh = testkit.NewProvider(e.clock)
	e.gh.AddWriter(shop, "mrosani", "lbianchi")
	e.git = testkit.NewSourceControl()
	clone := e.git.AddClone(roundClone, shop, "main")
	e.exec = testkit.NewExecutor(nil)
	e.tools = app.NewAntTools(app.AntToolsDeps{
		Store: testkit.NewStateStore(), Claims: reviewClaims{}, Clock: e.clock,
		Executor: e.exec, Reviews: reviewSink{&e.rounds},
	}, app.AntToolsOptions{})
	client := e.gh.Client("mrosani")
	deps := app.ShepherdRoundsDeps{
		Route: func(context.Context, domain.Ref) (app.ToolWorkspace, error) {
			return app.ToolWorkspace{Clone: clone, Repo: shop, Settings: domain.Defaults()}, nil
		},
		CodeHost: func(context.Context, domain.Repository) (ports.CodeHostPort, error) { return client, nil },
		Git:      e.git, Executor: e.exec, Tokens: e.tools, Clock: e.clock,
	}
	for _, o := range opts {
		o(&deps)
	}
	e.rounds = app.NewShepherdRounds(deps, app.ShepherdRoundsOptions{
		Root:       "/worktrees",
		MCPCommand: func(token string) []string { return []string{"anthill", "mcp", token} },
	})
	e.pr = domain.Ref{Repo: shop, Kind: domain.KindPR, Number: 34}
	e.gh.PutPullRequest(domain.PullRequest{Ref: e.pr, Title: "Fix login", Body: "Raises the timeout.", SourceBranch: branch, TargetBranch: "main", Author: "ada", HeadCommit: "c1"})
	return e
}

// round runs one round on the PR's head while agent plays its review
// agent, and returns how it ended.
func (e *roundEnv) round(ctx context.Context, agent func(x *testkit.FakeExecution)) (app.RoundResult, error) {
	e.t.Helper()
	p, _ := e.gh.PullRequest(e.pr)
	e.nextID++
	start := app.RoundStart{
		Round:    ports.RoundRecord{ID: e.nextID, PR: e.pr, Head: p.HeadCommit, StartedAt: e.clock.Now()},
		PR:       p,
		Settings: domain.Defaults(),
		Runner:   domain.RunnerClaudeCode,
		Instance: instanceA,
	}
	type outcome struct {
		res app.RoundResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := e.rounds.Run(ctx, start)
		done <- outcome{res, err}
	}()
	synctest.Wait()
	execs := e.exec.Executions()
	if len(execs) == 0 {
		o := <-done
		return o.res, o.err
	}
	agent(execs[len(execs)-1])
	synctest.Wait()
	o := <-done
	return o.res, o.err
}

// submit calls submit_review as the agent of x.
func (e *roundEnv) submit(x *testkit.FakeExecution, summary string, findings ...domain.Finding) app.ToolResult {
	e.t.Helper()
	type arg struct {
		File           string `json:"file,omitempty"`
		StartLine      int    `json:"start_line,omitempty"`
		EndLine        int    `json:"end_line,omitempty"`
		Severity       string `json:"severity"`
		Classification string `json:"classification"`
		Message        string `json:"message"`
		SuggestedFix   string `json:"suggested_fix,omitempty"`
	}
	args := struct {
		Summary  string `json:"summary"`
		Findings []arg  `json:"findings"`
	}{Summary: summary, Findings: []arg{}}
	for _, f := range findings {
		args.Findings = append(args.Findings, arg{f.File, f.StartLine, f.EndLine, string(f.Severity), string(f.Classification), f.Message, f.SuggestedFix})
	}
	raw, _ := json.Marshal(args)
	res := e.tools.Call(e.ctx, x.Spec.Run.ToolToken, domain.ToolSubmitReview, raw)
	synctest.Wait()
	return res
}

// summaries are the PR's round summary comments.
func (e *roundEnv) summaries() []domain.Comment {
	p, _ := e.gh.PullRequest(e.pr)
	var out []domain.Comment
	for _, c := range p.Comments {
		if _, ok := domain.RoundOf(c, nil); ok {
			out = append(out, c)
		}
	}
	return out
}

func (e *roundEnv) threads() []domain.ReviewThread {
	p, _ := e.gh.PullRequest(e.pr)
	return p.Threads
}

// noWorktrees checks that the round removed its worktree.
func (e *roundEnv) noWorktrees() {
	e.t.Helper()
	if w := e.git.Worktrees(roundClone); len(w) != 0 {
		e.t.Errorf("worktrees left: %v", w)
	}
}

var authNull = domain.Finding{
	Location: domain.Location{File: "src/auth.ts", StartLine: 42},
	Severity: domain.SeverityBlocking, Classification: domain.NeedsDeveloper,
	Message: "user may be null here", SuggestedFix: "if (!user) return;",
}

// Spec pr-shepherd, "Blocking finding", and ant-tools, "Structured
// findings": the review agent runs in a new session in a detached
// worktree of the head commit, with submit_review only, and its finding
// at src/auth.ts:42 becomes an inline comment on line 42 with severity
// blocking, next to one summary, each with its marker.
func TestRoundBlockingFinding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newRoundEnv(t, "feature/login")
		noTests := domain.Finding{Severity: domain.SeveritySuggestion, Classification: domain.NeedsDeveloper, Message: "add a test"}
		var token string
		res, err := e.round(e.ctx, func(x *testkit.FakeExecution) {
			token = x.Spec.Run.ToolToken
			spec := x.Spec
			path := domain.RoundWorktreePath("/worktrees", shop, 34, 1)
			if spec.Session != "" || spec.Run.Worktree != path || e.git.WorktreeCommit(path) != "c1" ||
				!spec.Deadline.Equal(t0.Add(app.RoundTimeout)) || spec.Runner != domain.RunnerClaudeCode ||
				strings.Join(spec.Run.MCPCommand, " ") != "anthill mcp "+token {
				t.Errorf("review agent spec = %+v", spec)
			}
			if !strings.Contains(spec.Run.Brief, "git diff origin/main...HEAD") || !strings.Contains(spec.Run.Brief, "submit_review") {
				t.Errorf("brief = %q", spec.Run.Brief)
			}
			defs, err := e.tools.Tools(token)
			if err != nil || len(defs) != 1 || defs[0].Name != domain.ToolSubmitReview {
				t.Errorf("review agent tools = %+v, %v", defs, err)
			}
			if r := e.submit(x, "One real problem.", authNull, noTests); r.IsError {
				t.Errorf("submit_review = %q", r.Text)
			}
			if r := e.submit(x, "again"); !r.IsError {
				t.Error("a second submit_review was accepted")
			}
		})
		if err != nil || len(res.Findings) != 2 {
			t.Fatalf("Run() = %+v, %v", res, err)
		}
		threads := e.threads()
		if len(threads) != 1 {
			t.Fatalf("threads = %+v, want one inline comment", threads)
		}
		th := threads[0]
		inline, ok := domain.ParseAnthillComment(th.Comments[0].Body)
		if th.Path != "src/auth.ts" || th.Line != 42 || !ok || inline.Marker.Kind != domain.CommentFinding ||
			inline.Marker.FindingFields == nil || !strings.Contains(inline.Body, "**blocking**") ||
			!strings.Contains(inline.Body, "```suggestion\nif (!user) return;\n```") {
			t.Errorf("inline comment on %s:%d = %q", th.Path, th.Line, th.Comments[0].Body)
		}
		sums := e.summaries()
		if len(sums) != 1 {
			t.Fatalf("%d summaries, want 1", len(sums))
		}
		m, _ := domain.FindMarker(sums[0].Body)
		if m.Head != "c1" || len(m.Findings) != 2 || m.Findings[0].Comment != th.Comments[0].ID || m.Findings[1].Comment != "" {
			t.Errorf("summary marker = %+v", m.RoundFields)
		}
		if !strings.Contains(sums[0].Body, "add a test") || !strings.Contains(sums[0].Body, "One real problem.") || !strings.Contains(sums[0].Body, th.Comments[0].URL) {
			t.Errorf("summary = %q", sums[0].Body)
		}
		r, _ := domain.RoundOf(sums[0], threads)
		if len(r.Findings) != 2 || r.Findings[0].Thread != th.ID || r.Findings[0].Resolved {
			t.Errorf("rebuilt round = %+v", r)
		}
		e.noWorktrees()
		if r := e.tools.Call(e.ctx, token, domain.ToolSubmitReview, json.RawMessage(`{"summary":"x","findings":[]}`)); r.Text != domain.MessageUnknownToken {
			t.Errorf("token after the round = %q", r.Text)
		}
	})
}

// Spec pr-shepherd, "Clean round": the summary says no issues were
// found, and nothing approves the PR.
func TestRoundClean(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newRoundEnv(t, "feature/login")
		res, err := e.round(e.ctx, func(x *testkit.FakeExecution) { e.submit(x, "Looks good.") })
		if err != nil || len(res.Findings) != 0 {
			t.Fatalf("Run() = %+v, %v", res, err)
		}
		sums := e.summaries()
		if len(sums) != 1 || !strings.Contains(sums[0].Body, "no issues found") {
			t.Fatalf("summaries = %+v", sums)
		}
		if len(e.threads()) != 0 {
			t.Errorf("threads = %+v", e.threads())
		}
		// The round only reads the PR and comments on it.
		for _, r := range e.gh.Requests() {
			if r.Op != testkit.OpPullRequest && r.Op != testkit.OpAddComment {
				t.Errorf("the round called %s", r.Op)
			}
		}
	})
}

// Spec pr-shepherd, "Unchanged problem in round 2": the same problem,
// moved two lines, gets no new inline comment, and the round 2 summary
// links to the existing thread as still open.
func TestRoundUnchangedProblemInRound2(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newRoundEnv(t, "feature/login")
		if _, err := e.round(e.ctx, func(x *testkit.FakeExecution) { e.submit(x, "", authNull) }); err != nil {
			t.Fatal(err)
		}
		e.gh.EditPullRequest(e.pr, func(p *domain.PullRequest) { p.HeadCommit = "c2" })
		moved := authNull
		moved.StartLine = 44
		if _, err := e.round(e.ctx, func(x *testkit.FakeExecution) { e.submit(x, "", moved) }); err != nil {
			t.Fatal(err)
		}
		threads := e.threads()
		if len(threads) != 1 {
			t.Fatalf("%d threads after round 2, want 1", len(threads))
		}
		sums := e.summaries()
		if len(sums) != 2 {
			t.Fatalf("%d summaries", len(sums))
		}
		body := sums[1].Body
		if !strings.Contains(body, "Still open from earlier rounds") || !strings.Contains(body, threads[0].Comments[0].URL) {
			t.Errorf("round 2 summary = %q", body)
		}
		r, _ := domain.RoundOf(sums[1], threads)
		if r.Head != "c2" || len(r.Findings) != 1 || r.Findings[0].Thread != threads[0].ID {
			t.Errorf("rebuilt round 2 = %+v", r)
		}
	})
}

// An inline comment the code host refuses, such as on lines outside the
// change, goes in the summary; on an ant PR, the summary lists the
// findings as handed to the owning ant and the other nits as left to
// developers.
func TestRoundSummaryOnAntPR(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newRoundEnv(t, "ant/gh-12-fix-login")
		e.gh.Fail(testkit.OpAddReviewComment, errors.New("line not in the diff"))
		nit := domain.Finding{Severity: domain.SeverityNit, Classification: domain.NeedsDeveloper, Message: "vague name"}
		if _, err := e.round(e.ctx, func(x *testkit.FakeExecution) { e.submit(x, "", authNull, nit) }); err != nil {
			t.Fatal(err)
		}
		sums := e.summaries()
		if len(sums) != 1 || len(e.threads()) != 0 {
			t.Fatalf("summaries %d, threads %d", len(sums), len(e.threads()))
		}
		body := sums[0].Body
		handed := strings.Index(body, "Handed to the owning ant")
		left := strings.Index(body, "Left to developers")
		if handed < 0 || left < handed || !strings.Contains(body[handed:left], "user may be null") || !strings.Contains(body[left:], "vague name") ||
			!strings.Contains(body, "if (!user) return;") {
			t.Errorf("summary = %q", body)
		}
	})
}

// A review agent that ends without submit_review, at its deadline or
// not, posts nothing, so the round is tried again; a cancelled round
// ends its agent and posts nothing. The worktree is removed every time.
func TestRoundWithoutReport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newRoundEnv(t, "feature/login")
		tokens := int64(10)
		res, err := e.round(e.ctx, func(x *testkit.FakeExecution) {
			x.Emit(ports.ExecEvent{Kind: ports.ExecUsage, Usage: ports.Usage{Tokens: &tokens}})
			x.End(ports.ExecExited, ports.Exit{Code: 1})
		})
		if !errors.Is(err, app.ErrNoReview) || res.Usage.Tokens == nil || *res.Usage.Tokens != 10 {
			t.Errorf("Run() without a report = %+v, %v", res, err)
		}
		_, err = e.round(e.ctx, func(x *testkit.FakeExecution) { x.End(ports.ExecEndedAtDeadline, ports.Exit{Code: -1}) })
		if !errors.Is(err, app.ErrNoReview) || !strings.Contains(err.Error(), "30m") {
			t.Errorf("Run() at the deadline = %v", err)
		}

		ctx, cancel := context.WithCancel(e.ctx)
		var agent *testkit.FakeExecution
		_, err = e.round(ctx, func(x *testkit.FakeExecution) {
			agent = x
			cancel()
			time.Sleep(time.Second)
		})
		if !errors.Is(err, context.Canceled) || len(agent.Stops()) != 1 {
			t.Errorf("Run() cancelled while the agent works = %v, stops %v", err, agent.Stops())
		}
		if n := len(e.summaries()); n != 0 {
			t.Errorf("%d summaries posted", n)
		}
		e.noWorktrees()
	})
}
