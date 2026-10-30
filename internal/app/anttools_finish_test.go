package app_test

import (
	"context"
	"testing"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
)

type acceptedOut struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Spec ant-tools, "Complete without commits".
func TestCompleteWithoutCommits(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token := e.issue(domain.RoleTaskRun)
	refused(t, e.call(token, domain.ToolFinish, `{"status":"complete","summary":"Done."}`), "no commits on the branch: finish with no-change, or ask a question")
	refused(t, e.call(token, domain.ToolFinish, `{"status":"partial","summary":"Half."}`), "no commits on the branch: finish with no-change, or ask a question")
	if got := e.antRecord().State(); got != domain.AntRunning || len(e.finishes.got) != 0 {
		t.Errorf("state %s, finishes %v: a refused finish must have no effect", got, e.finishes.got)
	}
	// no-change is the right status then.
	var out acceptedOut
	e.mustCall(token, domain.ToolFinish, `{"status":"no-change","summary":"Already fixed on main."}`, &out)
	if out.Status != "accepted" || e.antRecord().State() != domain.AntFinishing {
		t.Errorf("finish = %+v, state %s", out, e.antRecord().State())
	}
}

// Spec ant-tools, "No-change with commits".
func TestNoChangeWithCommits(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token := e.issue(domain.RoleTaskRun)
	e.git.Commit(antWorktree, "Fix the timeout")
	refused(t, e.call(token, domain.ToolFinish, `{"status":"no-change","summary":"Nothing to do."}`), "the branch has new commits: finish with complete or partial")
	if len(e.finishes.got) != 0 {
		t.Error("a refused finish was handed over")
	}
}

// A finish that passes its checks moves the ant to finishing and hands
// the finish over with its text made safe; later calls are refused.
func TestFinishHandedOver(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token := e.issue(domain.RoleTaskRun)
	e.git.Commit(antWorktree, "Fix the timeout")
	e.git.Commit(antWorktree, "Add a test")
	var out acceptedOut
	e.mustCall(token, domain.ToolFinish, `{"status":"partial","summary":"Fixed in `+antWorktree+`/auth.go","open_points":["Firefox not checked","key ACME-12345678"]}`, &out)
	if len(e.finishes.got) != 1 {
		t.Fatalf("finishes = %v", e.finishes.got)
	}
	f := e.finishes.got[0]
	if f.Status != domain.FinishPartial || f.Summary != "Fixed in ./auth.go" || f.NewCommits != 2 || f.Ant != e.ant || f.Run != 1 ||
		len(f.OpenPoints) != 2 || f.OpenPoints[1] != "key [REDACTED]" {
		t.Errorf("handed over = %+v", f)
	}
	if got := e.antRecord().State(); got != domain.AntFinishing {
		t.Errorf("state = %s, want finishing", got)
	}
	refused(t, e.call(token, domain.ToolPush, `{}`), "the run is finishing: no more tool calls")
}

// A follow-up run may finish no-change after replying, with or without
// commits on the branch (spec ant-execution, "Follow-up needing no
// change").
func TestFollowUpFinishWithoutCommitCheck(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	e.withPR()
	e.git.Commit(antWorktree, "Earlier work")
	var out acceptedOut
	e.mustCall(e.issue(domain.RoleFollowUpRun), domain.ToolFinish, `{"status":"no-change","summary":"Answered the question."}`, &out)
	if len(e.finishes.got) != 1 || e.finishes.got[0].Role != domain.RoleFollowUpRun {
		t.Errorf("finishes = %+v", e.finishes.got)
	}
}

// Spec ant-tools, "Structured findings", up to the hand-over: the two
// findings reach the shepherd checked and redacted. Posting them as two
// inline comments and a summary is the shepherd's (task 16.3).
func TestStructuredFindings(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	e.claimReview()
	token := e.issue(domain.RoleReviewAgent)
	args := `{"summary":"Two problems.","findings":[` +
		`{"file":"src/auth.ts","start_line":42,"severity":"blocking","classification":"needs-developer","message":"Null dereference of user."},` +
		`{"severity":"nit","classification":"auto-fixable","message":"Typo in the README, token ACME-12345678.","suggested_fix":"timeout"}]}`
	var out acceptedOut
	e.mustCall(token, domain.ToolSubmitReview, args, &out)
	if len(e.reviews.got) != 1 {
		t.Fatalf("reviews = %v", e.reviews.got)
	}
	r := e.reviews.got[0]
	if r.PR != ghPR(34) || r.Summary != "Two problems." || len(r.Findings) != 2 {
		t.Fatalf("handed over = %+v", r)
	}
	if f := r.Findings[0]; f.File != "src/auth.ts" || f.StartLine != 42 || f.Severity != domain.SeverityBlocking || f.Classification != domain.NeedsDeveloper {
		t.Errorf("first finding = %+v", f)
	}
	if f := r.Findings[1]; f.Known() || f.Message != "Typo in the README, token [REDACTED]." {
		t.Errorf("second finding = %+v", f)
	}
	refused(t, e.call(token, domain.ToolSubmitReview, args), "already reported: this agent's result was handed over")

	other := e.issue(domain.RoleReviewAgent)
	refused(t, e.call(other, domain.ToolSubmitReview, `{"summary":"x","findings":[{"file":"a.go","start_line":9,"end_line":3,"severity":"nit","classification":"auto-fixable","message":"m"}]}`),
		"findings[0]: finding has an invalid line range 9-3")
	refused(t, e.call(other, domain.ToolSubmitReview, `{"summary":"x","findings":[{"severity":"fatal","classification":"auto-fixable","message":"m"}]}`),
		`severity must be one of blocking, suggestion, nit, not "fatal"`)
}

