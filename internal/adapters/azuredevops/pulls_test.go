package azuredevops_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// prRoutes registers the list of PRs 34 and 35 with their threads and
// linked work items: 34 has threads and a native link to 12, 35 none.
func prRoutes(t *testing.T, s *server) {
	s.fixture("GET "+prsPath, http.StatusOK, "pulls.json")
	s.fixture("GET "+prPath+"/threads", http.StatusOK, "threads34.json")
	s.fixture("GET "+prPath+"/workitems", http.StatusOK, "pr_workitems34.json")
	s.fixture("GET /contoso/shop/_apis/git/repositories/web/pullrequests/35/threads", http.StatusOK, "threads_none.json")
	s.fixture("GET /contoso/shop/_apis/git/repositories/web/pullrequests/35/workitems", http.StatusOK, "pr_workitems_none.json")
}

func TestPullRequestsPoll(t *testing.T) {
	e := newEnv(t)
	prRoutes(t, e.srv)
	page, err := e.a.PullRequests(ctx(t), domain.Repository{Provider: domain.AzureDevOps, Org: "Contoso", Project: "Shop", Name: "Web"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.PullRequests) != 2 || !page.ServerTime.Equal(serverDate) {
		t.Fatalf("page = %+v", page)
	}
	pr := page.PullRequests[0]
	if pr.Ref != pr34 || pr.SourceBranch != "ant/ado-12-fix-login" || pr.TargetBranch != "main" || pr.HeadCommit != "c0ffee" ||
		pr.Author != "m.rosani@contoso.com" || pr.Draft || len(pr.Labels) != 1 || pr.Labels[0] != "anthill" ||
		pr.URL != e.srv.srv.URL+"/contoso/shop/_git/web/pullrequest/34" || pr.Item == nil || *pr.Item != ado12 {
		t.Errorf("pr = %+v", pr)
	}
	// Anchored threads are review threads; the general one is a comment;
	// the vote thread written by Azure DevOps is left out.
	if len(pr.Threads) != 2 || len(pr.Comments) != 1 {
		t.Fatalf("threads = %+v\ncomments = %+v", pr.Threads, pr.Comments)
	}
	active, fixed := pr.Threads[0], pr.Threads[1]
	if active.ID != "7" || active.Path != "src/auth.ts" || active.Line != 42 || active.Resolved || len(active.Comments) != 2 ||
		active.Comments[0].ID != "7.1" || active.Comments[0].Author != "ada@contoso.com" {
		t.Errorf("active thread = %+v", active)
	}
	if fixed.ID != "8" || fixed.Line != 5 || !fixed.Resolved {
		t.Errorf("fixed thread = %+v", fixed)
	}
	if c := pr.Comments[0]; c.ID != "9.1" || c.Body != "Looks good overall." ||
		c.URL != e.srv.srv.URL+"/contoso/shop/_git/web/pullrequest/34?discussionId=9" {
		t.Errorf("comment = %+v", c)
	}
	if !pr.UpdatedAt.Equal(time.Date(2026, 10, 29, 8, 40, 0, 0, time.UTC)) || pr.Revision == "" {
		t.Errorf("updated %v, revision %q", pr.UpdatedAt, pr.Revision)
	}
	if page.PullRequests[1].Item != nil {
		t.Errorf("PR 35 is linked to %v", page.PullRequests[1].Item)
	}
	if q := e.srv.matching(http.MethodGet, prsPath)[0].Query; !strings.Contains(q, "searchCriteria.status=active") {
		t.Errorf("query = %q", q)
	}

	// Nothing changed: the next poll returns no PR.
	next, err := e.a.PullRequests(ctx(t), web, page.Cursor)
	if err != nil || len(next.PullRequests) != 0 {
		t.Errorf("unchanged poll = %+v, %v", next.PullRequests, err)
	}
	// A cursor that knew PR 34 with other content returns it alone.
	var c struct {
		PRs map[string]string `json:"prs"`
	}
	_ = json.Unmarshal([]byte(page.Cursor), &c)
	c.PRs["34"] = "old"
	stale, _ := json.Marshal(c)
	if changed, _ := e.a.PullRequests(ctx(t), web, string(stale)); len(changed.PullRequests) != 1 || changed.PullRequests[0].Ref != pr34 {
		t.Errorf("poll after a change = %+v", changed.PullRequests)
	}
}

func TestPullRequestRead(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+prPath, http.StatusOK, "pull34.json")
	e.srv.fixture("GET "+prPath+"/threads", http.StatusOK, "threads_none.json")
	e.srv.fixture("GET "+prPath+"/workitems", http.StatusOK, "pr_workitems_none.json")
	pr, now, err := e.a.PullRequest(ctx(t), domain.Ref{Repo: domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "Shop", Name: "WEB"}, Kind: domain.KindPR, Number: 34})
	if err != nil || pr.Ref != pr34 || !now.Equal(serverDate) {
		t.Fatalf("PullRequest() = %+v, %v, %v", pr, now, err)
	}
	// No native link: "Closes AB#12" in the description links it.
	if pr.Item == nil || *pr.Item != ado12 {
		t.Errorf("item = %v", pr.Item)
	}
}

