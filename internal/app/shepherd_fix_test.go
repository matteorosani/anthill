package app_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// findingRounds is a RoundRunner that posts each finding as an inline
// comment and the summary with the round's findings, as a round does.
type findingRounds struct {
	host     ports.CodeHostPort
	findings []domain.Finding
	mu       sync.Mutex
	heads    []string
}

func (f *findingRounds) Run(ctx context.Context, r app.RoundStart) (app.RoundResult, error) {
	f.mu.Lock()
	f.heads = append(f.heads, r.Round.Head)
	f.mu.Unlock()
	var rfs []domain.RoundFinding
	for _, fd := range f.findings {
		c, err := f.host.AddReviewComment(ctx, r.Round.PR, ports.ReviewComment{Path: fd.File, StartLine: fd.StartLine, Comment: domain.AnthillComment{
			Role: domain.RoleShepherd, Body: fd.Message,
			Marker: domain.Marker{Version: domain.MarkerVersion, Instance: r.Instance, Kind: domain.CommentFinding, Ref: r.Round.PR.String(), FindingFields: fd.MarkerFields()},
		}})
		if err != nil {
			return app.RoundResult{}, err
		}
		rfs = append(rfs, domain.NewRoundFinding(fd, c.ID))
	}
	_, err := f.host.AddComment(ctx, r.Round.PR, domain.AnthillComment{
		Role: domain.RoleShepherd, Body: "Round summary.",
		Marker: domain.Marker{
			Version: domain.MarkerVersion, Instance: r.Instance, Kind: domain.CommentSummary, Ref: r.Round.PR.String(),
			RoundFields: &domain.RoundFields{Head: r.Round.Head, Requested: r.Round.Requested, Findings: rfs},
		},
	})
	return app.RoundResult{Findings: f.findings}, err
}

func (f *findingRounds) Heads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.heads)
}

// verifiedFixes is a FixPassRunner with the effect of a verified pass: each
// finding's thread is resolved, the push is recorded, and the PR's head
// moves to it.
type verifiedFixes struct {
	gh     *testkit.Provider
	host   ports.CodeHostPort
	mu     sync.Mutex
	starts []app.FixStart
	// hidden makes a pass push without posting its status comment, as if
	// no read of the PR showed it yet.
	hidden bool
}

const fixedHead = "fix1"

func (f *verifiedFixes) Run(ctx context.Context, s app.FixStart) (app.FixOutcome, error) {
	f.mu.Lock()
	f.starts = append(f.starts, s)
	hidden := f.hidden
	f.mu.Unlock()
	if hidden {
		// No status comment.
	} else if _, err := f.host.AddComment(ctx, s.PR.Ref, domain.AnthillComment{
		Role: domain.RoleShepherd, Body: "Pushed fixes.",
		Marker: domain.Marker{Version: domain.MarkerVersion, ID: app.AutofixMarkerPrefix + fixedHead, Instance: s.Instance, Kind: domain.CommentStatus, Ref: s.PR.Ref.String()},
	}); err != nil {
		return app.FixOutcome{}, err
	}
	for _, th := range s.Pass.Threads {
		if err := f.host.ResolveThread(ctx, s.PR.Ref, th); err != nil {
			return app.FixOutcome{}, err
		}
	}
	f.gh.EditPullRequest(s.PR.Ref, func(pr *domain.PullRequest) { pr.HeadCommit = fixedHead })
	return app.FixOutcome{Pushed: fixedHead, Fixed: len(s.Pass.Threads)}, nil
}

func (f *verifiedFixes) Starts() []app.FixStart {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.starts)
}

// fixShepherd is one instance whose shepherd reviews every PR (scope
// all) with findingRounds and verifiedFixes.
type fixShepherd struct {
	e        *shepherdEnv
	store    *testkit.StateStore
	rounds   *findingRounds
	fixes    *verifiedFixes
	shepherd *app.Shepherd
	settings domain.Effective
	mu       sync.Mutex
	errs     []error
}

