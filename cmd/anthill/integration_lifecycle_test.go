package main

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// tool is a fake agent step that calls an ant tool with args.
func tool(name string, args any) testkit.AgentStep {
	b, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return testkit.AgentStep{Op: testkit.StepTool, Tool: name, Args: b}
}

// agent is a fake agent run that takes its steps.
func agent(steps ...testkit.AgentStep) testkit.AgentRun {
	return testkit.AgentRun{Script: testkit.AgentScript{Steps: steps}}
}

// committing is a fake agent run that first commits subjects on the
// run's worktree in the fake source control, as an agent does with git.
func committing(git *testkit.SourceControl, subjects []string, steps ...testkit.AgentStep) testkit.AgentRun {
	run := agent(steps...)
	run.Prepare = func(spec ports.ExecSpec, _ *testkit.AgentScript) {
		for _, s := range subjects {
			git.Commit(spec.Run.Worktree, s)
		}
	}
	return run
}

// optedIn puts an opted-in open bug on the provider.
func optedIn(w *world, n int, title string) {
	w.gh.PutItem(domain.WorkItem{
		Ref: shopItem(n), Title: title, Body: "Steps to reproduce.",
		Type: domain.TypeBug, State: domain.StateOpen, Labels: []string{"anthill"}, Author: "ada",
	})
}

// accept accepts a proposal from the console, moving the clock through
// the claim's re-read delay while the command waits.
func (in *instance) accept(ref domain.Ref) {
	in.t.Helper()
	done := make(chan struct{})
	var code int
	var out string
	go func() {
		defer close(done)
		var stdout, stderr string
		code, stdout, stderr = in.command("accept", "#"+strconv.Itoa(ref.Number))
		out = stdout + stderr
	}()
	in.w.advanceUntil("anthill accept", domain.DefaultClaimRereadDelay, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, in)
	if code != 0 {
		in.t.Fatalf("%s: anthill accept = %d: %s", in.name(), code, out)
	}
}

// polled waits for the first poll of the instance.
func (in *instance) polled() {
	in.t.Helper()
	in.w.eventually(in.name()+"'s first poll", func() bool {
		st, err := in.db().PollStatus(context.Background(), domain.ConnectionGitHub)
		return err == nil && !st.LastSuccess.IsZero()
	}, in)
}

