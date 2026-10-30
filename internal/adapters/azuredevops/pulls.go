package azuredevops

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// prPage is the page size of the pull request list.
const prPage = 100

// prJSON is a Git pull request as the REST API returns it.
type prJSON struct {
	PullRequestID         int          `json:"pullRequestId"`
	Title                 string       `json:"title"`
	Description           string       `json:"description"`
	SourceRefName         string       `json:"sourceRefName"`
	TargetRefName         string       `json:"targetRefName"`
	CreatedBy             *identityRef `json:"createdBy"`
	CreationDate          time.Time    `json:"creationDate"`
	IsDraft               bool         `json:"isDraft"`
	LastMergeSourceCommit *struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeSourceCommit"`
	Labels []struct {
		Name   string `json:"name"`
		Active *bool  `json:"active"`
	} `json:"labels"`
}

// threadJSON is a PR comment thread. A thread with a threadContext is
// anchored to lines of a file; one without is a general comment.
type threadJSON struct {
	ID            int    `json:"id"`
	Status        string `json:"status"`
	ThreadContext *struct {
		FilePath       string    `json:"filePath"`
		RightFileStart *position `json:"rightFileStart"`
		LeftFileStart  *position `json:"leftFileStart"`
	} `json:"threadContext"`
	Comments        []prCommentJSON `json:"comments"`
	Properties      map[string]any  `json:"properties"`
	IsDeleted       bool            `json:"isDeleted"`
	LastUpdatedDate time.Time       `json:"lastUpdatedDate"`
}

type position struct {
	Line   int `json:"line"`
	Offset int `json:"offset"`
}

type prCommentJSON struct {
	ID              int          `json:"id"`
	ParentCommentID int          `json:"parentCommentId"`
	Author          *identityRef `json:"author"`
	Content         string       `json:"content"`
	PublishedDate   time.Time    `json:"publishedDate"`
	LastUpdatedDate time.Time    `json:"lastUpdatedDate"`
	CommentType     string       `json:"commentType"`
	IsDeleted       bool         `json:"isDeleted"`
}

// resolvedStatuses are the thread statuses that count as resolved: the
// thread was fixed, closed, or settled as won't fix or by design. Active,
// pending and a thread without status are unresolved.
var resolvedStatuses = []string{"fixed", "wontFix", "closed", "byDesign"}

// PullRequests polls the active PRs of a Git repository
// (organization/project/name) and returns those whose content changed
// since cur (spec work-item-sources, "Pull request ingestion"). Azure
// DevOps gives a PR no update time, so every poll reads every active PR
// with its threads and linked work items, and the cursor keeps a
// fingerprint of each. A PR that was completed or abandoned leaves the
// list without being reported.
func (a *Adapter) PullRequests(ctx context.Context, repo domain.Repository, cur string) (ports.PullRequestPage, error) {
	repo = Normalize(repo)
	if err := a.checkCodeRepo(repo); err != nil {
		return ports.PullRequestPage{}, err
	}
	c, err := parseCursor[prCursor](cur)
	if err != nil {
		return ports.PullRequestPage{}, err
	}
	var page ports.PullRequestPage
	next := prCursor{PRs: map[int]string{}}
	for skip := 0; ; skip += prPage {
		var list listOf[prJSON]
		resp, err := a.send(ctx, call{
			op:     "list pull requests of " + repo.String(),
			method: http.MethodGet,
			url: a.api(url.Values{
				"searchCriteria.status": {"active"},
				"$top":                  {strconv.Itoa(prPage)},
				"$skip":                 {strconv.Itoa(skip)},
			}, repo.Org, repo.Project, "_apis", "git", "repositories", repo.Name, "pullrequests"),
			out: &list,
		})
		if err != nil {
			return ports.PullRequestPage{}, err
		}
		if skip == 0 {
			page.ServerTime = a.serverTime(resp)
		}
		for _, p := range list.Value {
			mapped, err := a.pullRequest(ctx, repo, p)
			if err != nil {
				return ports.PullRequestPage{}, err
			}
			next.PRs[p.PullRequestID] = mapped.Revision
			if c.PRs[p.PullRequestID] != mapped.Revision {
				page.PullRequests = append(page.PullRequests, mapped)
			}
		}
		if len(list.Value) < prPage {
			break
		}
	}
	page.Cursor = cursorString(next)
	slices.SortFunc(page.PullRequests, func(x, y domain.PullRequest) int { return x.Ref.Number - y.Ref.Number })
	return page, nil
}

