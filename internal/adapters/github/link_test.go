package github

import (
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/testkit"
)

// Task 7.3: the forms of the link from a PR to its item.
func TestLinkedItem(t *testing.T) {
	a, err := New(Options{Credentials: &testkit.CredentialStore{}, Clock: testkit.NewClock(time.Now())})
	if err != nil {
		t.Fatal(err)
	}
	repo := domain.Repository{Provider: domain.GitHub, Org: "matteorosani", Name: "anthill"}
	tests := []struct {
		name   string
		body   string
		branch string
		head   string
		want   int
	}{
		{"closes", "Closes #12", "feature/x", "", 12},
		{"keyword with colon, any case", "FIXES: #7.", "feature/x", "", 7},
		{"resolved", "This resolved #8, I think", "feature/x", "", 8},
		{"same repository by name", "fixes MatteoRosani/Anthill#9", "feature/x", "", 9},
		{"same repository by URL", "Closes https://github.com/MatteoRosani/Anthill/issues/10", "feature/x", "", 10},
		{"other repository", "Closes matteorosani/other#12", "feature/x", "", 0},
		{"other host", "Closes https://ghe.example.com/matteorosani/anthill/issues/12", "feature/x", "", 0},
		{"other repository, then branch", "Closes matteorosani/other#12", "ant/gh-12-x", "", 12},
		{"body wins over branch", "Closes #5", "ant/gh-12-x", "", 5},
		{"mention is no link", "See #12", "feature/x", "", 0},
		{"branch without slug", "", "ant/gh-12", "", 12},
		{"branch of another prefix", "", "bot/gh-12-x", "", 0},
		{"branch of another provider", "", "ant/ado-12-x", "", 0},
		{"branch of a fork", "", "ant/gh-12-x", "someone/anthill", 0},
		{"branch of the same repository", "", "ant/gh-12-x", "MatteoRosani/Anthill", 12},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := &gh.PullRequest{Body: new(tt.body), Head: &gh.PullRequestBranch{Ref: new(tt.branch)}}
			if tt.head != "" {
				pr.Head.Repo = &gh.Repository{FullName: new(tt.head)}
			}
			got := a.linkedItem(repo, pr)
			switch {
			case tt.want == 0 && got != nil:
				t.Errorf("link = %v, want none", got)
			case tt.want != 0 && (got == nil || *got != domain.Ref{Repo: repo, Kind: domain.KindItem, Number: tt.want}):
				t.Errorf("link = %v, want #%d", got, tt.want)
			}
		})
	}
}
