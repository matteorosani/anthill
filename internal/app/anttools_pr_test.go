package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

type pushOut struct {
	Status string `json:"status"`
	Output string `json:"output"`
}

type prOut struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Draft       bool   `json:"draft"`
	ForcedDraft string `json:"forced_draft"`
}

// Spec ant-tools, "Push rejected because the remote moved".
func TestPushRejectedBecauseTheRemoteMoved(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token := e.issue(domain.RoleTaskRun)
	first := e.git.Commit(antWorktree, "Fix the timeout")
	var out pushOut
	e.mustCall(token, domain.ToolPush, `{}`, &out)
	if out.Status != "pushed" {
		t.Errorf("push = %+v", out)
	}
	if remote := e.git.RemoteCommits("/src/shop", antBranch); remote[len(remote)-1] != first {
		t.Fatalf("remote = %v, want the ant's commit on top", remote)
	}

	// A reviewer pushes to the ant branch; the ant's next push is
	// rejected, nothing is pushed, and the remote commits are fetched.
	theirs := e.git.SetRemoteBranch("/src/shop", antBranch, domain.BranchOwner{Item: "gh-12", Instance: "lbianchi@DESKTOP-7"}, "Reviewer suggestion")
	e.git.Commit(antWorktree, "More work")
	res := e.call(token, domain.ToolPush, `{}`)
	want := "push rejected: " + app.MessagePushConflict("origin", antBranch)
	if !res.IsError || !strings.HasPrefix(res.Text, want) || !strings.Contains(res.Text, "git output:\n! [rejected]") {
		t.Errorf("push = %+v, want the rejection with git's output", res)
	}
	if remote := e.git.RemoteCommits("/src/shop", antBranch); remote[len(remote)-1] != theirs {
		t.Error("the rejected push changed the remote")
	}
}

// Spec branching-policy, "Ant attempts to push to main" and "Amend after
// the first push", through the push tool.
func TestPushPolicyThroughTheTool(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token := e.issue(domain.RoleTaskRun)
	e.git.Commit(antWorktree, "Fix the timeout")
	var out pushOut
	e.mustCall(token, domain.ToolPush, `{}`, &out)
	e.git.Amend(antWorktree, "Fix the timeout, amended")
	res := e.call(token, domain.ToolPush, `{}`)
	if !res.IsError || !strings.HasPrefix(res.Text, "push rejected: "+domain.MessageHistoryRewritten) {
		t.Errorf("push after amend = %+v", res)
	}

	if err := e.git.Fetch(context.Background(), e.clone); err != nil {
		t.Fatal(err)
	}
	e.git.Checkout(antWorktree, "main")
	refused(t, e.call(token, domain.ToolPush, `{}`), "push to main is not allowed")
}

// Spec ant-tools, "Agent opens a ready PR".
func TestAgentOpensAReadyPR(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token := e.issue(domain.RoleTaskRun)
	e.git.Commit(antWorktree, "Fix the timeout")
	refused(t, e.call(token, domain.ToolOpenPullRequest, `{"title":"t","body":"b"}`), "the branch "+antBranch+" is not on the remote: call push first")
	var pushed pushOut
	e.mustCall(token, domain.ToolPush, `{}`, &pushed)
	e.git.Commit(antWorktree, "Not pushed yet")
	refused(t, e.call(token, domain.ToolOpenPullRequest, `{"title":"t","body":"b"}`), "the branch has 1 commits that are not pushed: call push first")
	e.mustCall(token, domain.ToolPush, `{}`, &pushed)

	var out prOut
	e.mustCall(token, domain.ToolOpenPullRequest, `{"title":"Fix login timeout on Safari","body":"Raises the session timeout.","draft":false}`, &out)
	if out.Draft || out.ForcedDraft != "" || out.Title != "Fix login timeout on Safari" {
		t.Errorf("result = %+v, want a ready PR", out)
	}
	rec := e.antRecord()
	if rec.PR == nil {
		t.Fatal("the ant record has no PR")
	}
	pr, _ := e.gh.PullRequest(*rec.PR)
	if pr.Draft || pr.Body != "Raises the session timeout.\n\nCloses #12" || pr.SourceBranch != antBranch || pr.TargetBranch != "main" ||
		len(pr.Labels) != 1 || pr.Labels[0] != "anthill" || pr.Item == nil || *pr.Item != ghRef(12) || pr.URL != out.URL {
		t.Errorf("opened PR = %+v", pr)
	}
}

