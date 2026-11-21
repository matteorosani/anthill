package domain_test

import (
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestPRStateEnded(t *testing.T) {
	for s, want := range map[domain.PRState]bool{domain.PRNone: false, domain.PROpen: false, domain.PRMerged: true, domain.PRClosed: true} {
		if s.Ended() != want {
			t.Errorf("%v.Ended() = %v", s, !want)
		}
	}
}

// Task 16.6: only a branch Anthill may push to gets a fix pass (spec
// pr-shepherd, "Fork branch not pushable").
func TestFixPushable(t *testing.T) {
	fork := domain.Repository{Provider: domain.GitHub, Org: "ada", Name: "shop"}
	cases := []struct {
		name string
		pr   domain.PullRequest
		want bool
	}{
		{"same repository", domain.PullRequest{}, true},
		{"fork with maintainer edits", domain.PullRequest{Fork: true, ForkRepo: fork, ForkURL: "https://github.com/ada/shop.git", MaintainerCanModify: true}, true},
		{"fork without maintainer edits", domain.PullRequest{Fork: true, ForkRepo: fork, ForkURL: "https://github.com/ada/shop.git"}, false},
		{"deleted fork", domain.PullRequest{Fork: true, MaintainerCanModify: true}, false},
	}
	for _, c := range cases {
		if got := domain.FixPushable(c.pr); got != c.want {
			t.Errorf("%s: FixPushable() = %v", c.name, got)
		}
	}
}
