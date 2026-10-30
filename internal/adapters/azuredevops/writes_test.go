package azuredevops_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// patchDoc decodes a JSON Patch request body.
func patchDoc(t *testing.T, body string) []map[string]any {
	t.Helper()
	var doc []map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("patch body %q: %v", body, err)
	}
	return doc
}

// A work item comment is posted as Markdown, with its marker as written.
func TestAddCommentOnWorkItem(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST "+commentsPath, http.StatusOK, "comment_created.json")
	c := comment(domain.CommentStatus, "")
	got, err := e.a.AddComment(ctx(t), ado12, c)
	if err != nil || got.ID != "20" || got.Author != "m.rosani@contoso.com" {
		t.Fatalf("AddComment() = %+v, %v", got, err)
	}
	req := e.srv.matching(http.MethodPost, commentsPath)[0]
	var body struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal([]byte(req.Body), &body)
	if body.Text != c.String() || !strings.Contains(req.Body, "<!-- anthill {") {
		t.Errorf("body = %s", req.Body)
	}
	if !strings.Contains(req.Query, "format=markdown") || !strings.Contains(req.Query, "api-version=7.1-preview.4") {
		t.Errorf("query = %q", req.Query)
	}
	if n := e.srv.count(http.MethodGet, commentsPath); n != 0 {
		t.Errorf("listed comments %d times for a comment without marker ID", n)
	}
}

// No comment with a marker ID is posted twice (design D7).
func TestAddCommentIsIdempotentByMarkerID(t *testing.T) {
	e := newEnv(t)
	c := comment(domain.CommentStatus, "status-1")
	text, _ := json.Marshal(c.String())
	// The first listing has no comment with the marker ID; the second
	// lists the one just posted.
	e.srv.sequence("GET "+commentsPath,
		respond(t, http.StatusOK, "comments12_claims.json"),
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"comments":[{"id":20,"text":` + string(text) +
				`,"createdBy":{"uniqueName":"m.rosani@contoso.com"},"createdDate":"2026-10-29T10:00:00Z","modifiedDate":"2026-10-29T10:00:00Z"}]}`))
		})
	e.srv.fixture("POST "+commentsPath, http.StatusOK, "comment_created.json")
	for range 2 {
		if posted, err := e.a.AddComment(ctx(t), ado12, c); err != nil || posted.ID != "20" {
			t.Fatalf("AddComment() = %+v, %v", posted, err)
		}
	}
	if n := e.srv.count(http.MethodPost, commentsPath); n != 1 {
		t.Errorf("posted %d times, want 1", n)
	}
}

func TestEditCommentOnWorkItem(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("PATCH "+commentsPath+"/20", http.StatusOK, "comment_created.json")
	if _, err := e.a.EditComment(ctx(t), ado12, "20", comment(domain.CommentStatus, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.EditComment(ctx(t), ado12, "7.1", comment(domain.CommentStatus, "")); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("thread comment id on a work item: %v", err)
	}
}

// Tags are changed with a test on the revision read, and a tag already
// present in another case is not added again.
func TestLabelsOnWorkItem(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+itemPath, http.StatusOK, "workitem12_tags.json")
	e.srv.fixture("PATCH "+itemPath, http.StatusOK, "workitem12_patched.json")
	if err := e.a.AddLabel(ctx(t), ado12, "anthill:hold"); err != nil {
		t.Fatal(err)
	}
	if err := e.a.AddLabel(ctx(t), ado12, "ANTHILL"); err != nil {
		t.Fatal(err)
	}
	if err := e.a.RemoveLabel(ctx(t), ado12, "Repo:Web"); err != nil {
		t.Fatal(err)
	}
	if err := e.a.RemoveLabel(ctx(t), ado12, "missing"); err != nil {
		t.Fatal(err)
	}
	patches := e.srv.matching(http.MethodPatch, itemPath)
	if len(patches) != 2 {
		t.Fatalf("sent %d patches, want 2", len(patches))
	}
	if ct := patches[0].Header.Get("Content-Type"); ct != "application/json-patch+json" {
		t.Errorf("Content-Type = %q", ct)
	}
	add, remove := patchDoc(t, patches[0].Body), patchDoc(t, patches[1].Body)
	if add[0]["op"] != "test" || add[0]["path"] != "/rev" || add[0]["value"] != 7.0 || add[1]["value"] != "anthill; repo:Web; anthill:hold" {
		t.Errorf("add = %v", add)
	}
	if remove[1]["path"] != "/fields/System.Tags" || remove[1]["value"] != "anthill" {
		t.Errorf("remove = %v", remove)
	}
}

// A tag change that is not a claim reads the item again after a revision
// conflict, at most three more times.
func TestLabelRetriesOnConflict(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+itemPath, http.StatusOK, "workitem12_tags.json")
	e.srv.sequence("PATCH "+itemPath,
		respond(t, http.StatusPreconditionFailed, "conflict.json"),
		respond(t, http.StatusOK, "workitem12_patched.json"))
	if err := e.a.AddLabel(ctx(t), ado12, "anthill:hold"); err != nil {
		t.Fatalf("AddLabel() after one conflict: %v", err)
	}
	if n := e.srv.count(http.MethodGet, itemPath); n != 2 {
		t.Errorf("read the item %d times, want 2", n)
	}

	e = newEnv(t)
	e.srv.fixture("GET "+itemPath, http.StatusOK, "workitem12_tags.json")
	e.srv.fixture("PATCH "+itemPath, http.StatusConflict, "conflict.json")
	err := e.a.AddLabel(ctx(t), ado12, "anthill:hold")
	if !errors.Is(err, ports.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
	if n := e.srv.count(http.MethodPatch, itemPath); n != 4 {
		t.Errorf("sent %d patches, want 4 (one try and three retries)", n)
	}
}
