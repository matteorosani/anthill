package azuredevops_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Claims lists only claim comments, by creation time and then comment ID,
// with the item's revision read first and Azure DevOps' time.
func TestClaims(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+itemPath, http.StatusOK, "workitem12_rev.json")
	e.srv.fixture("GET "+commentsPath, http.StatusOK, "comments12_claims.json")
	page, err := e.a.Claims(ctx(t), ado12)
	if err != nil {
		t.Fatal(err)
	}
	if page.Revision != "7" || !page.ServerTime.Equal(serverDate) || len(page.Comments) != 2 {
		t.Fatalf("page = %+v", page)
	}
	// Both were created at the same second; comment 8 comes before 9.
	if page.Comments[0].ID != "8" || page.Comments[1].ID != "9" {
		t.Errorf("order = %s, %s", page.Comments[0].ID, page.Comments[1].ID)
	}
	sent := e.srv.sent()
	if sent[0].Path != itemPath || sent[1].Path != commentsPath {
		t.Errorf("revision must be read before the comments: %s then %s", sent[0].Path, sent[1].Path)
	}
}

// Scenario "Azure DevOps optimistic concurrency": the claim tag and the
// assignment are sent in one JSON Patch with a test on /rev, and a
// revision conflict counts as a lost race, never retried.
func TestAzureDevOpsOptimisticConcurrency(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusPreconditionFailed} {
		e := newEnv(t)
		e.srv.fixture("GET /contoso/_apis/connectionData", http.StatusOK, "connection_data.json")
		e.srv.fixture("GET "+itemPath, http.StatusOK, "workitem12_unassigned.json")
		e.srv.fixture("PATCH "+itemPath, status, "conflict.json")
		assigned, err := e.a.Mark(ctx(t), ado12, ports.ClaimMark{Label: "anthill:claimed", Assign: true, Revision: "6"})
		if !errors.Is(err, ports.ErrConflict) || assigned {
			t.Errorf("%d: Mark() = %v, %v; want a lost race", status, assigned, err)
		}
		patches := e.srv.matching(http.MethodPatch, itemPath)
		if len(patches) != 1 {
			t.Fatalf("%d: sent %d patches, want 1", status, len(patches))
		}
		doc := patchDoc(t, patches[0].Body)
		if len(doc) != 3 || doc[0]["op"] != "test" || doc[0]["path"] != "/rev" || doc[0]["value"] != 6.0 ||
			doc[1]["path"] != "/fields/System.Tags" || doc[1]["value"] != "anthill; anthill:claimed" ||
			doc[2]["path"] != "/fields/System.AssignedTo" || doc[2]["value"] != "m.rosani@contoso.com" {
			t.Errorf("%d: patch = %v", status, doc)
		}
	}
}

// Mark assigns the developer only when Assigned To is empty, and says so.
func TestMarkAssignsWhenUnassigned(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /contoso/_apis/connectionData", http.StatusOK, "connection_data.json")
	e.srv.fixture("GET "+itemPath, http.StatusOK, "workitem12_unassigned.json")
	e.srv.fixture("PATCH "+itemPath, http.StatusOK, "workitem12_claimed.json")
	assigned, err := e.a.Mark(ctx(t), ado12, ports.ClaimMark{Label: "anthill:claimed", Assign: true, Revision: "7"})
	if err != nil || !assigned {
		t.Errorf("Mark() = %v, %v; want assigned", assigned, err)
	}
}

