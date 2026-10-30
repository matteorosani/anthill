package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// withPR gives the ant its PR 34 from the ant branch, with one review
// thread, as after open_pull_request.
func (e *toolsEnv) withPR() domain.Ref {
	e.t.Helper()
	pr := ghPR(34)
	e.gh.PutPullRequest(domain.PullRequest{
		Ref: pr, URL: "https://github.com/acme/shop/pull/34", Title: "Fix login",
		SourceBranch: antBranch, TargetBranch: "main", Author: "mrosani",
		Threads: []domain.ReviewThread{{ID: "thread-7", Path: "src/auth.go", Line: 42}},
	})
	rec := e.antRecord()
	rec.PR = &pr
	if err := e.store.SaveAnt(context.Background(), rec); err != nil {
		e.t.Fatal(err)
	}
	return pr
}

// lastComment returns the last comment on an item or PR, parsed as an
// Anthill comment.
func (e *toolsEnv) lastComment(ref domain.Ref) (domain.Comment, domain.AnthillComment) {
	e.t.Helper()
	var comments []domain.Comment
	if ref.Kind == domain.KindPR {
		pr, _ := e.gh.PullRequest(ref)
		comments = pr.Comments
	} else {
		item, _ := e.gh.Item(ref)
		comments = item.Comments
	}
	if len(comments) == 0 {
		e.t.Fatalf("no comment on %s", ref)
	}
	c := comments[len(comments)-1]
	ac, ok := domain.ParseAnthillComment(c.Body)
	if !ok {
		e.t.Fatalf("comment %q has no marker", c.Body)
	}
	return c, ac
}

// Spec ant-tools, "Agent replies to a review thread".
func TestAgentRepliesToAReviewThread(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	pr := e.withPR()
	var out struct {
		URL      string `json:"url"`
		Resolved bool   `json:"resolved"`
	}
	e.mustCall(e.issue(domain.RoleFollowUpRun), domain.ToolReplyToThread,
		`{"thread":"thread-7","body":"Handled the empty-password case in 3f2a1c","resolve":true}`, &out)
	if !out.Resolved {
		t.Errorf("result = %+v, want resolved", out)
	}
	got, _ := e.gh.PullRequest(pr)
	thread := got.Threads[0]
	if !thread.Resolved || len(thread.Comments) != 1 {
		t.Fatalf("thread = %+v, want one reply and resolved", thread)
	}
	ac, ok := domain.ParseAnthillComment(thread.Comments[0].Body)
	if !ok || ac.Body != "Handled the empty-password case in 3f2a1c" || ac.Marker.Kind != domain.CommentReply ||
		ac.Marker.Instance != instanceA || ac.Marker.Ref != "gh-pr-34" || ac.Role != domain.RoleAnt {
		t.Errorf("reply = %+v", ac)
	}

	// Without resolve, the thread stays open; an unknown thread fails.
	e.gh.EditPullRequest(pr, func(p *domain.PullRequest) { p.Threads[0].Resolved = false })
	e.mustCall(e.issue(domain.RoleFollowUpRun), domain.ToolReplyToThread, `{"thread":"thread-7","body":"More."}`, &out)
	if got, _ := e.gh.PullRequest(pr); got.Threads[0].Resolved || out.Resolved {
		t.Error("the thread was resolved without resolve")
	}
	if res := e.call(e.issue(domain.RoleFollowUpRun), domain.ToolReplyToThread, `{"thread":"thread-9","body":"x"}`); !res.IsError {
		t.Errorf("reply to an unknown thread = %+v", res)
	}
}

func TestReplyToThreadWithoutPR(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token := e.issue(domain.RoleTaskRun)
	refused(t, e.call(token, domain.ToolReplyToThread, `{"thread":"thread-7","body":"x"}`), app.MessageNoPR)
	refused(t, e.call(token, domain.ToolComment, `{"where":"pr","body":"x"}`), app.MessageNoPR)
}

