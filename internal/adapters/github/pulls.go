package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// PullRequests polls the open PRs of repo changed since cur (spec
// work-item-sources, "Pull request ingestion"). The list URL never
// changes, so every poll is a conditional request and an unchanged list
// costs one 304. A PR that was merged or closed leaves the list without
// being reported.
func (a *Adapter) PullRequests(ctx context.Context, repo domain.Repository, cur string) (ports.PullRequestPage, error) {
	repo = Normalize(repo)
	if err := checkRepo(repo); err != nil {
		return ports.PullRequestPage{}, err
	}
	c, err := parseCursor(cur)
	if err != nil {
		return ports.PullRequestPage{}, err
	}
	query := url.Values{
		"state":     {"open"},
		"sort":      {"updated"},
		"direction": {"desc"},
		"per_page":  {strconv.Itoa(perPage)},
	}
	op := "list pull requests of " + repo.String()
	path := fmt.Sprintf("repos/%s/%s/pulls", repo.Org, repo.Name)
	prs, first, err := listPages[*gh.PullRequest](ctx, a, op, path, query, c.ETag)
	if err != nil {
		return ports.PullRequestPage{}, err
	}
	page := ports.PullRequestPage{ServerTime: a.serverTime(first), Cursor: cur}
	if first.StatusCode == http.StatusNotModified {
		return page, nil
	}
	next := cursor{Since: c.Since, ETag: first.Header.Get("ETag")}
	for _, pr := range prs {
		updated := pr.GetUpdatedAt().Time
		if updated.After(next.Since) {
			next.Since = updated
		}
		// since is inclusive, as for issues.
		if updated.Before(c.Since) {
			continue
		}
		mapped, err := a.pullRequest(ctx, repo, pr)
		if err != nil {
			return ports.PullRequestPage{}, err
		}
		page.PullRequests = append(page.PullRequests, mapped)
	}
	page.Cursor = next.String()
	slices.SortFunc(page.PullRequests, func(x, y domain.PullRequest) int { return x.Ref.Number - y.Ref.Number })
	return page, nil
}

// PullRequest reads one PR fresh, with GitHub's time.
func (a *Adapter) PullRequest(ctx context.Context, ref domain.Ref) (domain.PullRequest, time.Time, error) {
	ref = normalizeRef(ref)
	if err := checkRepo(ref.Repo); err != nil {
		return domain.PullRequest{}, time.Time{}, err
	}
	pr, resp, err := a.client.PullRequests.Get(ctx, ref.Repo.Org, ref.Repo.Name, ref.Number)
	if err != nil {
		return domain.PullRequest{}, time.Time{}, a.apiError("read pull request "+ref.Full(), resp, err)
	}
	mapped, err := a.pullRequest(ctx, ref.Repo, pr)
	if err != nil {
		return domain.PullRequest{}, time.Time{}, err
	}
	return mapped, a.serverTime(resp), nil
}

// pullRequest maps a PR and reads its comments and review threads.
func (a *Adapter) pullRequest(ctx context.Context, repo domain.Repository, pr *gh.PullRequest) (domain.PullRequest, error) {
	mapped := a.mapPullRequest(repo, pr)
	comments, _, err := a.issueComments(ctx, mapped.Ref)
	if err != nil {
		return domain.PullRequest{}, err
	}
	reviews, err := a.reviewSummaries(ctx, mapped.Ref)
	if err != nil {
		return domain.PullRequest{}, err
	}
	mapped.Comments = append(comments, reviews...)
	threads, err := a.reviewThreads(ctx, mapped.Ref)
	if err != nil {
		return domain.PullRequest{}, err
	}
	mapped.Threads = threads
	return mapped, nil
}