// OpenPullRequest sends the branches as refs, the labels, the draft state
// and a native link to the work item.
func TestOpenPullRequest(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST "+prsPath, http.StatusCreated, "pull_created.json")
	item := ado12
	pr, err := e.a.OpenPullRequest(ctx(t), ports.NewPullRequest{
		Repo: web, Source: "ant/ado-12-fix-login", Target: "main", Title: "Fix login timeout",
		Body: "Closes AB#12", Draft: true, Labels: []string{"anthill"}, Item: &item,
	})
	if err != nil || pr.Ref.Number != 36 || !pr.Draft || pr.Item == nil || *pr.Item != ado12 {
		t.Fatalf("OpenPullRequest() = %+v, %v", pr, err)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(e.srv.matching(http.MethodPost, prsPath)[0].Body), &body)
	refs, _ := json.Marshal(body["workItemRefs"])
	labels, _ := json.Marshal(body["labels"])
	if body["sourceRefName"] != "refs/heads/ant/ado-12-fix-login" || body["targetRefName"] != "refs/heads/main" ||
		body["isDraft"] != true || body["description"] != "Closes AB#12" ||
		string(refs) != `[{"id":"12"}]` || string(labels) != `[{"name":"anthill"}]` {
		t.Errorf("body = %v", body)
	}
}

func TestOpenPullRequestThatExists(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST "+prsPath, http.StatusConflict, "pull_exists.json")
	_, err := e.a.OpenPullRequest(ctx(t), ports.NewPullRequest{Repo: web, Source: "ant/ado-12-fix-login", Target: "main"})
	if !errors.Is(err, ports.ErrConflict) || !strings.Contains(err.Error(), "TF401179") {
		t.Errorf("err = %v", err)
	}
}

// One PATCH sends the fields that are set; turning a PR into a draft is
// isDraft (spec ant-execution, "Draft PRs for incomplete work").
func TestUpdatePullRequest(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("PATCH "+prPath, http.StatusOK, "pull34_draft.json")
	draft := true
	pr, err := e.a.UpdatePullRequest(ctx(t), pr34, ports.PullRequestUpdate{Draft: &draft})
	if err != nil || !pr.Draft {
		t.Fatalf("UpdatePullRequest() = %+v, %v", pr, err)
	}
	if body := e.srv.matching(http.MethodPatch, prPath)[0].Body; strings.TrimSpace(body) != `{"isDraft":true}` {
		t.Errorf("body = %s", body)
	}
}