// claimReview makes this instance hold the review claim on PR 34.
func (e *toolsEnv) claimReview() {
	e.t.Helper()
	if err := e.store.SaveClaim(context.Background(), heldReview()); err != nil {
		e.t.Fatal(err)
	}
}

// Spec ant-tools, "One fix skipped", up to the hand-over: the fixed
// finding comes with its full commit and the skipped one with its
// reason. Verifying, pushing and reclassifying are the shepherd's (task
// 16.5).
func TestOneFixSkipped(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	e.claimReview()
	ctx := context.Background()
	e.git.SetRemoteBranch("/src/shop", "feature/login", domain.BranchOwner{}, "Human work")
	if err := e.git.Fetch(ctx, e.clone); err != nil {
		t.Fatal(err)
	}
	if err := e.git.CreateBranch(ctx, e.clone, "anthill/fix-pr-34", "feature/login"); err != nil {
		t.Fatal(err)
	}
	fixTree := "/home/ann/.anthill/worktrees/gh-acme-shop/fix-pr-34"
	if err := e.git.AddWorktree(ctx, e.clone, fixTree, "anthill/fix-pr-34", domain.Trailers{Item: "gh-pr-34", Agent: "claude-code", Instance: instanceA}); err != nil {
		t.Fatal(err)
	}
	fix := e.git.Commit(fixTree, "Remove the unused import")
	token, err := e.tools.Issue(app.ToolSession{Role: domain.RoleFixAgent, PR: ghPR(34), Round: 2, Worktree: fixTree, Branch: "feature/login", Findings: []string{"f1", "f2"}})
	if err != nil {
		t.Fatal(err)
	}

	refused(t, e.call(token, domain.ToolFinishFixes, `{"results":[{"finding":"f1","status":"fixed","commit":"`+fix.Hash[:7]+`"}]}`),
		"no result for finding f2: report each finding as fixed or skipped")
	refused(t, e.call(token, domain.ToolFinishFixes, `{"results":[{"finding":"f1","status":"fixed","commit":"abcdef1"},{"finding":"f2","status":"skipped","reason":"r"}]}`),
		"finding f1: commit abcdef1 is not a new commit of this fix pass")
	refused(t, e.call(token, domain.ToolFinishFixes, `{"results":[{"finding":"f1","status":"fixed","commit":"`+fix.Hash+`"},{"finding":"f2","status":"skipped"}]}`),
		"finding f2 is skipped without a reason")
	refused(t, e.call(token, domain.ToolFinishFixes, `{"results":[{"finding":"f3","status":"skipped","reason":"r"}]}`),
		`finding "f3" was not given to this fix pass`)

	var out acceptedOut
	e.mustCall(token, domain.ToolFinishFixes, `{"results":[{"finding":"f2","status":"skipped","reason":"needs an API decision"},{"finding":"f1","status":"fixed","commit":"`+fix.Hash[:7]+`"}]}`, &out)
	if len(e.fixes.got) != 1 {
		t.Fatalf("fix reports = %v", e.fixes.got)
	}
	r := e.fixes.got[0]
	want := []domain.FixResult{{Finding: "f1", Fixed: true, Commit: fix.Hash}, {Finding: "f2", Reason: "needs an API decision"}}
	if r.PR != ghPR(34) || r.Round != 2 || len(r.Results) != 2 || r.Results[0] != want[0] || r.Results[1] != want[1] {
		t.Errorf("handed over = %+v, want %+v", r, want)
	}
	if out.Message != "1 fixed and 1 skipped: Anthill verifies the fixes before pushing" {
		t.Errorf("result = %+v", out)
	}
}
