package github_test

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/github"
	"github.com/matteorosani/anthill/internal/domain"
)

func pr(n int) domain.Ref { return domain.Ref{Repo: repo, Kind: domain.KindPR, Number: n} }

// servePulls registers the open PRs of pulls.json with their comments,
// reviews and review threads.
func servePulls(t *testing.T, e *env) {
	e.srv.fixture("GET /repos/matteorosani/anthill/pulls", http.StatusOK, "pulls.json", "ETag", `"pulls-1"`)
	for _, n := range []int{34, 35, 36} {
		file := "pr_comments.json"
		if n != 34 {
			file = "empty_list.json"
		}
		e.srv.fixture(fmt.Sprintf("GET /repos/matteorosani/anthill/issues/%d/comments", n), http.StatusOK, file)
		reviews := "pr_reviews.json"
		if n != 34 {
			reviews = "empty_list.json"
		}
		e.srv.fixture(fmt.Sprintf("GET /repos/matteorosani/anthill/pulls/%d/reviews", n), http.StatusOK, reviews)
	}
	e.srv.graphql(
		`"number":34`, "graphql_threads.json",
		`"number":35`, "graphql_no_threads.json",
		`"number":36`, "graphql_no_threads.json",
	)
}

// Task 7.3: PR polling maps the head commit, the draft flag, review
// threads and the link to the item (spec work-item-sources, "Pull
// request ingestion").
func TestPullRequestsPoll(t *testing.T) {
	e := newEnv(t)
	servePulls(t, e)

	page, err := e.a.PullRequests(ctx(t), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.PullRequests) != 3 || !page.ServerTime.Equal(serverDate) {
		t.Fatalf("page = %+v", page)
	}
	p34, p35, p36 := page.PullRequests[0], page.PullRequests[1], page.PullRequests[2]
	if p34.Ref != pr(34) || p35.Ref != pr(35) || p36.Ref != pr(36) {
		t.Fatalf("refs = %v, %v, %v", p34.Ref, p35.Ref, p36.Ref)
	}

	t.Run("fields", func(t *testing.T) {
		if p34.HeadCommit != "1111111111111111111111111111111111111111" || p34.Draft || !p35.Draft {
			t.Errorf("head, draft = %s, %v, %v", p34.HeadCommit, p34.Draft, p35.Draft)
		}
		if p34.SourceBranch != "ant/gh-12-fix-login-timeout-on-safari" || p34.TargetBranch != "main" ||
			p34.Author != "mrosani" || p34.Title != "Fix login timeout on Safari" {
			t.Errorf("pr = %+v", p34)
		}
		if !slices.Equal(p34.Labels, []string{"anthill"}) {
			t.Errorf("labels = %v", p34.Labels)
		}
		if len(p34.Comments) != 2 || p34.Comments[0].ID != "9101" {
			t.Errorf("comments = %+v", p34.Comments)
		}
	})

	// A review's summary is a comment, so that change detection sees it;
	// reviews without text and pending reviews are not.
	t.Run("review summaries", func(t *testing.T) {
		submitted := time.Date(2026, 10, 29, 9, 47, 0, 0, time.UTC)
		want := domain.Comment{ID: "v456", Author: "bob", Body: "Please handle the empty password too.",
			URL:       "https://github.com/MatteoRosani/Anthill/pull/34#pullrequestreview-456",
			CreatedAt: submitted, UpdatedAt: submitted}
		if len(p34.Comments) != 2 || p34.Comments[1] != want {
			t.Errorf("comments = %+v, want the summary %+v", p34.Comments, want)
		}
		if len(p35.Comments) != 0 {
			t.Errorf("comments of #35 = %+v", p35.Comments)
		}
	})

	t.Run("review threads", func(t *testing.T) {
		if len(p34.Threads) != 2 {
			t.Fatalf("threads = %+v", p34.Threads)
		}
		open, resolved := p34.Threads[0], p34.Threads[1]
		if open.ID != "PRRT_kwDOA001" || open.Resolved || open.Path != "web/session.go" || open.Line != 42 {
			t.Errorf("open thread = %+v", open)
		}
		if len(open.Comments) != 1 || open.Comments[0].ID != "r7001" || open.Comments[0].Author != "alice" ||
			!open.Comments[0].CreatedAt.Equal(time.Date(2026, 10, 29, 9, 44, 0, 0, time.UTC)) {
			t.Errorf("thread comments = %+v", open.Comments)
		}
		// An outdated thread keeps its original line; a deleted
		// account has no author.
		if !resolved.Resolved || resolved.Line != 7 || resolved.Comments[0].Author != "" {
			t.Errorf("resolved thread = %+v", resolved)
		}
		if len(p35.Threads) != 0 {
			t.Errorf("threads of #35 = %+v", p35.Threads)
		}
	})

	// Spec work-item-sources, "Ant PR linked to its ticket", and spec
	// ant-execution, "PR body reference removed".
	t.Run("link to the item", func(t *testing.T) {
		if p34.Item == nil || *p34.Item != item(12) {
			t.Errorf("#34 links to %v, want #12 from Closes #12", p34.Item)
		}
		if p35.Item == nil || *p35.Item != item(13) {
			t.Errorf("#35 links to %v, want #13 from its branch name", p35.Item)
		}
		if p36.Item != nil {
			t.Errorf("#36 links to %v: a closing reference to another repository is no link", p36.Item)
		}
	})
}

