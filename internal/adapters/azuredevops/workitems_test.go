package azuredevops_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// batch registers the work item batch endpoint: a request for the
// System.TeamProject of linked items gets link_projects.json, any other
// gets items.
func batch(t *testing.T, s *server, items string) {
	full, links := respond(t, http.StatusOK, items), respond(t, http.StatusOK, "link_projects.json")
	s.handle("POST "+batchPath, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"fields"`) {
			links(w, r)
			return
		}
		full(w, r)
	})
}

// pollRoutes registers a first poll of Shop: the tagged WIQL query, the
// batch of items 12 and 15, the states of their types and their comments.
func pollRoutes(t *testing.T, s *server, wiql string) {
	s.fixture("POST "+wiqlPath, http.StatusOK, wiql)
	batch(t, s, "batch_12_15.json")
	s.fixture("GET /contoso/shop/_apis/wit/workitemtypes/Bug/states", http.StatusOK, "states_bug.json")
	s.fixture("GET /contoso/shop/_apis/wit/workitemtypes/User Story/states", http.StatusOK, "states_user_story.json")
	s.fixture("GET "+commentsPath, http.StatusOK, "comments12.json")
}

// Scenario "Azure DevOps bug mapped": a Bug with the anthill tag is
// represented with type bug, its Predecessor links as dependency links,
// and its System.Rev as the revision.
func TestAzureDevOpsBugMapped(t *testing.T) {
	e := newEnv(t)
	pollRoutes(t, e.srv, "wiql_tagged.json")
	page, err := e.a.Items(ctx(t), domain.Repository{Provider: domain.AzureDevOps, Org: "Contoso", Project: "Shop"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || !page.ServerTime.Equal(serverDate) {
		t.Fatalf("page = %+v", page)
	}
	bug := page.Items[0]
	platform := domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "platform"}
	want := domain.WorkItem{
		Ref:          ado12,
		URL:          e.srv.srv.URL + "/contoso/shop/_workitems/edit/12",
		Author:       "ada@contoso.com",
		Title:        "Login times out on Safari",
		Body:         "Login fails after 30 s on Safari.",
		Type:         domain.TypeBug,
		State:        domain.StateOpen,
		Labels:       []string{"anthill", "repo:Web"},
		Assignees:    []string{"m.rosani@contoso.com"},
		Dependencies: []domain.Ref{{Repo: shop, Kind: domain.KindItem, Number: 10}},
		Parent:       &domain.Ref{Repo: platform, Kind: domain.KindItem, Number: 3},
		Comments: []domain.Comment{{
			ID: "5", Author: "ada@contoso.com", URL: e.srv.srv.URL + "/contoso/shop/_workitems/edit/12", Body: "Also happens on iOS.",
			CreatedAt: time.Date(2026, 10, 29, 9, 30, 0, 123e6, time.UTC), UpdatedAt: time.Date(2026, 10, 29, 9, 30, 0, 123e6, time.UTC),
		}},
		Revision:  "7",
		UpdatedAt: time.Date(2026, 10, 29, 9, 30, 0, 123e6, time.UTC),
	}
	if got, _ := json.Marshal(bug); string(got) != string(must(json.Marshal(want))) {
		t.Errorf("bug =\n%s\nwant\n%s", got, must(json.Marshal(want)))
	}

	story := page.Items[1]
	if story.Type != domain.TypeFeature || story.State != domain.StateClosedNotPlanned || story.Body != "Keep users signed in." ||
		story.AcceptanceCriteria != "- The session lasts 30 days." || len(story.Children) != 1 || story.Children[0].Number != 16 ||
		story.Children[0].Repo != shop || story.Comments != nil {
		t.Errorf("story = %+v", story)
	}

	// The first poll asks for the opt-in tag only, with time precision.
	wiql := e.srv.matching(http.MethodPost, wiqlPath)[0]
	if !strings.Contains(wiql.Body, `[System.Tags] CONTAINS 'anthill'`) || strings.Contains(wiql.Body, "ChangedDate") ||
		!strings.Contains(wiql.Query, "timePrecision=true") {
		t.Errorf("WIQL = %s ? %s", wiql.Body, wiql.Query)
	}
	// Comments are read only for items that have some.
	if n := e.srv.count(http.MethodGet, "/contoso/shop/_apis/wit/workItems/15/comments"); n != 0 {
		t.Errorf("read the comments of 15 %d times", n)
	}
	// Item 15 changed half an hour before item 12, outside the overlap,
	// so only 12's revision is kept.
	var c struct {
		Since time.Time      `json:"since"`
		Revs  map[string]int `json:"revs"`
	}
	if err := json.Unmarshal([]byte(page.Cursor), &c); err != nil || !c.Since.Equal(want.UpdatedAt) || c.Revs["12"] != 7 || c.Revs["15"] != 0 {
		t.Errorf("cursor = %s", page.Cursor)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// A later poll reads every item changed since the cursor minus the
// overlap, tagged or not, and leaves out the ones whose revision did not
// move.
func TestItemsIncrementalPoll(t *testing.T) {
	e := newEnv(t)
	pollRoutes(t, e.srv, "wiql_changed.json")
	since := time.Date(2026, 10, 29, 9, 30, 0, 123e6, time.UTC)
	cur := `{"since":"2026-10-29T09:30:00.123Z","revs":{"12":6,"15":3}}`
	page, err := e.a.Items(ctx(t), shop, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Ref != ado12 {
		t.Fatalf("items = %+v, want only 12, whose revision moved", page.Items)
	}
	wiql := e.srv.matching(http.MethodPost, wiqlPath)[0].Body
	from := since.Add(-2 * time.Minute).Format("2006-01-02T15:04:05.000Z")
	if !strings.Contains(wiql, "[System.ChangedDate] >= '"+from+"'") || strings.Contains(wiql, "System.Tags") {
		t.Errorf("WIQL = %s, want changed since %s", wiql, from)
	}
	// Item 15 changed more than two minutes before the newest change, so
	// the next cursor does not keep it.
	if strings.Contains(page.Cursor, `"15"`) || !strings.Contains(page.Cursor, `"12":7`) {
		t.Errorf("cursor = %s", page.Cursor)
	}
}

func TestItemsNoMatch(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST "+wiqlPath, http.StatusOK, "wiql_empty.json")
	page, err := e.a.Items(ctx(t), shop, "")
	if err != nil || len(page.Items) != 0 || page.Cursor != `{"since":"0001-01-01T00:00:00Z"}` {
		t.Errorf("page = %+v, %v", page, err)
	}
	if n := e.srv.count(http.MethodPost, batchPath); n != 0 {
		t.Errorf("read %d batches of nothing", n)
	}
}

// Item reads one work item fresh, lowercasing the names it is given.
func TestItemNormalizesCase(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+itemPath, http.StatusOK, "workitem12.json")
	itemRoutes(e.srv)
	ref := domain.Ref{Repo: domain.Repository{Provider: domain.AzureDevOps, Org: "CONTOSO", Project: "Shop"}, Kind: domain.KindItem, Number: 12}
	item, now, err := e.a.Item(ctx(t), ref)
	if err != nil {
		t.Fatal(err)
	}
	if item.Ref != ado12 || !now.Equal(serverDate) || item.Revision != "7" {
		t.Errorf("Item() = %+v at %v", item, now)
	}
	if q := e.srv.matching(http.MethodGet, itemPath)[0].Query; !strings.Contains(q, "%24expand=relations") {
		t.Errorf("query = %q, want $expand=relations", q)
	}
}

func TestItemInAnotherProjectIsNotFound(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+itemPath, http.StatusOK, "workitem12_other_project.json")
	if _, _, err := e.a.Item(ctx(t), ado12); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestItemNotFound(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+itemPath, http.StatusNotFound, "not_found.json")
	_, _, err := e.a.Item(ctx(t), ado12)
	if !errors.Is(err, ports.ErrNotFound) || !strings.Contains(err.Error(), "TF401232") {
		t.Errorf("err = %v", err)
	}
}

// Comments are read page by page with the continuation token.
func TestItemCommentsFollowPages(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+itemPath, http.StatusOK, "workitem12.json")
	e.srv.fixture("POST "+batchPath, http.StatusOK, "link_projects.json")
	e.srv.fixture("GET /contoso/shop/_apis/wit/workitemtypes/Bug/states", http.StatusOK, "states_bug.json")
	e.srv.handle("GET "+commentsPath, func(w http.ResponseWriter, r *http.Request) {
		file := "comments12_page1.json"
		if r.URL.Query().Get("continuationToken") == "page-2" {
			file = "comments12_page2.json"
		}
		respond(t, http.StatusOK, file)(w, r)
	})
	item, _, err := e.a.Item(ctx(t), ado12)
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, c := range item.Comments {
		bodies = append(bodies, c.Body)
	}
	if !slices.Equal(bodies, []string{"first", "second"}) {
		t.Errorf("comments = %q", bodies)
	}
	if q := e.srv.matching(http.MethodGet, commentsPath)[0].Query; !strings.Contains(q, "api-version=7.1-preview.4") {
		t.Errorf("query = %q, want the comments preview version", q)
	}
}

// The states of a work item type are read once per project.
func TestStateCategoriesAreCached(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+itemPath, http.StatusOK, "workitem12.json")
	itemRoutes(e.srv)
	for range 2 {
		if _, _, err := e.a.Item(ctx(t), ado12); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.srv.count(http.MethodGet, "/contoso/shop/_apis/wit/workitemtypes/Bug/states"); n != 1 {
		t.Errorf("read the states %d times, want 1", n)
	}
}
