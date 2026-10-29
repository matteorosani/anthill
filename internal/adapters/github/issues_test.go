package github_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/github"
	"github.com/matteorosani/anthill/internal/domain"
)

func item(n int) domain.Ref { return domain.Ref{Repo: repo, Kind: domain.KindItem, Number: n} }

// serveFirstPoll registers the answers to a first poll of the opted-in
// issues and the comments, links and sub-issues of #12.
func serveFirstPoll(t *testing.T, e *env) {
	e.srv.handle("GET /repos/matteorosani/anthill/issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("labels") != "anthill" || q.Get("state") != "all" || q.Has("since") {
			t.Errorf("first poll query = %s, want labels=anthill, state=all and no since", r.URL.RawQuery)
		}
		respond(t, http.StatusOK, "issues_opted_in.json", "ETag", `"first"`)(w, r)
	})
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12/comments", http.StatusOK, "issue12_comments.json")
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12/dependencies/blocked_by", http.StatusOK, "issue12_blocked_by.json")
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12/sub_issues", http.StatusOK, "issue12_sub_issues.json")
}

// Task 7.2: a first poll maps every opted-in issue (spec
// work-item-sources, "Normalized work item model").
func TestItemsFirstPoll(t *testing.T) {
	e := newEnv(t)
	serveFirstPoll(t, e)

	page, err := e.a.Items(ctx(t), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if !page.ServerTime.Equal(serverDate) {
		t.Errorf("server time = %v, want the Date header %v", page.ServerTime, serverDate)
	}
	if page.Cursor == "" {
		t.Error("no cursor")
	}
	var refs []domain.Ref
	for _, it := range page.Items {
		refs = append(refs, it.Ref)
	}
	// #14 is a pull request and is left out. The fixture spells the
	// repository MatteoRosani/Anthill; refs are lowercase.
	if want := []domain.Ref{item(12), item(13), item(15)}; !slices.Equal(refs, want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
	i12, i13, i15 := page.Items[0], page.Items[1], page.Items[2]

	t.Run("fields", func(t *testing.T) {
		if i12.Title != "Fix login timeout on Safari" || i12.Body != "Users are logged out after 30 seconds." ||
			i12.Author != "alice" || i12.URL != "https://github.com/MatteoRosani/Anthill/issues/12" {
			t.Errorf("item = %+v", i12)
		}
		if i12.Type != domain.TypeTask || i12.State != domain.StateOpen {
			t.Errorf("type, state = %s, %s", i12.Type, i12.State)
		}
		if !slices.Equal(i12.Labels, []string{"anthill"}) || !slices.Equal(i12.Assignees, []string{"bob"}) {
			t.Errorf("labels, assignees = %v, %v", i12.Labels, i12.Assignees)
		}
		want := time.Date(2026, 10, 29, 9, 30, 0, 0, time.UTC)
		if !i12.UpdatedAt.Equal(want) || i12.Revision != "2026-10-29T09:30:00Z" {
			t.Errorf("updated, revision = %v, %q", i12.UpdatedAt, i12.Revision)
		}
	})

	// Spec work-item-sources, "GitHub issue with dependencies and
	// sub-issues".
	t.Run("blocked by and sub-issues", func(t *testing.T) {
		otherRef := domain.Ref{Repo: other, Kind: domain.KindItem, Number: 3}
		if want := []domain.Ref{item(10), otherRef}; !slices.Equal(i12.Dependencies, want) {
			t.Errorf("dependencies = %v, want %v", i12.Dependencies, want)
		}
		if want := []domain.Ref{item(20), item(21)}; !slices.Equal(i12.Children, want) {
			t.Errorf("children = %v, want %v", i12.Children, want)
		}
		if i15.Parent == nil || *i15.Parent != item(7) {
			t.Errorf("parent of #15 = %v, want #7", i15.Parent)
		}
		if i12.Parent != nil || i13.Dependencies != nil || i13.Children != nil {
			t.Error("links on items that have none")
		}
	})

	// Spec work-item-sources, "GitHub issue closed as not planned".
	t.Run("closed as not planned", func(t *testing.T) {
		if i13.State != domain.StateClosedNotPlanned {
			t.Errorf("#13 state = %s", i13.State)
		}
		if i15.State != domain.StateClosedNotPlanned {
			t.Errorf("#15, closed as duplicate, state = %s", i15.State)
		}
		if i13.Type != domain.TypeBug || i15.Type != domain.TypeFeature {
			t.Errorf("types from labels = %s, %s", i13.Type, i15.Type)
		}
	})

	// Spec work-item-sources, "Anthill's own comment" and "Developer
	// comments from the same account".
	t.Run("comments", func(t *testing.T) {
		if len(i12.Comments) != 2 {
			t.Fatalf("comments = %+v", i12.Comments)
		}
		human, own := i12.Comments[0], i12.Comments[1]
		if human.ID != "9001" || human.Author != "alice" || human.Origin() != domain.OriginHuman {
			t.Errorf("human comment = %+v", human)
		}
		if own.ID != "9002" || own.Origin() != domain.OriginAnthill {
			t.Errorf("Anthill comment = %+v", own)
		}
	})

	// Comments, links and sub-issues are read only where the issue
	// counts some.
	if n := len(e.srv.sent()); n != 4 {
		t.Errorf("%d requests, want 4", n)
	}
}

// Task 7.2: later polls send since and the ETag (design D7), and see
// issues whose opt-in label was removed (spec work-item-sources, "Opt-in
// label added later" relies on the same unfiltered read).
func TestItemsIncrementalPoll(t *testing.T) {
	e := newEnv(t)
	calls := 0
	e.srv.handle("GET /repos/matteorosani/anthill/issues", func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if q.Has("labels") {
			t.Errorf("incremental poll filters by label: %s", r.URL.RawQuery)
		}
		switch calls {
		case 1:
			if q.Get("since") != "2026-10-29T09:30:00Z" || r.Header.Get("If-None-Match") != `"abc"` {
				t.Errorf("poll 1: since=%q If-None-Match=%q", q.Get("since"), r.Header.Get("If-None-Match"))
			}
			w.WriteHeader(http.StatusNotModified)
		case 2:
			respond(t, http.StatusOK, "issues_since.json", "ETag", `"def"`)(w, r)
		case 3:
			if q.Get("since") != "2026-10-29T10:05:00Z" || r.Header.Get("If-None-Match") != "" {
				t.Errorf("poll 3: since=%q If-None-Match=%q", q.Get("since"), r.Header.Get("If-None-Match"))
			}
			respond(t, http.StatusOK, "issues_since.json", "ETag", `"ghi"`)(w, r)
		}
	})

	start := `{"since":"2026-10-29T09:30:00Z","etag":"\"abc\""}`
	page, err := e.a.Items(ctx(t), repo, start)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.Cursor != start || !page.ServerTime.Equal(serverDate) {
		t.Fatalf("304 page = %+v, want no items and the same cursor", page)
	}

	page, err = e.a.Items(ctx(t), repo, page.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Ref != item(16) || len(page.Items[0].Labels) != 0 {
		t.Fatalf("items = %+v, want #16 without labels", page.Items)
	}

	page, err = e.a.Items(ctx(t), repo, page.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"since":"2026-10-29T10:05:00Z","etag":"\"ghi\""}`; page.Cursor != want {
		t.Errorf("cursor = %s, want %s: same since, so the ETag is kept", page.Cursor, want)
	}
}

// Task 7.2: comments are read across pages.
func TestItemCommentsFollowPages(t *testing.T) {
	e := newEnv(t)
	e.srv.handle("GET /repos/matteorosani/anthill/issues/12", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"number":12,"state":"open","comments":2,"repository_url":"https://api.github.com/repos/MatteoRosani/Anthill"}`)
	})
	e.srv.handle("GET /repos/matteorosani/anthill/issues/12/comments", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = fmt.Fprint(w, `[{"id":2,"body":"second"}]`)
			return
		}
		next := e.srv.srv.URL + r.URL.Path + "?" + url.Values{"page": {"2"}, "per_page": {"100"}}.Encode()
		w.Header().Set("Link", `<`+next+`>; rel="next"`)
		_, _ = fmt.Fprint(w, `[{"id":1,"body":"first"}]`)
	})
	it, _, err := e.a.Item(ctx(t), gh12)
	if err != nil {
		t.Fatal(err)
	}
	if len(it.Comments) != 2 || it.Comments[0].ID != "1" || it.Comments[1].ID != "2" {
		t.Errorf("comments = %+v", it.Comments)
	}
}

// Task 7.2 and the re-check at acceptance: Item reads one issue with
// GitHub's time, and a ref spelled in another case means the same issue.
func TestItemNormalizesCase(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12", http.StatusOK, "issue12.json")
	mixed := domain.Ref{Repo: domain.Repository{Provider: domain.GitHub, Org: "MatteoRosani", Name: "Anthill"}, Kind: domain.KindItem, Number: 12}
	it, now, err := e.a.Item(ctx(t), mixed)
	if err != nil {
		t.Fatal(err)
	}
	if it.Ref != gh12 || !now.Equal(serverDate) {
		t.Errorf("ref, time = %v, %v", it.Ref, now)
	}
	if got := github.Normalize(mixed.Repo); got != repo {
		t.Errorf("Normalize = %v, want %v", got, repo)
	}
}