// markedComments are the comments on ref, threads included, whose marker
// has kind.
func markedComments(w *world, ref domain.Ref, kind domain.CommentKind) []domain.Comment {
	var all []domain.Comment
	if ref.Kind == domain.KindPR {
		pr, _ := w.gh.PullRequest(ref)
		all = append(all, pr.Comments...)
		for _, th := range pr.Threads {
			all = append(all, th.Comments...)
		}
	} else {
		item, _ := w.gh.Item(ref)
		all = item.Comments
	}
	var out []domain.Comment
	for _, c := range all {
		if m, ok := domain.FindMarker(c.Body); ok && m.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// rounds are the summary comments of the shepherd rounds on a PR.
func rounds(w *world, pr domain.Ref) []domain.Marker {
	var out []domain.Marker
	for _, c := range markedComments(w, pr, domain.CommentSummary) {
		if m, _ := domain.FindMarker(c.Body); m.RoundFields != nil {
			out = append(out, m)
		}
	}
	return out
}

// Task 19.1: the whole lifecycle of an ant on the fake provider, with
// the fake agent as every agent: accept, first run, question and answer,
// PR, shepherd round, follow-up run, merge, done.
func TestIntegrationAntLifecycle(t *testing.T) {
	w := newWorld(t)
	in := newInstance(t, w, instanceOptions{login: "mrosani", machine: "laptop"})
	optedIn(w, 12, "Fix login timeout")
	item := shopItem(12)
	pr := domain.Ref{Repo: shopRepo, Kind: domain.KindPR, Number: 1}

	// Every agent, in the order Anthill starts them, since each one
	// follows from the one before: the first run asks a question, which
	// ends the run; the run the answer resumes commits, pushes, opens the
	// PR and finishes; round 1 posts a blocking finding, which starts a
	// follow-up run that fixes it, replies on its thread and pushes; and
	// round 2 reviews the new head and finds nothing.
	in.queueAgents(
		agent(tool(domain.ToolAskQuestion, map[string]string{"text": "Which timeout should apply, 30 s or 60 s?"})),
		committing(in.git, []string{"fix: apply a 60 s login timeout"},
			tool(domain.ToolPush, map[string]any{}),
			tool(domain.ToolOpenPullRequest, map[string]any{"title": "Fix login timeout", "body": "Applies a 60 s timeout."}),
			tool(domain.ToolFinish, map[string]any{"status": "complete", "summary": "Login times out after 60 s."}),
		),
		agent(tool(domain.ToolSubmitReview, map[string]any{
			"summary": "One problem.",
			"findings": []map[string]any{{
				"file": "login.go", "start_line": 10, "end_line": 12, "severity": "blocking",
				"classification": "needs-developer", "message": "The timeout is not applied to retries.",
			}},
		})),
		testkit.AgentRun{Prepare: func(spec ports.ExecSpec, script *testkit.AgentScript) {
			in.git.Commit(spec.Run.Worktree, "fix: apply the timeout to retries")
			p, _ := w.gh.PullRequest(pr)
			script.Steps = append(script.Steps,
				tool(domain.ToolPush, map[string]any{}),
				tool(domain.ToolReplyToThread, map[string]any{"thread": p.Threads[0].ID, "body": "Fixed.", "resolve": true}),
				tool(domain.ToolFinish, map[string]any{"status": "complete", "summary": "Retries use the timeout too."}),
			)
		}},
		agent(tool(domain.ToolSubmitReview, map[string]any{"summary": "Looks good.", "findings": []any{}})),
	)
	in.start()
	in.polled()

	// Accept: the first run asks its question.
	in.accept(item)
	w.eventually("the question", in.antIn(item, domain.AntAwaitingInput), in)
	if got := markedComments(w, item, domain.CommentQuestion); len(got) != 1 {
		t.Fatalf("question comments = %d, want 1", len(got))
	}

	// The answer resumes the session, and the run opens the PR.
	w.gh.Comment(item, "mrosani", "@anthill 60 s, please.")
	w.advanceUntil("the ant PR", testPollInterval, in.antIn(item, domain.AntSucceeded), in)
	runs := in.agentRuns()
	if want := "session-" + string(runs[0].ID); runs[1].Spec.Session != want {
		t.Errorf("the second run resumes %q, want %q", runs[1].Spec.Session, want)
	}
	if !strings.Contains(runs[1].Spec.Run.Brief, "60 s, please.") {
		t.Errorf("the resumed run's brief has no answer:\n%s", runs[1].Spec.Run.Brief)
	}

	// The shepherd rounds and the follow-up run between them.
	w.advanceUntil("round 2", domain.DefaultClaimRereadDelay, func() bool {
		return len(rounds(w, pr)) == 2 && in.antIn(item, domain.AntSucceeded)()
	}, in)
	rs := rounds(w, pr)
	p, _ := w.gh.PullRequest(pr)
	if len(rs[0].Findings) != 1 || len(rs[1].Findings) != 0 || rs[0].Head == rs[1].Head || rs[1].Head != p.HeadCommit {
		t.Errorf("rounds = %+v, %+v; PR head %s", rs[0].RoundFields, rs[1].RoundFields, p.HeadCommit)
	}
	if len(p.Threads) != 1 || !p.Threads[0].Resolved || len(markedComments(w, pr, domain.CommentReply)) != 1 {
		t.Errorf("finding thread = %+v, want it answered once and resolved", p.Threads)
	}
	runs = in.agentRuns()
	if len(runs) != 5 {
		t.Fatalf("agent executions = %d, want 5: two task runs, two rounds and one follow-up run", len(runs))
	}
	if follow := runs[3]; follow.Spec.Session != runs[1].Spec.Session || !strings.Contains(follow.Spec.Run.Brief, "The timeout is not applied to retries.") {
		t.Errorf("follow-up run: session %q, brief:\n%s", follow.Spec.Session, follow.Spec.Run.Brief)
	}
	rec, _ := in.ant(item)
	if rec.PR == nil || *rec.PR != pr {
		t.Errorf("ant PR = %v, want %v", rec.PR, pr)
	}
	stored, err := in.db().Runs(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, r := range stored {
		kinds = append(kinds, string(r.Kind)+":"+string(r.Outcome))
	}
	// A question ends its run as stopped (spec agent-conversation).
	if want := []string{"task:stopped", "task:complete", "follow-up:complete"}; !slices.Equal(kinds, want) {
		t.Errorf("runs = %v, want %v", kinds, want)
	}

	// The developer merges the PR: the ant is done and its claim
	// released.
	w.gh.EditPullRequest(pr, func(p *domain.PullRequest) { p.State = domain.PRMerged })
	w.advanceUntil("done", testPollInterval, func() bool {
		item, _ := w.gh.Item(item)
		return in.antIn(item.Ref, domain.AntDone)() && !domain.HasLabel(item.Labels, domain.DefaultClaimLabel)
	}, in)
	if got := len(in.agentRuns()); got != 5 {
		t.Errorf("agent executions after the merge = %d, want still 5", got)
	}
}