// PullRequest reads one PR fresh, with Azure DevOps' time.
func (a *Adapter) PullRequest(ctx context.Context, ref domain.Ref) (domain.PullRequest, time.Time, error) {
	ref = normalizeRef(ref)
	if err := a.checkCodeRepo(ref.Repo); err != nil {
		return domain.PullRequest{}, time.Time{}, err
	}
	p, resp, err := a.readPR(ctx, ref)
	if err != nil {
		return domain.PullRequest{}, time.Time{}, err
	}
	mapped, err := a.pullRequest(ctx, ref.Repo, p)
	if err != nil {
		return domain.PullRequest{}, time.Time{}, err
	}
	return mapped, a.serverTime(resp), nil
}

func (a *Adapter) readPR(ctx context.Context, ref domain.Ref) (prJSON, *http.Response, error) {
	var p prJSON
	resp, err := a.send(ctx, call{
		op:     "read pull request " + ref.Full(),
		method: http.MethodGet,
		url:    a.prURL(ref, nil),
		out:    &p,
	})
	return p, resp, err
}

// prURL is the URL of a PR, or of something under it.
func (a *Adapter) prURL(ref domain.Ref, query url.Values, sub ...string) string {
	segments := append([]string{ref.Repo.Org, ref.Repo.Project, "_apis", "git", "repositories", ref.Repo.Name, "pullrequests", strconv.Itoa(ref.Number)}, sub...)
	return a.api(query, segments...)
}

// pullRequest maps a PR and reads its threads and linked work items. Its
// Revision is a fingerprint of its content.
func (a *Adapter) pullRequest(ctx context.Context, repo domain.Repository, p prJSON) (domain.PullRequest, error) {
	ref := domain.Ref{Repo: repo, Kind: domain.KindPR, Number: p.PullRequestID}
	threads, _, err := a.threads(ctx, ref)
	if err != nil {
		return domain.PullRequest{}, err
	}
	var linked listOf[struct {
		ID string `json:"id"`
	}]
	if _, err := a.send(ctx, call{
		op:     "list work items of " + ref.Full(),
		method: http.MethodGet,
		url:    a.prURL(ref, nil, "workitems"),
		out:    &linked,
	}); err != nil {
		return domain.PullRequest{}, err
	}
	var native []int
	for _, l := range linked.Value {
		if id, err := strconv.Atoi(l.ID); err == nil {
			native = append(native, id)
		}
	}
	mapped := a.mapPullRequest(repo, p, threads)
	mapped.Item = a.linkedItem(repo, p, native)
	mapped.Revision = fingerprint(mapped)
	return mapped, nil
}

// mapPullRequest maps a PR and its threads: anchored threads become
// review threads, and the comments of general threads become the PR's
// comments. System threads, such as "pushed a commit", are left out.
func (a *Adapter) mapPullRequest(repo domain.Repository, p prJSON, threads []threadJSON) domain.PullRequest {
	ref := domain.Ref{Repo: repo, Kind: domain.KindPR, Number: p.PullRequestID}
	mapped := domain.PullRequest{
		Ref:          ref,
		URL:          a.webURL(repo.Org, repo.Project, "_git", repo.Name, "pullrequest", strconv.Itoa(p.PullRequestID)),
		Title:        p.Title,
		Body:         p.Description,
		SourceBranch: strings.TrimPrefix(p.SourceRefName, "refs/heads/"),
		TargetBranch: strings.TrimPrefix(p.TargetRefName, "refs/heads/"),
		Author:       p.CreatedBy.login(),
		Draft:        p.IsDraft,
		UpdatedAt:    p.CreationDate,
	}
	if p.LastMergeSourceCommit != nil {
		mapped.HeadCommit = p.LastMergeSourceCommit.CommitID
	}
	for _, l := range p.Labels {
		if l.Active == nil || *l.Active {
			mapped.Labels = append(mapped.Labels, l.Name)
		}
	}
	for _, t := range threads {
		if t.IsDeleted || systemThread(t) {
			continue
		}
		if t.LastUpdatedDate.After(mapped.UpdatedAt) {
			mapped.UpdatedAt = t.LastUpdatedDate
		}
		comments := a.threadComments(ref, t)
		if t.ThreadContext == nil {
			mapped.Comments = append(mapped.Comments, comments...)
			continue
		}
		thread := domain.ReviewThread{
			ID:       strconv.Itoa(t.ID),
			Path:     strings.TrimPrefix(t.ThreadContext.FilePath, "/"),
			Resolved: slices.Contains(resolvedStatuses, t.Status),
			Comments: comments,
		}
		switch {
		case t.ThreadContext.RightFileStart != nil:
			thread.Line = t.ThreadContext.RightFileStart.Line
		case t.ThreadContext.LeftFileStart != nil:
			thread.Line = t.ThreadContext.LeftFileStart.Line
		}
		mapped.Threads = append(mapped.Threads, thread)
	}
	slices.SortStableFunc(mapped.Comments, compareComments)
	return mapped
}

