package azuredevops_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

// Tasks 16.6 and 16.7: reading one PR tells whether it is open, merged
// (completed) or closed (abandoned), and a PR from a fork is never
// pushable, since Azure DevOps has no maintainer edits.
func TestPullRequestStateAndFork(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		state domain.PRState
		fork  bool
	}{
		{"active", `"status":"active"`, domain.PROpen, false},
		{"completed", `"status":"completed"`, domain.PRMerged, false},
		{"abandoned", `"status":"abandoned"`, domain.PRClosed, false},
		{"fork", `"status":"active","forkSource":{"name":"refs/heads/feature/login"}`, domain.PROpen, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.srv.handle("GET "+prPath, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"pullRequestId":34,"sourceRefName":"refs/heads/feature/login","targetRefName":"refs/heads/main",%s}`, c.extra)
			})
			e.srv.fixture("GET "+prPath+"/threads", http.StatusOK, "threads_none.json")
			e.srv.fixture("GET "+prPath+"/workitems", http.StatusOK, "pr_workitems_none.json")
			got, _, err := e.a.PullRequest(ctx(t), pr34)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != c.state || got.Fork != c.fork || got.MaintainerCanModify || got.ForkURL != "" {
				t.Errorf("state, fork = %v, %v, %v, %q; want %v, %v", got.State, got.Fork, got.MaintainerCanModify, got.ForkURL, c.state, c.fork)
			}
		})
	}
}
