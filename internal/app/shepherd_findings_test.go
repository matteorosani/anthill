package app_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// handEnv is the owning instance A of an ant PR, with real ant
// supervision, change reaction and shepherd. Another instance, B, runs
// the rounds on the PR.
type handEnv struct {
	*reactEnv
	shepherd *app.Shepherd
}

func newHandEnv(t *testing.T) *handEnv {
	t.Helper()
	e := &handEnv{reactEnv: newReactEnv(t)}
	client := e.gh.Client("mrosani")
	e.shepherd = app.NewShepherd(app.ShepherdDeps{
		Store:  e.store,
		Claims: e.claiming,
		Route: func(context.Context, domain.Ref) (app.ToolWorkspace, error) {
			return app.ToolWorkspace{Clone: e.clone, Repo: shop, Settings: e.settings}, nil
		},
		CodeHost:    func(context.Context, domain.Repository) (ports.CodeHostPort, error) { return client, nil },
		WriteAccess: client.CanWrite,
		Instance:    func(domain.Repository) string { return instanceA },
		Runners:     app.NewRunners(map[domain.Runner]ports.AgentRunner{domain.RunnerClaudeCode: e.runner}),
		Rounds:      &fakeRounds{host: client},
		RoundEnds:   app.NewUsageRecorder(e.store),
		Acks:        &fakeAcks{host: client, instance: instanceA},
		Clock:       e.clock,
		Ants:        e.sup,
		Errors: func(err error) {
			e.mu.Lock()
			e.errs = append(e.errs, err)
			e.mu.Unlock()
		},
	})
	e.reactor = app.NewChangeReactor(app.ChangeReactorDeps{
		Store: e.store, Claims: e.claiming, Ants: e.sup,
		Tracker:  func(context.Context, domain.Ref) (ports.TrackerPort, error) { return client, nil },
		CodeHost: func(context.Context, domain.Repository) (ports.CodeHostPort, error) { return client, nil },
		Route: func(context.Context, domain.Ref) (app.ToolWorkspace, error) {
			return app.ToolWorkspace{Clone: e.clone, Repo: shop, Settings: e.settings}, nil
		},
		Classifier: e.classifier,
		Findings:   e.shepherd,
	})
	t.Cleanup(e.shepherd.Wait)
	return e
}