// mapPullRequest maps the fields of the PR object itself.
func (a *Adapter) mapPullRequest(repo domain.Repository, pr *gh.PullRequest) domain.PullRequest {
	repo = Normalize(repo)
	mapped := domain.PullRequest{
		Ref:          domain.Ref{Repo: repo, Kind: domain.KindPR, Number: pr.GetNumber()},
		URL:          pr.GetHTMLURL(),
		Title:        pr.GetTitle(),
		Body:         pr.GetBody(),
		SourceBranch: pr.GetHead().GetRef(),
		TargetBranch: pr.GetBase().GetRef(),
		Author:       pr.GetUser().GetLogin(),
		HeadCommit:   pr.GetHead().GetSHA(),
		Draft:        pr.GetDraft(),
		Revision:     pr.GetUpdatedAt().UTC().Format(time.RFC3339),
		UpdatedAt:    pr.GetUpdatedAt().Time,
	}
	for _, l := range pr.Labels {
		mapped.Labels = append(mapped.Labels, l.GetName())
	}
	mapped.Item = a.linkedItem(repo, pr)
	return mapped
}

// closingPattern finds a closing keyword and the word after it (GitHub
// docs, "Linking a pull request to an issue using a keyword").
var closingPattern = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+(\S+)`)

// Forms of an issue reference after a closing keyword.
var (
	shortIssuePattern = regexp.MustCompile(`^#([1-9][0-9]*)$`)
	repoIssuePattern  = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)#([1-9][0-9]*)$`)
	urlIssuePattern   = regexp.MustCompile(`^https?://([^/]+)/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/issues/([1-9][0-9]*)$`)
)

// linkedItem finds the item a PR was opened for (spec ant-execution,
// "Session index"): first a closing reference in the body to an issue of
// the same repository, then the ant branch name, so that a PR whose
// "Closes #12" was edited away still links through ant/gh-12-…. A
// closing reference to another repository is not a link.
func (a *Adapter) linkedItem(repo domain.Repository, pr *gh.PullRequest) *domain.Ref {
	for _, m := range closingPattern.FindAllStringSubmatch(pr.GetBody(), -1) {
		if n, ok := a.closingTarget(repo, strings.TrimRight(m[1], ".,;:!?)]")); ok {
			return &domain.Ref{Repo: repo, Kind: domain.KindItem, Number: n}
		}
	}
	// A branch of a fork is not an ant branch of this repository.
	if head := pr.GetHead().GetRepo(); head != nil && !strings.EqualFold(head.GetFullName(), repo.Org+"/"+repo.Name) {
		return nil
	}
	prefix := a.options(repo).BranchPrefix
	branch := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `/gh-([1-9][0-9]*)(?:-|$)`)
	if m := branch.FindStringSubmatch(pr.GetHead().GetRef()); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return &domain.Ref{Repo: repo, Kind: domain.KindItem, Number: n}
		}
	}
	return nil
}

// closingTarget returns the issue number a closing reference names when
// it is an issue of repo.
func (a *Adapter) closingTarget(repo domain.Repository, word string) (int, bool) {
	var number string
	if m := shortIssuePattern.FindStringSubmatch(word); m != nil {
		number = m[1]
	} else if m := repoIssuePattern.FindStringSubmatch(word); m != nil {
		if !sameRepo(repo, m[1], m[2]) {
			return 0, false
		}
		number = m[3]
	} else if m := urlIssuePattern.FindStringSubmatch(word); m != nil {
		if !strings.EqualFold(m[1], a.host) || !sameRepo(repo, m[2], m[3]) {
			return 0, false
		}
		number = m[4]
	} else {
		return 0, false
	}
	n, err := strconv.Atoi(number)
	return n, err == nil
}

func sameRepo(repo domain.Repository, owner, name string) bool {
	return strings.EqualFold(repo.Org, owner) && strings.EqualFold(repo.Name, name)
}

// threadsQuery reads a PR's review threads with their resolution, which
// REST does not give.
const threadsQuery = `query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id isResolved path line originalLine
          comments(first: 100) { nodes { databaseId url body createdAt updatedAt author { login } } }
        }
      }
    }
  }
}`

type gqlThread struct {
	ID           string `json:"id"`
	IsResolved   bool   `json:"isResolved"`
	Path         string `json:"path"`
	Line         *int   `json:"line"`
	OriginalLine *int   `json:"originalLine"`
	Comments     struct {
		Nodes []gqlComment `json:"nodes"`
	} `json:"comments"`
}