func newFixShepherd(e *shepherdEnv, edit func(*domain.Effective), findings ...domain.Finding) *fixShepherd {
	client := e.gh.Client("mrosani")
	in := &fixShepherd{e: e, store: testkit.NewStateStore(), rounds: &findingRounds{host: client, findings: findings}, fixes: &verifiedFixes{gh: e.gh, host: client}}
	in.settings = domain.Defaults()
	in.settings.ShepherdScope = domain.ScopeAll
	in.settings.VerificationCommand = "go test ./..."
	if edit != nil {
		edit(&in.settings)
	}
	claimRoute := func(context.Context, domain.Ref) (app.ClaimTarget, error) {
		return app.ClaimTarget{Provider: client, Instance: instanceA, Label: domain.DefaultClaimLabel}, nil
	}
	claiming := app.NewClaiming(claimRoute, in.store, e.clock, &testkit.Notifier{}, &testkit.ClaimLossHandler{}, app.ClaimingOptions{})
	in.shepherd = app.NewShepherd(app.ShepherdDeps{
		Store:  in.store,
		Claims: claiming,
		Route: func(_ context.Context, ref domain.Ref) (app.ToolWorkspace, error) {
			return app.ToolWorkspace{Repo: ref.Repo, Settings: in.settings}, nil
		},
		CodeHost:    func(context.Context, domain.Repository) (ports.CodeHostPort, error) { return client, nil },
		WriteAccess: client.CanWrite,
		Instance:    func(domain.Repository) string { return instanceA },
		Runners:     app.NewRunners(map[domain.Runner]ports.AgentRunner{domain.RunnerClaudeCode: testkit.NewAgentRunner(ports.Capabilities{})}),
		Rounds:      in.rounds,
		RoundEnds:   app.NewUsageRecorder(in.store),
		Fixes:       in.fixes,
		Acks:        &fakeAcks{host: client, instance: instanceA},
		Clock:       e.clock,
		Errors: func(err error) {
			in.mu.Lock()
			in.errs = append(in.errs, err)
			in.mu.Unlock()
		},
	})
	e.t.Cleanup(func() {
		in.shepherd.Wait()
		in.mu.Lock()
		defer in.mu.Unlock()
		for _, err := range in.errs {
			e.t.Errorf("background error: %v", err)
		}
	})
	return in
}

func (in *fixShepherd) trigger(pr domain.Ref) {
	in.e.t.Helper()
	if err := in.e.run(func() error { return in.shepherd.Trigger(in.e.ctx, pr) }); err != nil {
		in.e.t.Fatalf("Trigger(%s) = %v", pr, err)
	}
}

// statuses lists the marker IDs of the PR's status comments.
func (e *shepherdEnv) statuses(pr domain.Ref) []string {
	p, _ := e.gh.PullRequest(pr)
	var out []string
	for _, c := range p.Comments {
		if m, ok := domain.FindMarker(c.Body); ok && m.Kind == domain.CommentStatus {
			out = append(out, m.ID)
		}
	}
	return out
}

// Spec pr-shepherd, "Auto-fix on an opted-in human PR" and "Human PR
// opted in": the round's auto-fixable finding goes to a fix pass within
// the round's claim, its thread is resolved, and the fix's push starts
// no round.
func TestShepherdAutoFixOnAnOptedInHumanPR(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		in := newFixShepherd(e, nil, unusedImportFinding, authNull)
		pr := e.putPR(shop, 34, "feature/login", func(p *domain.PullRequest) { p.Labels = []string{domain.LabelAutofix} })

		in.trigger(pr)
		starts := in.fixes.Starts()
		if len(starts) != 1 {
			t.Fatalf("%d fix passes, want 1", len(starts))
		}
		s := starts[0]
		p, _ := e.gh.PullRequest(pr)
		if s.Pass.Head != "c1" || len(s.Pass.Findings) != 1 || s.Pass.Findings[0].Message != "unused import" ||
			len(s.Pass.Threads) != 1 || s.Pass.Threads[0] != p.Threads[0].ID || s.Round == 0 || s.Instance != instanceA || s.Runner != domain.RunnerClaudeCode {
			t.Errorf("fix pass = %+v", s)
		}
		if !p.Threads[0].Resolved || p.Threads[1].Resolved {
			t.Errorf("threads resolved = %v, %v", p.Threads[0].Resolved, p.Threads[1].Resolved)
		}
		// The fix's push starts no round, now or later.
		in.trigger(pr)
		if got := in.rounds.Heads(); !slices.Equal(got, []string{"c1"}) {
			t.Errorf("rounds on %v", got)
		}
		if got := in.fixes.Starts(); len(got) != 1 {
			t.Errorf("%d fix passes", len(got))
		}
		held, _ := in.store.HeldClaims(e.ctx)
		if len(held) != 0 {
			t.Errorf("claims held: %v", held)
		}
	})
}

// Spec pr-shepherd, "Human PR not opted in": nothing is fixed.
func TestShepherdHumanPRNotOptedIn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		in := newFixShepherd(e, nil, unusedImportFinding)
		pr := e.putPR(shop, 34, "feature/login", nil)
		in.trigger(pr)
		if got := in.fixes.Starts(); len(got) != 0 {
			t.Errorf("fix passes = %+v", got)
		}
		if got := e.statuses(pr); len(got) != 0 {
			t.Errorf("status comments = %v", got)
		}
	})
}