// Spec ant-tools, "Comment containing a secret": the comment is posted
// with the value redacted, and local paths are rewritten (spec
// agent-conversation, "Comment content safety").
func TestCommentContainingASecret(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	secret := "ghp_" + strings.Repeat("a1B2", 9)
	body := "Token " + secret + " and ACME-12345678 found in " + antWorktree + "/src/auth.go, see " + userHome + "/.netrc"
	var out struct {
		URL string `json:"url"`
	}
	e.mustCall(e.issue(domain.RoleTaskRun), domain.ToolComment, `{"where":"item","body":"`+body+`"}`, &out)
	c, ac := e.lastComment(ghRef(12))
	want := "Token [REDACTED] and [REDACTED] found in ./src/auth.go, see ~/.netrc"
	if ac.Body != want {
		t.Errorf("posted body = %q, want %q", ac.Body, want)
	}
	if strings.Contains(c.Body, secret) || ac.Marker.Kind != domain.CommentReply || ac.Marker.Ref != "gh-12" {
		t.Errorf("posted comment = %q", c.Body)
	}

	// On the PR, the comment goes to the PR.
	pr := e.withPR()
	e.mustCall(e.issue(domain.RoleTaskRun), domain.ToolComment, `{"where":"pr","body":"Ready for review."}`, &out)
	if _, ac := e.lastComment(pr); ac.Body != "Ready for review." || ac.Marker.Ref != "gh-pr-34" {
		t.Errorf("PR comment = %+v", ac)
	}
}

func TestAskQuestion(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	e.clock.Advance(10 * time.Minute)
	var out struct {
		URL     string `json:"url"`
		Message string `json:"message"`
	}
	e.mustCall(e.issue(domain.RoleTaskRun), domain.ToolAskQuestion, `{"text":"Does timeout mean 30 or 60 seconds?"}`, &out)
	if out.Message != app.MessageQuestionPosted {
		t.Errorf("result = %+v", out)
	}
	c, ac := e.lastComment(ghRef(12))
	if ac.Marker.Kind != domain.CommentQuestion || ac.Body != "Does timeout mean 30 or 60 seconds?" {
		t.Errorf("question = %+v", ac)
	}
	questions, _ := e.store.PendingQuestions(context.Background())
	if len(questions) != 1 || questions[0].CommentID != c.ID || questions[0].On != ghRef(12) || questions[0].Ant != e.ant {
		t.Errorf("stored questions = %+v", questions)
	}
	rec := e.antRecord()
	if rec.State() != domain.AntAwaitingInput || rec.Budget.Counting() {
		t.Errorf("state %s, counting %v; want awaiting-input with the budget paused", rec.State(), rec.Budget.Counting())
	}
	if len(e.runs.ended) != 1 || e.runs.ended[0] != "1/1: asked a question" {
		t.Errorf("ended runs = %v", e.runs.ended)
	}
	if n := e.notifier.Sent(); len(n) != 1 || n[0].Kind != ports.NotifyQuestion {
		t.Errorf("notifications = %+v", n)
	}
	// The run has ended: its next call is refused.
	refused(t, e.call(e.issue(domain.RoleTaskRun), domain.ToolGetTask, `{}`), "the run has ended")
}

// A follow-up run asks on its PR by default.
func TestAskQuestionInFollowUpRun(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	pr := e.withPR()
	var out struct{}
	e.mustCall(e.issue(domain.RoleFollowUpRun), domain.ToolAskQuestion, `{"text":"Keep the old API?"}`, &out)
	if _, ac := e.lastComment(pr); ac.Marker.Kind != domain.CommentQuestion {
		t.Errorf("question on the PR = %+v", ac)
	}
}

func TestReportProgress(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token := e.issue(domain.RoleTaskRun)
	var out struct {
		URL string `json:"url"`
	}
	e.mustCall(token, domain.ToolReportProgress, `{"text":"Reproduced the bug."}`, &out)
	e.mustCall(token, domain.ToolReportProgress, `{"text":"`+strings.Repeat("x", 5000)+`"}`, &out)
	item, _ := e.gh.Item(ghRef(12))
	if len(item.Comments) != 1 {
		t.Fatalf("comments = %d, want the one status comment edited", len(item.Comments))
	}
	ac, _ := domain.ParseAnthillComment(item.Comments[0].Body)
	if ac.Marker.Kind != domain.CommentStatus || !strings.Contains(ac.Body, "(truncated, ") || len([]rune(ac.Body)) > domain.DefaultSummaryLimit {
		t.Errorf("status comment = %d characters, kind %s", len([]rune(ac.Body)), ac.Marker.Kind)
	}

}
