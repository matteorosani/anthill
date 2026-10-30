package github_test

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Claims returns the claim comments and the /anthill release commands,
// in creation order, with the labels and GitHub's time (design D3).
func TestClaims(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12/labels", http.StatusOK, "labels.json")
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12/comments", http.StatusOK, "issue12_claims.json")

	page, err := e.a.Claims(ctx(t), gh12)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Comments) != 2 || page.Comments[0].ID != "9002" || page.Comments[1].ID != "9003" {
		t.Errorf("claims = %+v, want the claim comment 9002 and the release command 9003", page.Comments)
	}
	if !slices.Equal(page.Labels, []string{"anthill", "anthill:claimed"}) {
		t.Errorf("labels = %v", page.Labels)
	}
	if !page.ServerTime.Equal(serverDate) || page.Revision != "" {
		t.Errorf("server time, revision = %v, %q", page.ServerTime, page.Revision)
	}
}

// Post posts a claim comment; Retract deletes it.
func TestPostAndRetract(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST /repos/matteorosani/anthill/issues/12/comments", http.StatusCreated, "comment_created.json")
	e.srv.handle("DELETE /repos/matteorosani/anthill/issues/comments/9010", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	e.srv.fixture("DELETE /repos/matteorosani/anthill/issues/comments/9011", http.StatusNotFound, "not_found.json")

	c, err := e.a.Post(ctx(t), gh12, comment(domain.CommentClaim, ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.a.Retract(ctx(t), gh12, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.a.Retract(ctx(t), gh12, "9011"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("gone comment: err = %v", err)
	}
	if err := e.a.Retract(ctx(t), pr34, "r7001"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("review comment: err = %v", err)
	}
}

// Edit renews a claim by editing its comment.
func TestEditRenewsClaim(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("PATCH /repos/matteorosani/anthill/issues/comments/9002", http.StatusOK, "comment_edited.json")
	got, err := e.a.Edit(ctx(t), gh12, "9002", comment(domain.CommentClaim, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Errorf("edited claim = %+v", got)
	}
}

// Task 7.4: Mark adds the claim label and assigns the authenticated user,
// reporting whether Anthill made the assignment (spec work-claiming,
// "Release").
func TestMarkAndUnmark(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST /repos/matteorosani/anthill/issues/12/labels", http.StatusOK, "labels.json")
	e.srv.fixture("GET /user", http.StatusOK, "user.json")
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12", http.StatusOK, "issue12.json")
	e.srv.fixture("POST /repos/matteorosani/anthill/issues/12/assignees", http.StatusCreated, "issue12_assigned.json")
	e.srv.fixture("DELETE /repos/matteorosani/anthill/issues/12/labels/anthill:claimed", http.StatusOK, "labels.json")
	e.srv.fixture("DELETE /repos/matteorosani/anthill/issues/12/assignees", http.StatusOK, "issue12.json")

	assigned, err := e.a.Mark(ctx(t), gh12, ports.ClaimMark{Label: "anthill:claimed", Assign: true})
	if err != nil {
		t.Fatal(err)
	}
	if !assigned {
		t.Error("assigned = false, want true: bob was the only assignee")
	}
	if got := labelsSent(t, last(t, e, "POST", "/repos/matteorosani/anthill/issues/12/labels")); !slices.Equal(got, []string{"anthill:claimed"}) {
		t.Errorf("labels sent = %v", got)
	}
	if b := body(t, last(t, e, "POST", "/repos/matteorosani/anthill/issues/12/assignees")); len(b["assignees"].([]any)) != 1 || b["assignees"].([]any)[0] != "MRosani" {
		t.Errorf("assignees body = %v", b)
	}

	if err := e.a.Unmark(ctx(t), gh12, "anthill:claimed", true); err != nil {
		t.Fatal(err)
	}
	if b := body(t, last(t, e, "DELETE", "/repos/matteorosani/anthill/issues/12/assignees")); b["assignees"].([]any)[0] != "MRosani" {
		t.Errorf("unassign body = %v", b)
	}
	if n := e.srv.count("GET", "/user"); n != 1 {
		t.Errorf("GET /user sent %d times, want once", n)
	}
}

// Mark reports no assignment when the user was already assigned, in any
// case of the login, so that release leaves the assignment alone.
func TestMarkAlreadyAssigned(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST /repos/matteorosani/anthill/issues/12/labels", http.StatusOK, "labels.json")
	e.srv.fixture("GET /user", http.StatusOK, "user.json")
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12", http.StatusOK, "issue12_assigned.json")

	assigned, err := e.a.Mark(ctx(t), gh12, ports.ClaimMark{Label: "anthill:claimed", Assign: true})
	if err != nil {
		t.Fatal(err)
	}
	if assigned {
		t.Error("assigned = true for a user already assigned")
	}
	if n := e.srv.count("POST", "/repos/matteorosani/anthill/issues/12/assignees"); n != 0 {
		t.Errorf("assigned again %d times", n)
	}
}

// A review claim on a PR gets the label only.
func TestMarkPullRequest(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST /repos/matteorosani/anthill/issues/34/labels", http.StatusOK, "labels.json")
	assigned, err := e.a.Mark(ctx(t), pr34, ports.ClaimMark{Label: "anthill:claimed"})
	if err != nil || assigned {
		t.Errorf("assigned, err = %v, %v", assigned, err)
	}
}