type gqlComment struct {
	DatabaseID int64     `json:"databaseId"`
	URL        string    `json:"url"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Author     *struct {
		Login string `json:"login"`
	} `json:"author"`
}

// reviewThreads reads every review thread of a PR, with up to 100
// comments each.
func (a *Adapter) reviewThreads(ctx context.Context, ref domain.Ref) ([]domain.ReviewThread, error) {
	var threads []domain.ReviewThread
	var after *string
	for {
		var data struct {
			Repository struct {
				PullRequest *struct {
					ReviewThreads struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []gqlThread `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		vars := map[string]any{"owner": ref.Repo.Org, "name": ref.Repo.Name, "number": ref.Number, "after": after}
		if err := a.graphql(ctx, "read review threads of "+ref.Full(), threadsQuery, vars, &data); err != nil {
			return nil, err
		}
		pr := data.Repository.PullRequest
		if pr == nil {
			return nil, fmt.Errorf("github: read review threads of %s: %w", ref.Full(), ports.ErrNotFound)
		}
		for _, t := range pr.ReviewThreads.Nodes {
			threads = append(threads, mapThread(t))
		}
		if !pr.ReviewThreads.PageInfo.HasNextPage {
			return threads, nil
		}
		endCursor := pr.ReviewThreads.PageInfo.EndCursor
		after = &endCursor
	}
}

func mapThread(t gqlThread) domain.ReviewThread {
	thread := domain.ReviewThread{ID: t.ID, Path: t.Path, Resolved: t.IsResolved}
	switch {
	case t.Line != nil:
		thread.Line = *t.Line
	case t.OriginalLine != nil:
		// The thread is outdated: its line is gone from the head commit.
		thread.Line = *t.OriginalLine
	}
	for _, c := range t.Comments.Nodes {
		thread.Comments = append(thread.Comments, c.comment())
	}
	return thread
}

func (c gqlComment) comment() domain.Comment {
	comment := domain.Comment{
		ID:        reviewCommentID(c.DatabaseID),
		URL:       c.URL,
		Body:      c.Body,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
	if c.Author != nil {
		comment.Author = c.Author.Login
	}
	return comment
}

// Review comments and issue comments have separate IDs and separate
// endpoints, so a review comment's ID carries an r in front, such as
// r123, and EditComment picks the endpoint by it.
const reviewPrefix = "r"

// The summary of a submitted review, the text typed with Approve or
// Request changes, is a PR comment whose ID carries a v in front, such
// as v456, because reviews have IDs of their own. Anthill never edits
// one, so EditComment refuses it as not found.
const summaryPrefix = "v"

// reviewSummaries reads the submitted reviews of a PR and returns those
// with a summary as comments, so that they reach change detection like
// any other comment. A pending review is visible to its author only and
// is skipped.
func (a *Adapter) reviewSummaries(ctx context.Context, ref domain.Ref) ([]domain.Comment, error) {
	list, resp, err := collect(func(o gh.ListOptions) ([]*gh.PullRequestReview, *gh.Response, error) {
		return a.client.PullRequests.ListReviews(ctx, ref.Repo.Org, ref.Repo.Name, ref.Number, &o)
	})
	if err != nil {
		return nil, a.apiError("list reviews of "+ref.Full(), resp, err)
	}
	var out []domain.Comment
	for _, r := range list {
		if r.GetState() == "PENDING" || strings.TrimSpace(r.GetBody()) == "" {
			continue
		}
		out = append(out, domain.Comment{
			ID:        summaryPrefix + strconv.FormatInt(r.GetID(), 10),
			Author:    r.GetUser().GetLogin(),
			URL:       r.GetHTMLURL(),
			Body:      r.GetBody(),
			CreatedAt: r.GetSubmittedAt().Time,
			UpdatedAt: r.GetSubmittedAt().Time,
		})
	}
	return out, nil
}

func reviewCommentID(id int64) string {
	return reviewPrefix + strconv.FormatInt(id, 10)
}