// systemThread reports whether Azure DevOps wrote the thread itself, such
// as a vote or a pushed commit.
func systemThread(t threadJSON) bool {
	if _, ok := t.Properties["CodeReviewThreadType"]; ok {
		return true
	}
	return slices.ContainsFunc(t.Comments, func(c prCommentJSON) bool { return c.CommentType == "system" })
}

// threadComments maps the comments of a thread, oldest first.
func (a *Adapter) threadComments(ref domain.Ref, t threadJSON) []domain.Comment {
	var out []domain.Comment
	for _, c := range t.Comments {
		if c.IsDeleted {
			continue
		}
		out = append(out, a.prComment(ref, t.ID, c))
	}
	slices.SortStableFunc(out, compareComments)
	return out
}

// A PR comment is identified by its thread and its number in the thread,
// so its ID is <thread>.<comment>, such as 7.1.
func prCommentID(thread, comment int) string {
	return strconv.Itoa(thread) + "." + strconv.Itoa(comment)
}

func (a *Adapter) prComment(ref domain.Ref, thread int, c prCommentJSON) domain.Comment {
	return domain.Comment{
		ID:     prCommentID(thread, c.ID),
		Author: c.Author.login(),
		URL: a.webURL(ref.Repo.Org, ref.Repo.Project, "_git", ref.Repo.Name, "pullrequest", strconv.Itoa(ref.Number)) +
			"?discussionId=" + strconv.Itoa(thread),
		Body:      c.Content,
		CreatedAt: c.PublishedDate,
		UpdatedAt: c.LastUpdatedDate,
	}
}

// compareComments orders comments by creation time, then by ID, numbers
// compared as numbers (spec work-claiming, "Claim acquisition resolves
// races deterministically").
func compareComments(x, y domain.Comment) int {
	if c := x.CreatedAt.Compare(y.CreatedAt); c != 0 {
		return c
	}
	xs, ys := strings.Split(x.ID, "."), strings.Split(y.ID, ".")
	for i := 0; i < len(xs) && i < len(ys); i++ {
		xi, _ := strconv.Atoi(xs[i])
		yi, _ := strconv.Atoi(ys[i])
		if c := cmp.Compare(xi, yi); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(xs), len(ys))
}

// threads reads every thread of a PR, with the response.
func (a *Adapter) threads(ctx context.Context, ref domain.Ref) ([]threadJSON, *http.Response, error) {
	var list listOf[threadJSON]
	resp, err := a.send(ctx, call{
		op:     "list threads of " + ref.Full(),
		method: http.MethodGet,
		url:    a.prURL(ref, nil, "threads"),
		out:    &list,
	})
	if err != nil {
		return nil, nil, err
	}
	return list.Value, resp, nil
}

// fingerprint is a short hash of a PR's content, without the revision and
// the update time, so that a poll can tell whether it changed.
func fingerprint(pr domain.PullRequest) string {
	pr.Revision, pr.UpdatedAt = "", time.Time{}
	data, _ := json.Marshal(pr) // A PullRequest always marshals.
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// closingPattern finds a closing keyword followed by an Azure Boards
// reference, such as "Closes AB#12" or "fixes: AB#12".
var closingPattern = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+AB#([1-9][0-9]*)\b`)

// linkedItem finds the work item a PR was opened for (spec ant-execution,
// "Session index"): first the work items linked to the PR natively, then
// a closing keyword followed by AB#<id> in the description, then the ant
// branch name, such as ant/ado-12-fix-login. A work item ID is unique in
// the organization; the item is taken to be in the PR's project.
func (a *Adapter) linkedItem(repo domain.Repository, p prJSON, native []int) *domain.Ref {
	project := domain.Repository{Provider: domain.AzureDevOps, Org: repo.Org, Project: repo.Project}
	ref := func(n int) *domain.Ref { return &domain.Ref{Repo: project, Kind: domain.KindItem, Number: n} }
	if len(native) > 0 {
		return ref(slices.Min(native))
	}
	if m := closingPattern.FindStringSubmatch(p.Description); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return ref(n)
		}
	}
	prefix := a.options(repo).BranchPrefix
	branch := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `/ado-([1-9][0-9]*)(?:-|$)`)
	if m := branch.FindStringSubmatch(strings.TrimPrefix(p.SourceRefName, "refs/heads/")); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return ref(n)
		}
	}
	return nil
}