// Assigned To holds one person on Azure DevOps. Mark never replaces
// another person, nor re-assigns the developer, and reports that Anthill
// made no assignment, so that the release will not unassign anyone.
func TestMarkKeepsAnotherAssignee(t *testing.T) {
	for _, file := range []string{"workitem12_other_assignee.json", "workitem12_tags.json"} {
		e := newEnv(t)
		e.srv.fixture("GET /contoso/_apis/connectionData", http.StatusOK, "connection_data.json")
		e.srv.fixture("GET "+itemPath, http.StatusOK, file)
		e.srv.fixture("PATCH "+itemPath, http.StatusOK, "workitem12_claimed.json")
		assigned, err := e.a.Mark(ctx(t), ado12, ports.ClaimMark{Label: "anthill:claimed", Assign: true, Revision: "7"})
		if err != nil || assigned {
			t.Errorf("%s: Mark() = %v, %v; want not assigned", file, assigned, err)
		}
		doc := patchDoc(t, e.srv.matching(http.MethodPatch, itemPath)[0].Body)
		if len(doc) != 2 || doc[1]["path"] != "/fields/System.Tags" {
			t.Errorf("%s: patch = %v, want the tag only", file, doc)
		}
	}
}

// Unmark removes the tag and, when asked, the developer's own assignment;
// a revision conflict is retried, because a release must not fail.
func TestUnmark(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /contoso/_apis/connectionData", http.StatusOK, "connection_data.json")
	e.srv.fixture("GET "+itemPath, http.StatusOK, "workitem12_claimed.json")
	e.srv.sequence("PATCH "+itemPath,
		respond(t, http.StatusPreconditionFailed, "conflict.json"),
		respond(t, http.StatusOK, "workitem12_patched.json"))
	if err := e.a.Unmark(ctx(t), ado12, "anthill:claimed", true); err != nil {
		t.Fatal(err)
	}
	patches := e.srv.matching(http.MethodPatch, itemPath)
	if len(patches) != 2 {
		t.Fatalf("sent %d patches, want 2", len(patches))
	}
	doc := patchDoc(t, patches[1].Body)
	if len(doc) != 3 || doc[0]["value"] != 8.0 || doc[1]["value"] != "anthill" ||
		doc[2]["op"] != "remove" || doc[2]["path"] != "/fields/System.AssignedTo" {
		t.Errorf("patch = %v", doc)
	}
	if _, hasValue := doc[2]["value"]; hasValue {
		t.Errorf("remove carries a value: %v", doc[2])
	}
}

func TestPostAndRetractOnWorkItem(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+commentsPath, http.StatusOK, "comments12_claims.json")
	e.srv.fixture("POST "+commentsPath, http.StatusOK, "comment_created.json")
	e.srv.handle("DELETE "+commentsPath+"/20", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	c, err := e.a.Post(ctx(t), ado12, comment(domain.CommentClaim, "claim-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.a.Retract(ctx(t), ado12, c.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.srv.count(http.MethodDelete, commentsPath+"/20"); n != 1 {
		t.Errorf("deleted %d times", n)
	}
}

// A review claim on a PR is a general comment and a label, with no
// revision and no assignee.
func TestClaimsOnPullRequest(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+prPath+"/threads", http.StatusOK, "threads34_claims.json")
	e.srv.fixture("POST "+prPath+"/labels", http.StatusOK, "label_created.json")
	e.srv.handle("DELETE "+prPath+"/threads/20/comments/1", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	page, err := e.a.Claims(ctx(t), pr34)
	if err != nil {
		t.Fatal(err)
	}
	if page.Revision != "" || len(page.Comments) != 2 || page.Comments[0].ID != "20.1" || page.Comments[1].ID != "21.1" {
		t.Fatalf("page = %+v", page)
	}
	if assigned, err := e.a.Mark(ctx(t), pr34, ports.ClaimMark{Label: "anthill:claimed"}); err != nil || assigned {
		t.Errorf("Mark() = %v, %v", assigned, err)
	}
	if err := e.a.Retract(ctx(t), pr34, "20.1"); err != nil {
		t.Error(err)
	}
	if _, err := e.a.Mark(ctx(t), pr34, ports.ClaimMark{Label: "anthill:claimed", Assign: true}); err == nil {
		t.Error("Mark with Assign on a PR succeeded")
	}
}
