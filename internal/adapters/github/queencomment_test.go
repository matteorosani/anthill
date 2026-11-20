package github_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Spec queen-console, "Answer from the console": the queen's answer is
// posted as the queen's own text, with no prefix and no marker.
func TestPostAsQueen(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST /repos/matteorosani/anthill/issues/12/comments", http.StatusCreated, "comment_created.json")
	e.srv.graphql("addPullRequestReviewThreadReply", "graphql_reply.json")

	got, err := e.a.PostAsQueen(ctx(t), gh12, "", "> Which timeout?\n\n30 seconds.")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "9010" || got.Author != "mrosani" {
		t.Errorf("comment = %+v", got)
	}
	if b := body(t, last(t, e, "POST", "/repos/matteorosani/anthill/issues/12/comments")); b["body"] != "> Which timeout?\n\n30 seconds." {
		t.Errorf("body = %q", b["body"])
	}

	got, err = e.a.PostAsQueen(ctx(t), pr34, "PRRT_kwDOA001", "30 seconds.")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "r7011" {
		t.Errorf("reply = %+v", got)
	}
	var sent struct {
		Variables map[string]string `json:"variables"`
	}
	if err := json.Unmarshal([]byte(last(t, e, "POST", "/graphql").Body), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Variables["id"] != "PRRT_kwDOA001" || sent.Variables["body"] != "30 seconds." {
		t.Errorf("variables = %v", sent.Variables)
	}
}
