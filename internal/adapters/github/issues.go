package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// perPage is the largest page GitHub serves.
const perPage = 100

// Items polls the issues of repo changed since cur (spec
// work-item-sources, "Change detection"). An empty cursor reads every
// issue with the opt-in label. A non-empty one reads every issue updated
// since the cursor, with or without the label, as a conditional request:
// when nothing changed, GitHub answers 304 and the page is empty with
// the same cursor. Pull requests, which the issues endpoint also lists,
// are left out.
func (a *Adapter) Items(ctx context.Context, repo domain.Repository, cur string) (ports.ItemPage, error) {
	repo = Normalize(repo)
	if err := checkRepo(repo); err != nil {
		return ports.ItemPage{}, err
	}
	c, err := parseCursor(cur)
	if err != nil {
		return ports.ItemPage{}, err
	}
	query := url.Values{
		"state":     {"all"},
		"sort":      {"updated"},
		"direction": {"desc"},
		"per_page":  {strconv.Itoa(perPage)},
	}
	if c.Since.IsZero() {
		query.Set("labels", a.options(repo).OptInLabel)
	} else {
		query.Set("since", c.Since.UTC().Format(time.RFC3339))
	}
	op := "list issues of " + repo.String()
	path := fmt.Sprintf("repos/%s/%s/issues", repo.Org, repo.Name)

	issues, first, err := listPages[*gh.Issue](ctx, a, op, path, query, c.ETag)
	if err != nil {
		return ports.ItemPage{}, err
	}
	page := ports.ItemPage{ServerTime: a.serverTime(first), Cursor: cur}
	if first.StatusCode == http.StatusNotModified {
		return page, nil
	}
	next := cursor{Since: c.Since, ETag: first.Header.Get("ETag")}
	for _, issue := range issues {
		if t := issue.GetUpdatedAt().Time; t.After(next.Since) {
			next.Since = t
		}
	}
	if !next.Since.Equal(c.Since) {
		// The next poll asks with another since, so another URL; the
		// ETag of this one would never match.
		next.ETag = ""
	}
	page.Cursor = next.String()
	for _, issue := range issues {
		if issue.IsPullRequest() {
			continue
		}
		item, err := a.workItem(ctx, repo, issue)
		if err != nil {
			return ports.ItemPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	slices.SortFunc(page.Items, func(x, y domain.WorkItem) int { return x.Ref.Number - y.Ref.Number })
	return page, nil
}

// Item reads one issue fresh, with GitHub's time (spec task-eligibility,
// "Re-check at acceptance").
func (a *Adapter) Item(ctx context.Context, ref domain.Ref) (domain.WorkItem, time.Time, error) {
	ref = normalizeRef(ref)
	if err := checkRepo(ref.Repo); err != nil {
		return domain.WorkItem{}, time.Time{}, err
	}
	op := "read issue " + ref.Full()
	issue, resp, err := a.client.Issues.Get(ctx, ref.Repo.Org, ref.Repo.Name, ref.Number)
	if err != nil {
		return domain.WorkItem{}, time.Time{}, a.apiError(op, resp, err)
	}
	if issue.IsPullRequest() {
		return domain.WorkItem{}, time.Time{}, fmt.Errorf("github: %s: it is a pull request: %w", op, ports.ErrNotFound)
	}
	item, err := a.workItem(ctx, ref.Repo, issue)
	if err != nil {
		return domain.WorkItem{}, time.Time{}, err
	}
	return item, a.serverTime(resp), nil
}

// workItem maps an issue and reads what the issue object only counts:
// comments, "blocked by" links and sub-issues. Each is read only when
// its count is not zero.
func (a *Adapter) workItem(ctx context.Context, repo domain.Repository, issue *gh.Issue) (domain.WorkItem, error) {
	item := mapIssue(repo, issue)
	ref := item.Ref
	org, name, number := ref.Repo.Org, ref.Repo.Name, ref.Number

	if issue.GetComments() > 0 {
		comments, _, err := a.issueComments(ctx, ref)
		if err != nil {
			return domain.WorkItem{}, err
		}
		item.Comments = comments
	}
	if deps := issue.IssueDependenciesSummary; deps != nil && (deps.GetBlockedBy() > 0 || deps.GetTotalBlockedBy() > 0) {
		blockers, resp, err := collect(func(o gh.ListOptions) ([]*gh.Issue, *gh.Response, error) {
			return a.client.Issues.ListBlockedBy(ctx, org, name, int64(number), &o)
		})
		if err != nil {
			return domain.WorkItem{}, a.apiError("list blocked-by links of "+ref.Full(), resp, err)
		}
		for _, b := range blockers {
			item.Dependencies = append(item.Dependencies, issueRef(ref.Repo, b))
		}
	}
	if subs := issue.SubIssuesSummary; subs != nil && subs.GetTotal() > 0 {
		children, resp, err := collect(func(o gh.ListOptions) ([]*gh.SubIssue, *gh.Response, error) {
			return a.client.SubIssue.ListByIssue(ctx, org, name, int64(number), &o)
		})
		if err != nil {
			return domain.WorkItem{}, a.apiError("list sub-issues of "+ref.Full(), resp, err)
		}
		for _, child := range children {
			item.Children = append(item.Children, issueRef(ref.Repo, (*gh.Issue)(child)))
		}
	}
	return item, nil
}

// issueComments reads every comment of an issue or PR, oldest first,
// with the first page's response.
func (a *Adapter) issueComments(ctx context.Context, ref domain.Ref) ([]domain.Comment, *gh.Response, error) {
	list, resp, err := collect(func(o gh.ListOptions) ([]*gh.IssueComment, *gh.Response, error) {
		return a.client.Issues.ListComments(ctx, ref.Repo.Org, ref.Repo.Name, ref.Number, &gh.IssueListCommentsOptions{ListOptions: o})
	})
	if err != nil {
		return nil, nil, a.apiError("list comments of "+ref.Full(), resp, err)
	}
	comments := make([]domain.Comment, 0, len(list))
	for _, c := range list {
		comments = append(comments, issueComment(c))
	}
	return comments, resp, nil
}

// mapIssue maps the fields of the issue object itself.
func mapIssue(repo domain.Repository, issue *gh.Issue) domain.WorkItem {
	item := domain.WorkItem{
		Ref:       issueRef(repo, issue),
		URL:       issue.GetHTMLURL(),
		Author:    issue.GetUser().GetLogin(),
		Title:     issue.GetTitle(),
		Body:      issue.GetBody(),
		Type:      issueType(issue),
		State:     issueState(issue),
		Revision:  issue.GetUpdatedAt().UTC().Format(time.RFC3339),
		UpdatedAt: issue.GetUpdatedAt().Time,
	}
	for _, l := range issue.Labels {
		item.Labels = append(item.Labels, l.GetName())
	}
	for _, u := range issue.Assignees {
		item.Assignees = append(item.Assignees, u.GetLogin())
	}
	if parent, ok := refFromAPIURL(issue.GetParentIssueURL()); ok {
		item.Parent = &parent
	}
	return item
}

// issueRef is the reference of an issue, in the repository its
// repository_url names, or in repo when it names none.
func issueRef(repo domain.Repository, issue *gh.Issue) domain.Ref {
	if r, ok := repoFromAPIURL(issue.GetRepositoryURL()); ok {
		repo = r
	}
	return domain.Ref{Repo: Normalize(repo), Kind: domain.KindItem, Number: issue.GetNumber()}
}

// issueState maps state and state_reason. A duplicate counts as not
// planned, so that a dependency closed as a duplicate blocks instead of
// passing (spec task-eligibility, "Dependency abandoned").
func issueState(issue *gh.Issue) domain.ItemState {
	if issue.GetState() != "closed" {
		return domain.StateOpen
	}
	switch issue.GetStateReason() {
	case "not_planned", "duplicate":
		return domain.StateClosedNotPlanned
	}
	return domain.StateClosedCompleted
}

// issueType maps the issue type, or failing that the bug and
// enhancement labels.
func issueType(issue *gh.Issue) domain.ItemType {
	switch strings.ToLower(issue.GetType().GetName()) {
	case "task":
		return domain.TypeTask
	case "bug":
		return domain.TypeBug
	case "feature":
		return domain.TypeFeature
	}
	for _, l := range issue.Labels {
		switch strings.ToLower(l.GetName()) {
		case "bug":
			return domain.TypeBug
		case "enhancement", "feature":
			return domain.TypeFeature
		}
	}
	return domain.TypeOther
}

func issueComment(c *gh.IssueComment) domain.Comment {
	return domain.Comment{
		ID:        strconv.FormatInt(c.GetID(), 10),
		Author:    c.GetUser().GetLogin(),
		URL:       c.GetHTMLURL(),
		Body:      c.GetBody(),
		CreatedAt: c.GetCreatedAt().Time,
		UpdatedAt: c.GetUpdatedAt().Time,
	}
}

// repoFromAPIURL reads the repository from an API URL such as
// https://api.github.com/repos/MatteoRosani/Anthill/issues/5.
func repoFromAPIURL(raw string) (domain.Repository, bool) {
	parts, ok := apiPath(raw)
	if !ok || len(parts) < 2 {
		return domain.Repository{}, false
	}
	return Normalize(domain.Repository{Provider: domain.GitHub, Org: parts[0], Name: parts[1]}), true
}

// refFromAPIURL reads an issue reference from an API URL ending in
// /repos/{owner}/{repo}/issues/{number}.
func refFromAPIURL(raw string) (domain.Ref, bool) {
	parts, ok := apiPath(raw)
	if !ok || len(parts) != 4 || parts[2] != "issues" {
		return domain.Ref{}, false
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n <= 0 {
		return domain.Ref{}, false
	}
	repo, _ := repoFromAPIURL(raw)
	return domain.Ref{Repo: repo, Kind: domain.KindItem, Number: n}, true
}

// apiPath returns the path segments after /repos/.
func apiPath(raw string) ([]string, bool) {
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return nil, false
	}
	_, rest, ok := strings.Cut(u.Path, "/repos/")
	if !ok {
		return nil, false
	}
	return strings.Split(strings.Trim(rest, "/"), "/"), true
}