// A general PR comment is a new thread without status, so that it never
// blocks a "comments must be resolved" policy, and is posted once per
// marker ID.
func TestAddCommentOnPullRequest(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+prPath+"/threads", http.StatusOK, "threads34.json")
	e.srv.fixture("POST "+prPath+"/threads", http.StatusOK, "thread_created.json")
	got, err := e.a.AddComment(ctx(t), pr34, comment(domain.CommentQuestion, "q-1"))
	if err != nil || got.ID != "30.1" {
		t.Fatalf("AddComment() = %+v, %v", got, err)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(e.srv.matching(http.MethodPost, prPath+"/threads")[0].Body), &body)
	if _, ok := body["status"]; ok {
		t.Errorf("thread has a status: %v", body)
	}
	if _, ok := body["threadContext"]; ok {
		t.Errorf("general comment has a context: %v", body)
	}
}

func TestEditCommentOnPullRequest(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("PATCH "+prPath+"/threads/7/comments/1", http.StatusOK, "pr_comment_edited.json")
	got, err := e.a.EditComment(ctx(t), pr34, "7.1", comment(domain.CommentStatus, ""))
	if err != nil || got.ID != "7.1" || !got.UpdatedAt.After(got.CreatedAt) {
		t.Fatalf("EditComment() = %+v, %v", got, err)
	}
	if _, err := e.a.EditComment(ctx(t), pr34, "7", comment(domain.CommentStatus, "")); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("bad id: %v", err)
	}
}

// An inline finding is an active thread anchored to the lines of a file.
func TestAddReviewComment(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST "+prPath+"/threads", http.StatusOK, "thread_created.json")
	_, err := e.a.AddReviewComment(ctx(t), pr34, ports.ReviewComment{Path: "src/auth.ts", StartLine: 42, EndLine: 44, Comment: comment(domain.CommentFinding, "")})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Status        string `json:"status"`
		ThreadContext struct {
			FilePath       string `json:"filePath"`
			RightFileStart struct{ Line int }
			RightFileEnd   struct{ Line int }
		} `json:"threadContext"`
	}
	_ = json.Unmarshal([]byte(e.srv.matching(http.MethodPost, prPath+"/threads")[0].Body), &body)
	if body.Status != "active" || body.ThreadContext.FilePath != "/src/auth.ts" ||
		body.ThreadContext.RightFileStart.Line != 42 || body.ThreadContext.RightFileEnd.Line != 44 {
		t.Errorf("body = %+v", body)
	}
}

// A reply goes to the thread's first comment, once per marker ID.
func TestReplyToThread(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+prPath+"/threads/7", http.StatusOK, "thread7.json")
	e.srv.fixture("POST "+prPath+"/threads/7/comments", http.StatusOK, "pr_comment_created.json")
	got, err := e.a.ReplyToThread(ctx(t), pr34, "7", comment(domain.CommentReply, "r-1"))
	if err != nil || got.ID != "7.3" {
		t.Fatalf("ReplyToThread() = %+v, %v", got, err)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(e.srv.matching(http.MethodPost, prPath+"/threads/7/comments")[0].Body), &body)
	if body["parentCommentId"] != 1.0 {
		t.Errorf("body = %v", body)
	}
}

func TestResolveThread(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("PATCH "+prPath+"/threads/7", http.StatusOK, "thread_resolved.json")
	if err := e.a.ResolveThread(ctx(t), pr34, "7"); err != nil {
		t.Fatal(err)
	}
	if body := e.srv.matching(http.MethodPatch, prPath+"/threads/7")[0].Body; strings.TrimSpace(body) != `{"status":"fixed"}` {
		t.Errorf("body = %s", body)
	}
}

// PR labels use the labels API; removing a missing one is no error.
func TestLabelsOnPullRequest(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST "+prPath+"/labels", http.StatusOK, "label_created.json")
	e.srv.fixture("DELETE "+prPath+"/labels/anthill:review", http.StatusNotFound, "not_found.json")
	if err := e.a.AddLabel(ctx(t), pr34, "anthill:claimed"); err != nil {
		t.Error(err)
	}
	if err := e.a.RemoveLabel(ctx(t), pr34, "anthill:review"); err != nil {
		t.Error(err)
	}
}
