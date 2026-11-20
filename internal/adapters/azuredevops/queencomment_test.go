package azuredevops_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Spec queen-console, "Answer from the console": the queen's answer is
// posted as the queen's own text, with no prefix and no marker, in the
// question's thread on a PR and as a comment on a work item.
func TestPostAsQueen(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST "+commentsPath, http.StatusOK, "comment_created.json")
	e.srv.fixture("POST "+prPath+"/threads/7/comments", http.StatusOK, "pr_comment_created.json")
	e.srv.fixture("POST "+prPath+"/threads", http.StatusOK, "thread_created.json")

	got, err := e.a.PostAsQueen(ctx(t), ado12, "", "> Which timeout?\n\n30 seconds.")
	if err != nil || got.ID != "20" {
		t.Fatalf("on the item: %+v, %v", got, err)
	}
	var text struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal([]byte(e.srv.matching(http.MethodPost, commentsPath)[0].Body), &text)
	if text.Text != "> Which timeout?\n\n30 seconds." {
		t.Errorf("item comment = %q", text.Text)
	}

	got, err = e.a.PostAsQueen(ctx(t), pr34, "7", "30 seconds.")
	if err != nil || got.ID != "7.3" {
		t.Fatalf("in the thread: %+v, %v", got, err)
	}
	var reply map[string]any
	_ = json.Unmarshal([]byte(e.srv.matching(http.MethodPost, prPath+"/threads/7/comments")[0].Body), &reply)
	if reply["content"] != "30 seconds." || reply["parentCommentId"] != 1.0 {
		t.Errorf("reply = %v", reply)
	}

	if _, err := e.a.PostAsQueen(ctx(t), pr34, "", "30 seconds."); err != nil {
		t.Fatalf("new thread: %v", err)
	}
	var thread struct {
		Comments []map[string]any `json:"comments"`
	}
	_ = json.Unmarshal([]byte(e.srv.matching(http.MethodPost, prPath+"/threads")[0].Body), &thread)
	if len(thread.Comments) != 1 || thread.Comments[0]["content"] != "30 seconds." {
		t.Errorf("thread = %+v", thread)
	}

	if _, err := e.a.PostAsQueen(ctx(t), pr34, "x", "30 seconds."); err == nil {
		t.Error("an invalid thread id was accepted")
	}
}
