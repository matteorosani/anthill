package github_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

// Tasks 16.6 and 16.7: reading one PR tells whether it is open, merged
// or closed, and whether its branch is in a fork that takes a push from
// the repository's maintainers.
func TestPullRequestStateAndFork(t *testing.T) {
	const same = `"repo":{"full_name":"MatteoRosani/Anthill","clone_url":"https://github.com/MatteoRosani/Anthill.git"}`
	const fork = `"repo":{"full_name":"Ada/Anthill","clone_url":"https://github.com/Ada/Anthill.git"}`
	fields := func(n int, head, extra string) string {
		return fmt.Sprintf(`{"number":%d,"body":"","head":{"ref":"feature/login","sha":"22",%s},"base":{"ref":"main"},%s}`, n, head, extra)
	}
	cases := []struct {
		name string
		json string
		want domain.PullRequest
	}{
		{"open", fields(40, same, `"state":"open"`), domain.PullRequest{State: domain.PROpen}},
		{"merged", fields(41, same, `"state":"closed","merged":true`), domain.PullRequest{State: domain.PRMerged}},
		{"merged without the flag", fields(42, same, `"state":"closed","merged_at":"2026-11-01T10:00:00Z"`), domain.PullRequest{State: domain.PRMerged}},
		{"closed", fields(43, same, `"state":"closed","merged":false`), domain.PullRequest{State: domain.PRClosed}},
		{"fork with maintainer edits", fields(44, fork, `"state":"open","maintainer_can_modify":true`), domain.PullRequest{
			State: domain.PROpen, Fork: true, MaintainerCanModify: true,
			ForkRepo: domain.Repository{Provider: domain.GitHub, Org: "ada", Name: "anthill"}, ForkURL: "https://github.com/Ada/Anthill.git",
		}},
		{"fork without maintainer edits", fields(45, fork, `"state":"open","maintainer_can_modify":false`), domain.PullRequest{
			State: domain.PROpen, Fork: true,
			ForkRepo: domain.Repository{Provider: domain.GitHub, Org: "ada", Name: "anthill"}, ForkURL: "https://github.com/Ada/Anthill.git",
		}},
		{"deleted fork", fields(46, `"repo":null`, `"state":"open","maintainer_can_modify":true`), domain.PullRequest{State: domain.PROpen, Fork: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			var n int
			_, _ = fmt.Sscanf(c.json, `{"number":%d`, &n)
			e.srv.handle(fmt.Sprintf("GET /repos/matteorosani/anthill/pulls/%d", n), func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, c.json)
			})
			e.srv.fixture(fmt.Sprintf("GET /repos/matteorosani/anthill/issues/%d/comments", n), http.StatusOK, "empty_list.json")
			e.srv.fixture(fmt.Sprintf("GET /repos/matteorosani/anthill/pulls/%d/reviews", n), http.StatusOK, "empty_list.json")
			e.srv.graphql(fmt.Sprintf(`"number":%d`, n), "graphql_no_threads.json")

			got, _, err := e.a.PullRequest(ctx(t), pr(n))
			if err != nil {
				t.Fatal(err)
			}
			if got.State != c.want.State || got.Fork != c.want.Fork || got.ForkRepo != c.want.ForkRepo ||
				got.ForkURL != c.want.ForkURL || got.MaintainerCanModify != c.want.MaintainerCanModify {
				t.Errorf("state, fork = %v, %v %v %q %v; want %v, %v %v %q %v", got.State, got.Fork, got.ForkRepo, got.ForkURL, got.MaintainerCanModify,
					c.want.State, c.want.Fork, c.want.ForkRepo, c.want.ForkURL, c.want.MaintainerCanModify)
			}
		})
	}
}