// roundByB posts, as instance B, the summary of a round on the PR's head
// with findings, and returns its change event as A's poll sees it.
func (e *handEnv) roundByB(pr domain.Ref, findings ...domain.Finding) domain.ChangeEvent {
	e.t.Helper()
	p, _ := e.gh.PullRequest(pr)
	if p.HeadCommit == "" {
		e.gh.EditPullRequest(pr, func(p *domain.PullRequest) { p.HeadCommit = "c1" })
		p, _ = e.gh.PullRequest(pr)
	}
	marked := make([]domain.RoundFinding, len(findings))
	listed := make([]domain.SummaryFinding, len(findings))
	for i, f := range findings {
		marked[i] = domain.NewRoundFinding(f, "")
		listed[i] = domain.SummaryFinding{Finding: f}
	}
	c, err := e.gh.Client("lbianchi").AddComment(e.ctx, pr, domain.AnthillComment{
		Role: domain.RoleShepherd,
		Body: domain.RenderRoundSummary(domain.RoundSummaryInput{Head: p.HeadCommit, AntPR: true, Findings: listed}),
		Marker: domain.Marker{
			Version: domain.MarkerVersion, Instance: instanceB, Kind: domain.CommentSummary, Ref: pr.String(),
			RoundFields: &domain.RoundFields{Head: p.HeadCommit, Findings: marked},
		},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return domain.ChangeEvent{Ref: pr, Kind: domain.ChangeCommentAdded, Actor: "lbianchi", At: e.clock.Now(), Origin: domain.OriginAnthill, CommentID: c.ID, Body: c.Body}
}

// handedComments counts the comments saying a round was handed.
func (e *handEnv) handedComments(pr domain.Ref) int {
	p, _ := e.gh.PullRequest(pr)
	n := 0
	for _, c := range p.Comments {
		if head, ok := domain.HandedOf(c); ok {
			n++
			if head != p.HeadCommit || c.Author != "mrosani" {
				e.t.Errorf("handed comment %q by %s, want for %s by mrosani", c.Body, c.Author, p.HeadCommit)
			}
		}
	}
	return n
}

var (
	handBlocking = domain.Finding{
		Location: domain.Location{File: "src/auth.ts", StartLine: 42},
		Severity: domain.SeverityBlocking, Classification: domain.NeedsDeveloper, Message: "user may be null here",
	}
	handSuggestion = domain.Finding{Severity: domain.SeveritySuggestion, Classification: domain.NeedsDeveloper, Message: "add a test for the timeout"}
	handFixableNit = domain.Finding{
		Location: domain.Location{File: "src/util.ts", StartLine: 1},
		Severity: domain.SeverityNit, Classification: domain.AutoFixable, Message: "unused import of fs",
	}
	handDeveloperNit = domain.Finding{Severity: domain.SeverityNit, Classification: domain.NeedsDeveloper, Message: "the name tmp is vague"}
)

// Spec pr-shepherd, "Findings handed to the owning ant", and
// change-reaction, "Blocking finding fixed by the ant": a round by
// another instance on the ant PR goes to the owning instance, which hands
// the blocking finding to one follow-up run, with the auto-fixable nit,
// and leaves the other nit to developers. The hand-over is recorded on
// the PR and happens once.
func TestFindingsHandedToTheOwningAnt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newHandEnv(t)
		id := e.succeed(12)
		pr := *e.ant(id).PR
		round := e.roundByB(pr, handBlocking, handFixableNit, handDeveloperNit)
		e.poll(round)

		e.wantRuns(id, 2)
		e.wantState(id, domain.AntRunning)
		got := e.briefs.last()
		if len(got.changes) != 1 || got.changes[0].Event.CommentID != round.CommentID ||
			!reflect.DeepEqual(got.changes[0].Findings, []domain.Finding{handBlocking, handFixableNit}) {
			t.Errorf("follow-up run changes = %+v, want the blocking finding and the auto-fixable nit", got.changes)
		}
		if n := e.handedComments(pr); n != 1 {
			t.Errorf("%d handed comments, want 1", n)
		}
		p, _ := e.gh.PullRequest(pr)
		if summary := p.Comments[len(p.Comments)-2].Body; !strings.Contains(summary, "Handed to the owning ant") || !strings.Contains(summary, "Left to developers") {
			t.Errorf("round summary = %q", summary)
		}

		// The same event again, and another trigger, hand nothing more.
		e.poll(round)
		e.mustDo(func() error { return e.shepherd.Trigger(e.ctx, pr) })
		e.wantRuns(id, 2)
		if n := e.handedComments(pr); n != 1 {
			t.Errorf("%d handed comments after a second trigger, want 1", n)
		}
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Auto-fixable nit included in the run" and
// "Nit left alone": a suggestion and an auto-fixable nit go to one
// follow-up run and no fix agent runs; nits alone start no run.
func TestAutoFixableNitIncludedInTheRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newHandEnv(t)
		id := e.succeed(12)
		pr := *e.ant(id).PR
		e.poll(e.roundByB(pr, handSuggestion, handFixableNit))

		e.wantRuns(id, 2)
		if got := e.briefs.last().changes; len(got) != 1 || !reflect.DeepEqual(got[0].Findings, []domain.Finding{handSuggestion, handFixableNit}) {
			t.Errorf("follow-up run changes = %+v", got)
		}
		// The executions are the task run and the follow-up run: no fix
		// agent.
		if n := len(e.exec.Executions()); n != 2 {
			t.Errorf("%d executions, want 2", n)
		}
		e.wantNoErrors()
	})
	synctest.Test(t, func(t *testing.T) {
		e := newHandEnv(t)
		id := e.succeed(12)
		pr := *e.ant(id).PR
		e.poll(e.roundByB(pr, handFixableNit, handDeveloperNit))

		e.wantRuns(id, 1)
		if n := e.handedComments(pr); n != 0 {
			t.Errorf("%d handed comments for nits alone", n)
		}
		e.wantNoErrors()
	})
}

// A hand-over whose findings are stored, because the ant is held, keeps
// them for its follow-up run; routing the same round twice stores it
// once.
func TestHandedFindingsAreStoredOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newHandEnv(t)
		id := e.succeed(12)
		pr := *e.ant(id).PR
		e.setLabels(12, e.settings.HoldLabel, "")
		round := e.roundByB(pr, handBlocking)
		e.poll(round)

		e.wantRuns(id, 1)
		stored := e.stored(id)
		if len(stored) != 1 || !reflect.DeepEqual(stored[0].Findings, []domain.Finding{handBlocking}) || stored[0].Key == "" {
			t.Fatalf("stored changes = %+v", stored)
		}
		change := domain.AntChange{Event: stored[0].Event, Findings: stored[0].Findings}
		e.mustDo(func() error { return e.sup.RouteChange(e.ctx, id, change) })
		if n := len(e.stored(id)); n != 1 {
			t.Errorf("%d stored changes after routing the round again", n)
		}

		e.setLabels(12, "", e.settings.HoldLabel)
		e.mustDo(func() error { return e.sup.Trigger(e.ctx, id) })
		e.wantRuns(id, 2)
		if got := e.briefs.last().changes; len(got) != 1 || !reflect.DeepEqual(got[0].Findings, []domain.Finding{handBlocking}) {
			t.Errorf("follow-up run changes = %+v", got)
		}
		e.wantNoErrors()
	})
}