// Spec ant-tools, "Second PR for the same item".
func TestSecondPRForTheSameItem(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token := e.issue(domain.RoleTaskRun)
	e.git.Commit(antWorktree, "Fix the timeout")
	var pushed pushOut
	e.mustCall(token, domain.ToolPush, `{}`, &pushed)
	var out prOut
	e.mustCall(token, domain.ToolOpenPullRequest, `{"title":"Fix login","body":"b"}`, &out)
	before := len(e.gh.Requests())
	refused(t, e.call(token, domain.ToolOpenPullRequest, `{"title":"Again","body":"b"}`), app.MessageSecondPR(out.URL))
	for _, r := range e.gh.Requests()[before:] {
		if r.Op == testkit.OpOpenPullRequest {
			t.Error("a second PR was opened")
		}
	}

	// An open PR from the ant branch that the record does not know about
	// counts too.
	rec := e.antRecord()
	rec.PR = nil
	if err := e.store.SaveAnt(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	refused(t, e.call(token, domain.ToolOpenPullRequest, `{"title":"Again","body":"b"}`), app.MessageSecondPR(out.URL))
}

// A PR is forced to draft after a partial or timed-out run, until a run
// finishes complete (spec ant-execution, "Draft PRs for incomplete
// work").
func TestForcedDraft(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	ctx := context.Background()
	runs, _ := e.store.Runs(ctx, e.ant)
	ended := runs[0]
	ended.EndedAt, ended.Outcome = t0.Add(time.Hour), ports.OutcomeTimedOut
	if err := e.store.EndRun(ctx, ended); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.StartRun(ctx, ports.RunRecord{Ant: e.ant, Kind: ports.RunTask, StartedAt: t0.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	token := e.issue(domain.RoleTaskRun)
	e.git.Commit(antWorktree, "WIP")
	var pushed pushOut
	e.mustCall(token, domain.ToolPush, `{}`, &pushed)
	var out prOut
	e.mustCall(token, domain.ToolOpenPullRequest, `{"title":"Fix login","body":"b","draft":false}`, &out)
	want := "the PR stays a draft until a run finishes complete: an earlier run timed out"
	if !out.Draft || out.ForcedDraft != want {
		t.Errorf("result = %+v, want a forced draft", out)
	}
	refused(t, e.call(token, domain.ToolUpdatePullRequest, `{"draft":false}`), want)

	// The body keeps its closing reference.
	e.mustCall(token, domain.ToolUpdatePullRequest, `{"title":"Fix login on Safari","body":"New text."}`, &out)
	pr, _ := e.gh.PullRequest(*e.antRecord().PR)
	if pr.Title != "Fix login on Safari" || pr.Body != "New text.\n\nCloses #12" || !pr.Draft {
		t.Errorf("updated PR = %+v", pr)
	}
}

func TestClosingReference(t *testing.T) {
	other := domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "web"}
	ado := domain.Ref{Repo: domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop"}, Kind: domain.KindItem, Number: 7}
	for _, tt := range []struct {
		item domain.Ref
		repo domain.Repository
		body string
		want string
	}{
		{ghRef(12), shop, "Text.", "Text.\n\nCloses #12"},
		{ghRef(12), other, "Text.\n", "Text.\n\nCloses acme/shop#12"},
		{ado, other, "Text.", "Text.\n\nAB#7"},
		{ghRef(12), shop, "Text.\n\nCloses #12", "Text.\n\nCloses #12"},
		{ghRef(12), shop, "", "Closes #12"},
	} {
		if got := domain.WithClosingReference(tt.body, tt.item, tt.repo); got != tt.want {
			t.Errorf("WithClosingReference(%q, %s) = %q, want %q", tt.body, tt.item, got, tt.want)
		}
	}
}
