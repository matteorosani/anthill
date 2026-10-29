package github_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// body decodes the JSON body of a recorded request.
func body(t *testing.T, r request) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(r.Body), &v); err != nil {
		t.Fatalf("%s %s: body %q: %v", r.Method, r.Path, r.Body, err)
	}
	return v
}

// last returns the last request whose method and path match.
func last(t *testing.T, e *env, method, path string) request {
	t.Helper()
	sent := e.srv.sent()
	for i := len(sent) - 1; i >= 0; i-- {
		if sent[i].Method == method && sent[i].Path == path {
			return sent[i]
		}
	}
	t.Fatalf("no %s %s among %d requests", method, path, len(sent))
	return request{}
}

// Task 7.4: a comment is posted with its prefix, body and marker.
func TestAddComment(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST /repos/matteorosani/anthill/issues/12/comments", http.StatusCreated, "comment_created.json")

	c := comment(domain.CommentQuestion, "")
	got, err := e.a.AddComment(ctx(t), gh12, c)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "9010" || got.Author != "mrosani" {
		t.Errorf("comment = %+v", got)
	}
	if b := body(t, last(t, e, "POST", "/repos/matteorosani/anthill/issues/12/comments")); b["body"] != c.String() {
		t.Errorf("body = %q, want %q", b["body"], c.String())
	}
	// Without a marker ID nothing is looked up first.
	if n := len(e.srv.sent()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

// Task 7.4 and design D7: a comment whose marker ID is already on the
// item is not posted again.
func TestAddCommentIsIdempotentByMarkerID(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12/comments", http.StatusOK, "issue12_comments_marked.json")
	e.srv.fixture("POST /repos/matteorosani/anthill/issues/12/comments", http.StatusCreated, "comment_created.json")

	got, err := e.a.AddComment(ctx(t), gh12, comment(domain.CommentStatus, "status-run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "9003" {
		t.Errorf("comment = %+v, want the existing 9003", got)
	}
	if n := e.srv.count("POST", "/repos/matteorosani/anthill/issues/12/comments"); n != 0 {
		t.Errorf("posted %d times", n)
	}

	got, err = e.a.AddComment(ctx(t), gh12, comment(domain.CommentStatus, "status-run-2"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "9010" || e.srv.count("POST", "/repos/matteorosani/anthill/issues/12/comments") != 1 {
		t.Errorf("a new marker ID was not posted: %+v", got)
	}
}

// Task 7.4: comment edits go to the issue or the review comment endpoint.
func TestEditComment(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("PATCH /repos/matteorosani/anthill/issues/comments/9002", http.StatusOK, "comment_edited.json")
	e.srv.fixture("PATCH /repos/matteorosani/anthill/pulls/comments/7001", http.StatusOK, "review_comment_created.json")

	c := comment(domain.CommentClaim, "")
	got, err := e.a.EditComment(ctx(t), gh12, "9002", c)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "9002" || got.UpdatedAt.Equal(got.CreatedAt) {
		t.Errorf("edited = %+v", got)
	}
	if b := body(t, last(t, e, "PATCH", "/repos/matteorosani/anthill/issues/comments/9002")); b["body"] != c.String() {
		t.Errorf("body = %q", b["body"])
	}

	got, err = e.a.EditComment(ctx(t), pr34, "r7001", c)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "r7010" {
		t.Errorf("edited review comment = %+v", got)
	}
	if _, err := e.a.EditComment(ctx(t), gh12, "x1", c); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("invalid id: err = %v", err)
	}
}

// Task 7.4: labels are added and removed; removing a missing label does
// nothing.
func TestLabels(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST /repos/matteorosani/anthill/issues/12/labels", http.StatusOK, "labels.json")
	e.srv.fixture("DELETE /repos/matteorosani/anthill/issues/12/labels/anthill:hold", http.StatusNotFound, "label_missing.json")
	e.srv.fixture("DELETE /repos/matteorosani/anthill/issues/12/labels/needs%20review", http.StatusOK, "labels.json")
	e.srv.fixture("DELETE /repos/matteorosani/anthill/issues/99/labels/anthill", http.StatusNotFound, "not_found.json")

	if err := e.a.AddLabel(ctx(t), gh12, "anthill:hold"); err != nil {
		t.Fatal(err)
	}
	if got := labelsSent(t, last(t, e, "POST", "/repos/matteorosani/anthill/issues/12/labels")); !slices.Equal(got, []string{"anthill:hold"}) {
		t.Errorf("labels sent = %v", got)
	}
	if err := e.a.RemoveLabel(ctx(t), gh12, "anthill:hold"); err != nil {
		t.Errorf("missing label: %v", err)
	}
	if err := e.a.RemoveLabel(ctx(t), gh12, "needs review"); err != nil {
		t.Errorf("label with a space: %v", err)
	}
	if err := e.a.RemoveLabel(ctx(t), item(99), "anthill"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("missing issue: err = %v", err)
	}
}

// labelsSent decodes the label list of an add-labels request.
func labelsSent(t *testing.T, r request) []string {
	t.Helper()
	var labels []string
	if err := json.Unmarshal([]byte(r.Body), &labels); err != nil {
		t.Fatalf("labels body %q: %v", r.Body, err)
	}
	return labels
}

// Task 7.4: a PR is opened with the body Anthill built, which holds the
// closing reference, and labelled anthill (spec ant-tools, "Agent opens
// a ready PR").
func TestOpenPullRequest(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST /repos/matteorosani/anthill/pulls", http.StatusCreated, "pull_created.json")
	e.srv.fixture("POST /repos/matteorosani/anthill/issues/40/labels", http.StatusOK, "labels.json")

	got, err := e.a.OpenPullRequest(ctx(t), ports.NewPullRequest{
		Repo:   domain.Repository{Provider: domain.GitHub, Org: "MatteoRosani", Name: "Anthill"},
		Source: "ant/gh-12-fix-login-timeout-on-safari",
		Target: "main",
		Title:  "Fix login timeout on Safari",
		Body:   "Keeps the session alive.\n\nCloses #12",
		Draft:  true,
		Labels: []string{"anthill"},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := body(t, last(t, e, "POST", "/repos/matteorosani/anthill/pulls"))
	if b["head"] != "ant/gh-12-fix-login-timeout-on-safari" || b["base"] != "main" || b["draft"] != true ||
		!strings.HasSuffix(b["body"].(string), "Closes #12") {
		t.Errorf("create body = %v", b)
	}
	if got := labelsSent(t, last(t, e, "POST", "/repos/matteorosani/anthill/issues/40/labels")); !slices.Equal(got, []string{"anthill"}) {
		t.Errorf("labels sent = %v", got)
	}
	if got.Ref != pr(40) || !got.Draft || got.Item == nil || *got.Item != item(12) || !slices.Contains(got.Labels, "anthill") {
		t.Errorf("pr = %+v", got)
	}
}

// Task 7.4: a second PR for the same branch is a conflict.
func TestOpenPullRequestThatExists(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST /repos/matteorosani/anthill/pulls", http.StatusUnprocessableEntity, "pull_exists.json")
	_, err := e.a.OpenPullRequest(ctx(t), ports.NewPullRequest{Repo: repo, Source: "ant/gh-12-x", Target: "main", Title: "t"})
	if !errors.Is(err, ports.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

// Task 7.4: title and body go over REST; draft and ready over GraphQL,
// and only when the state changes.
func TestUpdatePullRequest(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("PATCH /repos/matteorosani/anthill/pulls/34", http.StatusOK, "pull34.json")
	e.srv.fixture("GET /repos/matteorosani/anthill/pulls/34", http.StatusOK, "pull34.json")
	e.srv.graphql(
		"convertPullRequestToDraft", "graphql_draft.json",
		"markPullRequestReadyForReview", "graphql_ready.json",
	)
	title := "New title"
	if _, err := e.a.UpdatePullRequest(ctx(t), pr34, ports.PullRequestUpdate{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if b := body(t, last(t, e, "PATCH", "/repos/matteorosani/anthill/pulls/34")); b["title"] != title || b["body"] != nil {
		t.Errorf("patch body = %v", b)
	}

	// Spec ant-execution, "Draft PRs for incomplete work".
	draft := true
	got, err := e.a.UpdatePullRequest(ctx(t), pr34, ports.PullRequestUpdate{Draft: &draft})
	if err != nil {
		t.Fatal(err)
	}
	q := last(t, e, "POST", "/graphql")
	if !strings.Contains(q.Body, "convertPullRequestToDraft") || !strings.Contains(q.Body, "PR_kwDOA034") {
		t.Errorf("mutation = %s", q.Body)
	}
	if !got.Draft {
		t.Error("PR is not a draft")
	}

	ready := false
	before := e.srv.count("POST", "/graphql")
	if _, err := e.a.UpdatePullRequest(ctx(t), pr34, ports.PullRequestUpdate{Draft: &ready}); err != nil {
		t.Fatal(err)
	}
	if e.srv.count("POST", "/graphql") != before {
		t.Error("a ready PR was marked ready again")
	}
}

// Task 7.4: an inline finding is posted on the head commit over a line
// range, idempotently by marker ID.
func TestAddReviewComment(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /repos/matteorosani/anthill/pulls/34/comments", http.StatusOK, "review_comments_marked.json")
	e.srv.fixture("GET /repos/matteorosani/anthill/pulls/34", http.StatusOK, "pull34.json")
	e.srv.fixture("POST /repos/matteorosani/anthill/pulls/34/comments", http.StatusCreated, "review_comment_created.json")

	finding := comment(domain.CommentFinding, "finding-new")
	got, err := e.a.AddReviewComment(ctx(t), pr34, ports.ReviewComment{Path: "web/session.go", StartLine: 40, EndLine: 44, Comment: finding})
	if err != nil {
		t.Fatal(err)
	}
	b := body(t, last(t, e, "POST", "/repos/matteorosani/anthill/pulls/34/comments"))
	if b["commit_id"] != "1111111111111111111111111111111111111111" || b["path"] != "web/session.go" ||
		b["start_line"] != 40.0 || b["line"] != 44.0 || b["side"] != "RIGHT" || b["body"] != finding.String() {
		t.Errorf("body = %v", b)
	}
	if got.ID != "r7010" {
		t.Errorf("comment = %+v", got)
	}

	got, err = e.a.AddReviewComment(ctx(t), pr34, ports.ReviewComment{Path: "web/session.go", StartLine: 44, Comment: comment(domain.CommentFinding, "finding-abc")})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "r7005" || e.srv.count("POST", "/repos/matteorosani/anthill/pulls/34/comments") != 1 {
		t.Errorf("existing finding posted again: %+v", got)
	}
}

// Task 7.4: a single-line finding has no start line.
func TestAddReviewCommentSingleLine(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /repos/matteorosani/anthill/pulls/34", http.StatusOK, "pull34.json")
	e.srv.fixture("POST /repos/matteorosani/anthill/pulls/34/comments", http.StatusCreated, "review_comment_created.json")
	if _, err := e.a.AddReviewComment(ctx(t), pr34, ports.ReviewComment{Path: "a.go", StartLine: 3, Comment: comment(domain.CommentFinding, "")}); err != nil {
		t.Fatal(err)
	}
	b := body(t, last(t, e, "POST", "/repos/matteorosani/anthill/pulls/34/comments"))
	if b["line"] != 3.0 || b["start_line"] != nil {
		t.Errorf("body = %v", b)
	}
}

// Task 7.4: thread replies go over GraphQL, idempotently by marker ID.
func TestReplyToThread(t *testing.T) {
	e := newEnv(t)
	e.srv.graphql(
		"addPullRequestReviewThreadReply", "graphql_reply.json",
		"PullRequestReviewThread", "graphql_thread_comments_marked.json",
	)
	reply := comment(domain.CommentReply, "reply-new")
	got, err := e.a.ReplyToThread(ctx(t), pr34, "PRRT_kwDOA001", reply)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "r7011" || got.Author != "mrosani" {
		t.Errorf("reply = %+v", got)
	}
	q := last(t, e, "POST", "/graphql")
	var sent struct {
		Variables map[string]string `json:"variables"`
	}
	if err := json.Unmarshal([]byte(q.Body), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Variables["id"] != "PRRT_kwDOA001" || sent.Variables["body"] != reply.String() {
		t.Errorf("variables = %v", sent.Variables)
	}

	before := e.srv.count("POST", "/graphql")
	got, err = e.a.ReplyToThread(ctx(t), pr34, "PRRT_kwDOA001", comment(domain.CommentReply, "reply-7001"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "r7006" || e.srv.count("POST", "/graphql") != before+1 {
		t.Errorf("existing reply posted again: %+v", got)
	}
}

// Task 7.4: a thread is resolved over GraphQL; an unknown one is not
// found.
func TestResolveThread(t *testing.T) {
	e := newEnv(t)
	e.srv.graphql(
		"PRRT_missing", "graphql_not_found.json",
		"resolveReviewThread", "graphql_resolve.json",
	)
	if err := e.a.ResolveThread(ctx(t), pr34, "PRRT_kwDOA001"); err != nil {
		t.Fatal(err)
	}
	if q := last(t, e, "POST", "/graphql"); !strings.Contains(q.Body, "PRRT_kwDOA001") {
		t.Errorf("mutation = %s", q.Body)
	}
	if err := e.a.ResolveThread(ctx(t), pr34, "PRRT_missing"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