// Spec pr-shepherd, "No verification command" and "Fork branch not
// pushable": the findings stay suggested changes, nothing is pushed, and
// a comment, posted once, says why.
func TestShepherdAutoFixUnavailable(t *testing.T) {
	for _, c := range []struct {
		name   string
		edit   func(*domain.Effective)
		pr     func(*domain.PullRequest)
		reason domain.AutofixUnavailableReason
		text   string
	}{
		{"no verification command", func(s *domain.Effective) { s.VerificationCommand = "" }, nil,
			domain.AutofixNoVerification, "Auto-fix needs a verification command"},
		{"fork branch not pushable", nil, func(p *domain.PullRequest) {
			p.Fork, p.ForkRepo, p.ForkURL = true, domain.Repository{Provider: domain.GitHub, Org: "ada", Name: "shop"}, "https://github.com/ada/shop.git"
		}, domain.AutofixNotPushable, "does not allow edits by maintainers"},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newShepherdEnv(t)
				in := newFixShepherd(e, c.edit, unusedImportFinding)
				pr := e.putPR(shop, 34, "feature/login", func(p *domain.PullRequest) {
					p.Labels = []string{domain.LabelAutofix}
					if c.pr != nil {
						c.pr(p)
					}
				})
				in.trigger(pr)
				in.trigger(pr)
				if got := in.fixes.Starts(); len(got) != 0 {
					t.Errorf("fix passes = %+v", got)
				}
				if got := e.statuses(pr); !slices.Equal(got, []string{app.AutofixUnavailableMarkerID(c.reason)}) {
					t.Errorf("status comments = %v", got)
				}
				p, _ := e.gh.PullRequest(pr)
				found := false
				for _, cm := range p.Comments {
					found = found || strings.Contains(cm.Body, c.text)
				}
				if !found || p.HeadCommit != "c1" || p.Threads[0].Resolved {
					t.Errorf("comment found %v, head %s, resolved %v", found, p.HeadCommit, p.Threads[0].Resolved)
				}
			})
		})
	}
}

// Spec agent-conversation, "Fix on a human PR": /anthill fix by the PR's
// author in a finding's thread opts that thread in, and only that one.
func TestShepherdFixCommandOptsInOneThread(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		in := newFixShepherd(e, nil, unusedImportFinding, unusedVarFinding)
		pr := e.putPR(shop, 34, "feature/login", nil)
		in.trigger(pr)
		if got := in.fixes.Starts(); len(got) != 0 {
			t.Fatalf("fix passes before /anthill fix = %+v", got)
		}
		p, _ := e.gh.PullRequest(pr)
		thread := p.Threads[1].ID
		// An outsider's command does not count.
		e.gh.EditPullRequest(pr, func(p *domain.PullRequest) {
			p.Threads[0].Comments = append(p.Threads[0].Comments, domain.Comment{ID: "h0", Author: "stranger", Body: "/anthill fix"})
			p.Threads[1].Comments = append(p.Threads[1].Comments, domain.Comment{ID: "h1", Author: "ada", Body: "/anthill fix"})
		})
		var won bool
		if err := e.run(func() error {
			var err error
			won, err = in.shepherd.OptInFix(e.ctx, pr, thread, "h1")
			return err
		}); err != nil || !won {
			t.Fatalf("OptInFix() = %v, %v", won, err)
		}
		starts := in.fixes.Starts()
		if len(starts) != 1 || !slices.Equal(starts[0].Pass.Threads, []string{thread}) {
			t.Fatalf("fix passes = %+v", starts)
		}
		// Repeating the command does nothing more.
		if err := e.run(func() error { _, err := in.shepherd.OptInFix(e.ctx, pr, thread, "h1"); return err }); err != nil {
			t.Fatal(err)
		}
		if got := in.fixes.Starts(); len(got) != 1 {
			t.Errorf("%d fix passes", len(got))
		}
	})
}

// Spec pr-shepherd, "Failed auto-fix falls back to developer": a finding
// whose thread holds a fix pass's reclassification is needs-developer
// for every instance, and gets no second pass.
func TestShepherdReclassifiedFindingGetsNoPass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		in := newFixShepherd(e, nil, unusedImportFinding)
		pr := e.putPR(shop, 34, "feature/login", nil)
		in.trigger(pr)
		p, _ := e.gh.PullRequest(pr)
		thread := p.Threads[0].ID
		// Another instance's fix pass failed and reclassified the finding.
		if _, err := e.gh.Client("lbianchi").ReplyToThread(e.ctx, pr, thread, domain.AnthillComment{
			Role: domain.RoleShepherd, Body: domain.FixFailedText(domain.FixVerifyFailed, "exit code 1"),
			Marker: domain.Marker{Version: domain.MarkerVersion, ID: domain.ReclassifiedMarkerID(thread), Instance: instanceB, Kind: domain.CommentReply, Ref: pr.String()},
		}); err != nil {
			t.Fatal(err)
		}
		e.gh.EditPullRequest(pr, func(p *domain.PullRequest) { p.Labels = []string{domain.LabelAutofix} })
		in.trigger(pr)
		if got := in.fixes.Starts(); len(got) != 0 {
			t.Errorf("fix passes = %+v", got)
		}
	})
}