// Task 7.3: later polls are conditional and keep only PRs updated since
// the cursor.
func TestPullRequestsIncrementalPoll(t *testing.T) {
	e := newEnv(t)
	calls := 0
	e.srv.handle("GET /repos/matteorosani/anthill/pulls", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			if r.Header.Get("If-None-Match") != `"pulls-1"` {
				t.Errorf("If-None-Match = %q", r.Header.Get("If-None-Match"))
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		respond(t, http.StatusOK, "pulls.json", "ETag", `"pulls-2"`)(w, r)
	})
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/34/comments", http.StatusOK, "pr_comments.json")
	e.srv.fixture("GET /repos/matteorosani/anthill/pulls/34/reviews", http.StatusOK, "empty_list.json")
	e.srv.graphql(`"number":34`, "graphql_threads.json")

	start := `{"since":"2026-10-29T09:45:00Z","etag":"\"pulls-1\""}`
	page, err := e.a.PullRequests(ctx(t), repo, start)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.PullRequests) != 0 || page.Cursor != start {
		t.Fatalf("304 page = %+v", page)
	}
	page, err = e.a.PullRequests(ctx(t), repo, page.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	// Only #34 was updated after 09:45; #35 and #36 are not read again.
	if len(page.PullRequests) != 1 || page.PullRequests[0].Ref != pr(34) {
		t.Fatalf("prs = %+v", page.PullRequests)
	}
	if want := `{"since":"2026-10-29T09:50:00Z","etag":"\"pulls-2\""}`; page.Cursor != want {
		t.Errorf("cursor = %s, want %s", page.Cursor, want)
	}
}

// Task 7.3: one PR is read fresh with GitHub's time, and the branch
// prefix comes from the repository's options.
func TestPullRequestUsesRepoOptions(t *testing.T) {
	e := newEnv(t, func(o *github.Options) {
		o.Repo = func(r domain.Repository) github.RepoOptions {
			if r != repo {
				t.Errorf("options asked for %v", r)
			}
			return github.RepoOptions{BranchPrefix: "bot"}
		}
	})
	e.srv.handle("GET /repos/matteorosani/anthill/pulls/35", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"number":35,"body":"","head":{"ref":"bot/gh-13-drop","sha":"22"},"base":{"ref":"main"}}`)
	})
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/35/comments", http.StatusOK, "empty_list.json")
	e.srv.fixture("GET /repos/matteorosani/anthill/pulls/35/reviews", http.StatusOK, "empty_list.json")
	e.srv.graphql(`"number":35`, "graphql_no_threads.json")

	got, now, err := e.a.PullRequest(ctx(t), pr(35))
	if err != nil {
		t.Fatal(err)
	}
	if got.Item == nil || *got.Item != item(13) || !now.Equal(serverDate) {
		t.Errorf("item, time = %v, %v", got.Item, now)
	}
}
