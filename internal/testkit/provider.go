package testkit

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Op names one kind of provider call, for the request log, barriers and
// injected failures.
type Op string

// The provider calls. AddComment, EditComment and CanWrite serve both
// TrackerPort and CodeHostPort: the ref's kind decides whether they act on
// an item or a PR.
const (
	OpItems            Op = "items"
	OpItem             Op = "item"
	OpAddComment       Op = "add-comment"
	OpEditComment      Op = "edit-comment"
	OpAddLabel         Op = "add-label"
	OpRemoveLabel      Op = "remove-label"
	OpCanWrite         Op = "can-write"
	OpClaims           Op = "claims"
	OpPost             Op = "post"
	OpEdit             Op = "edit"
	OpRetract          Op = "retract"
	OpMark             Op = "mark"
	OpUnmark           Op = "unmark"
	OpPullRequests     Op = "pull-requests"
	OpPullRequest      Op = "pull-request"
	OpOpenPullRequest  Op = "open-pull-request"
	OpUpdatePR         Op = "update-pull-request"
	OpAddReviewComment Op = "add-review-comment"
	OpReplyToThread    Op = "reply-to-thread"
	OpResolveThread    Op = "resolve-thread"
)

// Request is one call received by the fake provider.
type Request struct {
	Op    Op
	Login string
	// Ref is set for calls on one item or PR, Repo for calls on a
	// repository.
	Ref  domain.Ref
	Repo domain.Repository
	// At is the provider's time when the call arrived.
	At time.Time
}

// Provider is an in-memory provider that plays tracker, code host and
// claim store, like a GitHub adapter does. Each instance talks to it
// through its own Client. See the package documentation for the options
// that simulate races, lag, rate limits and errors.
type Provider struct {
	mu    sync.Mutex
	clock *Clock
	// seq numbers every change, for comment IDs, revisions and cursors.
	seq      int
	items    map[domain.Ref]*itemEntry
	prs      map[domain.Ref]*prEntry
	writers  map[domain.Repository]map[string]bool
	requests []Request
	fail     map[Op]error
	failNext map[Op][]error
	barriers map[Op]*barrier
	readLag  time.Duration
	limitTo  time.Time
	prNumber int
}

type itemEntry struct {
	item domain.WorkItem
	seq  int
}

type prEntry struct {
	pr  domain.PullRequest
	seq int
}

type barrier struct {
	n       int
	arrived int
	release chan struct{}
}

// NewProvider returns an empty provider whose server time is clock's time.
func NewProvider(clock *Clock) *Provider {
	return &Provider{
		clock:    clock,
		items:    map[domain.Ref]*itemEntry{},
		prs:      map[domain.Ref]*prEntry{},
		writers:  map[domain.Repository]map[string]bool{},
		fail:     map[Op]error{},
		failNext: map[Op][]error{},
		barriers: map[Op]*barrier{},
	}
}

// Client returns the view of the provider for one developer login. It
// implements TrackerPort, CodeHostPort and ClaimStore.
func (p *Provider) Client(login string) *ProviderClient {
	return &ProviderClient{p: p, login: login}
}

// PutItem stores an item, replacing any item with the same ref, as a
// change seen by the next poll. Its Revision is set by the provider.
func (p *Provider) PutItem(item domain.WorkItem) {
	p.mu.Lock()
	defer p.mu.Unlock()
	item = cloneItem(item)
	item.Revision, item.UpdatedAt = p.bump(), p.clock.Now()
	p.items[item.Ref] = &itemEntry{item: item, seq: p.seq}
}

// PutPullRequest stores a pull request, replacing any with the same ref.
func (p *Provider) PutPullRequest(pr domain.PullRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pr = clonePR(pr)
	pr.Revision, pr.UpdatedAt = p.bump(), p.clock.Now()
	p.prs[pr.Ref] = &prEntry{pr: pr, seq: p.seq}
	p.prNumber = max(p.prNumber, pr.Ref.Number)
}

// EditItem changes a stored item as a human would, such as editing its
// body or removing a label. It panics if the item does not exist.
func (p *Provider) EditItem(ref domain.Ref, edit func(*domain.WorkItem)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.mustItem(ref)
	edit(&e.item)
	p.touchItem(e)
}

// EditPullRequest changes a stored PR as a human would, such as pushing a
// commit. It panics if the PR does not exist.
func (p *Provider) EditPullRequest(ref domain.Ref, edit func(*domain.PullRequest)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.prs[ref]
	if !ok {
		panic(fmt.Sprintf("testkit.Provider: no pull request %s", ref.Full()))
	}
	edit(&e.pr)
	p.touchPR(e)
}

// Comment posts a comment as author, such as a human reply.
func (p *Provider) Comment(ref domain.Ref, author, body string) domain.Comment {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, err := p.addComment(ref, author, body)
	if err != nil {
		panic(fmt.Sprintf("testkit.Provider: %v", err))
	}
	return c
}

// Item returns a stored item as it is now, without lag, for assertions.
func (p *Provider) Item(ref domain.Ref) (domain.WorkItem, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.items[ref]
	if !ok {
		return domain.WorkItem{}, false
	}
	return cloneItem(e.item), true
}

// PullRequest returns a stored PR as it is now, for assertions.
func (p *Provider) PullRequest(ref domain.Ref) (domain.PullRequest, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.prs[ref]
	if !ok {
		return domain.PullRequest{}, false
	}
	return clonePR(e.pr), true
}

// AddWriter gives logins write access to a repository.
func (p *Provider) AddWriter(repo domain.Repository, logins ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.writers[repo] == nil {
		p.writers[repo] = map[string]bool{}
	}
	for _, l := range logins {
		p.writers[repo][l] = true
	}
}

// Requests returns every call received so far, in arrival order,
// including refused ones.
func (p *Provider) Requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.requests)
}

// Fail makes every call of op fail with err until Fail(op, nil).
func (p *Provider) Fail(op Op, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		delete(p.fail, op)
		return
	}
	p.fail[op] = err
}

// FailNext makes the next call of op fail with err. Several calls queue
// several failures.
func (p *Provider) FailNext(op Op, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failNext[op] = append(p.failNext[op], err)
}

// RateLimit refuses every call with a *ports.RateLimitError until the
// provider's time reaches until.
func (p *Provider) RateLimit(until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.limitTo = until
}

// ReadLag hides, from every read, comments created less than lag ago by
// the provider's time, like a provider with read-after-write lag.
func (p *Provider) ReadLag(lag time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.readLag = lag
}

// Barrier holds the next n calls of op after the provider has carried
// them out, until all n have arrived; then all of them return. Two
// instances that both post a claim under Barrier(OpPost, 2) therefore
// both re-read after both comments exist, and the provider's order of
// the two posts decides the winner.
func (p *Provider) Barrier(op Op, n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.barriers[op] = &barrier{n: n, release: make(chan struct{})}
}

// bump moves the change sequence and returns it as a revision.
func (p *Provider) bump() string {
	p.seq++
	return strconv.Itoa(p.seq)
}

func (p *Provider) touchItem(e *itemEntry) {
	e.item.Revision, e.item.UpdatedAt = p.bump(), p.clock.Now()
	e.seq = p.seq
}

func (p *Provider) touchPR(e *prEntry) {
	e.pr.Revision, e.pr.UpdatedAt = p.bump(), p.clock.Now()
	e.seq = p.seq
}

func (p *Provider) mustItem(ref domain.Ref) *itemEntry {
	e, ok := p.items[ref]
	if !ok {
		panic(fmt.Sprintf("testkit.Provider: no item %s", ref.Full()))
	}
	return e
}

// call logs a request and returns the error it must fail with, if any.
// It must be called with p.mu held.
func (p *Provider) call(op Op, login string, ref domain.Ref, repo domain.Repository) error {
	now := p.clock.Now()
	p.requests = append(p.requests, Request{Op: op, Login: login, Ref: ref, Repo: repo, At: now})
	if now.Before(p.limitTo) {
		return &ports.RateLimitError{Reset: p.limitTo}
	}
	if errs := p.failNext[op]; len(errs) > 0 {
		p.failNext[op] = errs[1:]
		return errs[0]
	}
	return p.fail[op]
}

// wait holds a call at op's barrier, if one is set. It must be called
// without p.mu held, after the call was carried out.
func (p *Provider) wait(ctx context.Context, op Op) error {
	p.mu.Lock()
	b := p.barriers[op]
	if b == nil {
		p.mu.Unlock()
		return nil
	}
	b.arrived++
	if b.arrived == b.n {
		close(b.release)
		delete(p.barriers, op)
	}
	p.mu.Unlock()
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// visible drops the comments hidden by the read lag.
func (p *Provider) visible(comments []domain.Comment) []domain.Comment {
	if p.readLag <= 0 {
		return slices.Clone(comments)
	}
	cutoff := p.clock.Now().Add(-p.readLag)
	var out []domain.Comment
	for _, c := range comments {
		if !c.CreatedAt.After(cutoff) {
			out = append(out, c)
		}
	}
	return out
}

// comments returns the comment list of an item or PR.
func (p *Provider) comments(ref domain.Ref) (*[]domain.Comment, func(), error) {
	if ref.Kind == domain.KindPR {
		e, ok := p.prs[ref]
		if !ok {
			return nil, nil, fmt.Errorf("pull request %s: %w", ref.Full(), ports.ErrNotFound)
		}
		return &e.pr.Comments, func() { p.touchPR(e) }, nil
	}
	e, ok := p.items[ref]
	if !ok {
		return nil, nil, fmt.Errorf("item %s: %w", ref.Full(), ports.ErrNotFound)
	}
	return &e.item.Comments, func() { p.touchItem(e) }, nil
}

func (p *Provider) newComment(author, body string) domain.Comment {
	now := p.clock.Now()
	id := p.bump()
	return domain.Comment{ID: id, Author: author, URL: "https://fake.test/comment/" + id, Body: body, CreatedAt: now, UpdatedAt: now}
}

func (p *Provider) addComment(ref domain.Ref, author, body string) (domain.Comment, error) {
	list, touch, err := p.comments(ref)
	if err != nil {
		return domain.Comment{}, err
	}
	c := p.newComment(author, body)
	*list = append(*list, c)
	touch()
	return c, nil
}

func (p *Provider) editComment(ref domain.Ref, id, body string) (domain.Comment, error) {
	list, touch, err := p.comments(ref)
	if err != nil {
		return domain.Comment{}, err
	}
	for i := range *list {
		if (*list)[i].ID == id {
			(*list)[i].Body = body
			(*list)[i].UpdatedAt = p.clock.Now()
			touch()
			return (*list)[i], nil
		}
	}
	return domain.Comment{}, fmt.Errorf("comment %s on %s: %w", id, ref.Full(), ports.ErrNotFound)
}

func (p *Provider) labels(ref domain.Ref) (*[]string, func(), error) {
	if ref.Kind == domain.KindPR {
		e, ok := p.prs[ref]
		if !ok {
			return nil, nil, fmt.Errorf("pull request %s: %w", ref.Full(), ports.ErrNotFound)
		}
		return &e.pr.Labels, func() { p.touchPR(e) }, nil
	}
	e, ok := p.items[ref]
	if !ok {
		return nil, nil, fmt.Errorf("item %s: %w", ref.Full(), ports.ErrNotFound)
	}
	return &e.item.Labels, func() { p.touchItem(e) }, nil
}

func cloneItem(i domain.WorkItem) domain.WorkItem {
	i.Labels = slices.Clone(i.Labels)
	i.Assignees = slices.Clone(i.Assignees)
	i.Dependencies = slices.Clone(i.Dependencies)
	i.Children = slices.Clone(i.Children)
	i.Comments = slices.Clone(i.Comments)
	if i.Parent != nil {
		parent := *i.Parent
		i.Parent = &parent
	}
	return i
}

func clonePR(pr domain.PullRequest) domain.PullRequest {
	pr.Labels = slices.Clone(pr.Labels)
	pr.Comments = slices.Clone(pr.Comments)
	threads := make([]domain.ReviewThread, len(pr.Threads))
	for i, t := range pr.Threads {
		t.Comments = slices.Clone(t.Comments)
		threads[i] = t
	}
	pr.Threads = threads
	if pr.Item != nil {
		item := *pr.Item
		pr.Item = &item
	}
	return pr
}

// ProviderClient is one developer's view of a Provider. Its login is the
// author of the comments it posts and the assignee of the claims it marks.
type ProviderClient struct {
	p     *Provider
	login string
}

var (
	_ ports.TrackerPort  = (*ProviderClient)(nil)
	_ ports.CodeHostPort = (*ProviderClient)(nil)
	_ ports.ClaimStore   = (*ProviderClient)(nil)
)

// Items returns the items of repo changed since cursor, every item when
// cursor is empty, in ref order.
func (c *ProviderClient) Items(_ context.Context, repo domain.Repository, cursor string) (ports.ItemPage, error) {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call(OpItems, c.login, domain.Ref{}, repo); err != nil {
		return ports.ItemPage{}, err
	}
	since, _ := strconv.Atoi(cursor)
	page := ports.ItemPage{Cursor: strconv.Itoa(p.seq), ServerTime: p.clock.Now()}
	for _, e := range p.items {
		if e.item.Ref.Repo == repo && e.seq > since {
			item := cloneItem(e.item)
			item.Comments = p.visible(item.Comments)
			page.Items = append(page.Items, item)
		}
	}
	slices.SortFunc(page.Items, func(a, b domain.WorkItem) int { return a.Ref.Number - b.Ref.Number })
	return page, nil
}

// Item returns one item and the provider's time.
func (c *ProviderClient) Item(_ context.Context, ref domain.Ref) (domain.WorkItem, time.Time, error) {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call(OpItem, c.login, ref, ref.Repo); err != nil {
		return domain.WorkItem{}, time.Time{}, err
	}
	e, ok := p.items[ref]
	if !ok {
		return domain.WorkItem{}, time.Time{}, fmt.Errorf("item %s: %w", ref.Full(), ports.ErrNotFound)
	}
	item := cloneItem(e.item)
	item.Comments = p.visible(item.Comments)
	return item, p.clock.Now(), nil
}

// AddComment posts an Anthill comment on an item or PR. A comment with
// the same marker ID is returned instead of posting again.
func (c *ProviderClient) AddComment(ctx context.Context, ref domain.Ref, ac domain.AnthillComment) (domain.Comment, error) {
	return c.post(ctx, OpAddComment, ref, ac)
}

// EditComment replaces a comment's text.
func (c *ProviderClient) EditComment(ctx context.Context, ref domain.Ref, commentID string, ac domain.AnthillComment) (domain.Comment, error) {
	return c.edit(ctx, OpEditComment, ref, commentID, ac)
}

// AddLabel adds a label to an item or PR; adding a present label does
// nothing.
func (c *ProviderClient) AddLabel(_ context.Context, ref domain.Ref, label string) error {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call(OpAddLabel, c.login, ref, ref.Repo); err != nil {
		return err
	}
	return p.addLabel(ref, label)
}

func (p *Provider) addLabel(ref domain.Ref, label string) error {
	labels, touch, err := p.labels(ref)
	if err != nil {
		return err
	}
	if !domain.HasLabel(*labels, label) {
		*labels = append(*labels, label)
		touch()
	}
	return nil
}

// RemoveLabel removes a label; removing a missing label does nothing.
func (c *ProviderClient) RemoveLabel(_ context.Context, ref domain.Ref, label string) error {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call(OpRemoveLabel, c.login, ref, ref.Repo); err != nil {
		return err
	}
	return p.removeLabel(ref, label)
}

func (p *Provider) removeLabel(ref domain.Ref, label string) error {
	labels, touch, err := p.labels(ref)
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(*labels, func(l string) bool { return strings.EqualFold(l, label) }); i >= 0 {
		*labels = slices.Delete(*labels, i, i+1)
		touch()
	}
	return nil
}

// CanWrite reports whether login was given write access with AddWriter.
func (c *ProviderClient) CanWrite(_ context.Context, repo domain.Repository, login string) (bool, error) {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call(OpCanWrite, c.login, domain.Ref{}, repo); err != nil {
		return false, err
	}
	return p.writers[repo][login], nil
}

// Claims returns all visible comments of the item or PR, its revision
// and the provider's time.
func (c *ProviderClient) Claims(ctx context.Context, ref domain.Ref) (ports.ClaimPage, error) {
	p := c.p
	p.mu.Lock()
	page, err := p.claims(c.login, ref)
	p.mu.Unlock()
	if err != nil {
		return ports.ClaimPage{}, err
	}
	return page, p.wait(ctx, OpClaims)
}

func (p *Provider) claims(login string, ref domain.Ref) (ports.ClaimPage, error) {
	if err := p.call(OpClaims, login, ref, ref.Repo); err != nil {
		return ports.ClaimPage{}, err
	}
	list, _, err := p.comments(ref)
	if err != nil {
		return ports.ClaimPage{}, err
	}
	page := ports.ClaimPage{Comments: p.visible(*list), ServerTime: p.clock.Now()}
	if e, ok := p.items[ref]; ok {
		page.Revision = e.item.Revision
	}
	return page, nil
}

// Post posts a claim comment.
func (c *ProviderClient) Post(ctx context.Context, ref domain.Ref, ac domain.AnthillComment) (domain.Comment, error) {
	return c.post(ctx, OpPost, ref, ac)
}

// Edit replaces a claim comment's text, which moves its UpdatedAt.
func (c *ProviderClient) Edit(ctx context.Context, ref domain.Ref, commentID string, ac domain.AnthillComment) (domain.Comment, error) {
	return c.edit(ctx, OpEdit, ref, commentID, ac)
}

// Retract deletes a comment.
func (c *ProviderClient) Retract(ctx context.Context, ref domain.Ref, commentID string) error {
	p := c.p
	p.mu.Lock()
	err := p.retract(c.login, ref, commentID)
	p.mu.Unlock()
	if err != nil {
		return err
	}
	return p.wait(ctx, OpRetract)
}

func (p *Provider) retract(login string, ref domain.Ref, id string) error {
	if err := p.call(OpRetract, login, ref, ref.Repo); err != nil {
		return err
	}
	list, touch, err := p.comments(ref)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(*list, func(c domain.Comment) bool { return c.ID == id })
	if i < 0 {
		return fmt.Errorf("comment %s on %s: %w", id, ref.Full(), ports.ErrNotFound)
	}
	*list = slices.Delete(*list, i, i+1)
	touch()
	return nil
}

// Mark adds the claim label and, when asked, the client's login as
// assignee. A revision that is not the item's current one fails with
// ports.ErrConflict.
func (c *ProviderClient) Mark(ctx context.Context, ref domain.Ref, m ports.ClaimMark) (bool, error) {
	p := c.p
	p.mu.Lock()
	assigned, err := p.mark(c.login, ref, m)
	p.mu.Unlock()
	if err != nil {
		return false, err
	}
	return assigned, p.wait(ctx, OpMark)
}

func (p *Provider) mark(login string, ref domain.Ref, m ports.ClaimMark) (bool, error) {
	if err := p.call(OpMark, login, ref, ref.Repo); err != nil {
		return false, err
	}
	if m.Revision != "" {
		if e, ok := p.items[ref]; ok && e.item.Revision != m.Revision {
			return false, fmt.Errorf("item %s is at revision %s, not %s: %w", ref.Full(), e.item.Revision, m.Revision, ports.ErrConflict)
		}
	}
	if err := p.addLabel(ref, m.Label); err != nil {
		return false, err
	}
	if !m.Assign {
		return false, nil
	}
	e, ok := p.items[ref]
	if !ok {
		return false, fmt.Errorf("only items have assignees, not %s", ref.Full())
	}
	if slices.Contains(e.item.Assignees, login) {
		return false, nil
	}
	e.item.Assignees = append(e.item.Assignees, login)
	p.touchItem(e)
	return true, nil
}

// Unmark removes the claim label and, when asked, the client's login
// from the assignees.
func (c *ProviderClient) Unmark(_ context.Context, ref domain.Ref, label string, unassign bool) error {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call(OpUnmark, c.login, ref, ref.Repo); err != nil {
		return err
	}
	if err := p.removeLabel(ref, label); err != nil {
		return err
	}
	if e, ok := p.items[ref]; ok && unassign {
		if i := slices.Index(e.item.Assignees, c.login); i >= 0 {
			e.item.Assignees = slices.Delete(e.item.Assignees, i, i+1)
			p.touchItem(e)
		}
	}
	return nil
}

func (c *ProviderClient) post(ctx context.Context, op Op, ref domain.Ref, ac domain.AnthillComment) (domain.Comment, error) {
	p := c.p
	p.mu.Lock()
	comment, err := p.postLocked(op, c.login, ref, ac)
	p.mu.Unlock()
	if err != nil {
		return domain.Comment{}, err
	}
	return comment, p.wait(ctx, op)
}

func (p *Provider) postLocked(op Op, login string, ref domain.Ref, ac domain.AnthillComment) (domain.Comment, error) {
	if err := p.call(op, login, ref, ref.Repo); err != nil {
		return domain.Comment{}, err
	}
	if id := ac.Marker.ID; id != "" {
		list, _, err := p.comments(ref)
		if err != nil {
			return domain.Comment{}, err
		}
		for _, existing := range *list {
			if m, ok := domain.FindMarker(existing.Body); ok && m.ID == id {
				return existing, nil
			}
		}
	}
	return p.addComment(ref, login, ac.String())
}

func (c *ProviderClient) edit(ctx context.Context, op Op, ref domain.Ref, id string, ac domain.AnthillComment) (domain.Comment, error) {
	p := c.p
	p.mu.Lock()
	comment, err := func() (domain.Comment, error) {
		if err := p.call(op, c.login, ref, ref.Repo); err != nil {
			return domain.Comment{}, err
		}
		return p.editComment(ref, id, ac.String())
	}()
	p.mu.Unlock()
	if err != nil {
		return domain.Comment{}, err
	}
	return comment, p.wait(ctx, op)
}

// PullRequests returns the PRs of repo changed since cursor, every PR
// when cursor is empty, in ref order.
func (c *ProviderClient) PullRequests(_ context.Context, repo domain.Repository, cursor string) (ports.PullRequestPage, error) {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call(OpPullRequests, c.login, domain.Ref{}, repo); err != nil {
		return ports.PullRequestPage{}, err
	}
	since, _ := strconv.Atoi(cursor)
	page := ports.PullRequestPage{Cursor: strconv.Itoa(p.seq), ServerTime: p.clock.Now()}
	for _, e := range p.prs {
		if e.pr.Ref.Repo == repo && e.seq > since {
			page.PullRequests = append(page.PullRequests, p.visiblePR(e.pr))
		}
	}
	slices.SortFunc(page.PullRequests, func(a, b domain.PullRequest) int { return a.Ref.Number - b.Ref.Number })
	return page, nil
}

func (p *Provider) visiblePR(pr domain.PullRequest) domain.PullRequest {
	pr = clonePR(pr)
	pr.Comments = p.visible(pr.Comments)
	for i := range pr.Threads {
		pr.Threads[i].Comments = p.visible(pr.Threads[i].Comments)
	}
	return pr
}

// PullRequest returns one PR and the provider's time.
func (c *ProviderClient) PullRequest(_ context.Context, ref domain.Ref) (domain.PullRequest, time.Time, error) {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call(OpPullRequest, c.login, ref, ref.Repo); err != nil {
		return domain.PullRequest{}, time.Time{}, err
	}
	e, ok := p.prs[ref]
	if !ok {
		return domain.PullRequest{}, time.Time{}, fmt.Errorf("pull request %s: %w", ref.Full(), ports.ErrNotFound)
	}
	return p.visiblePR(e.pr), p.clock.Now(), nil
}

// OpenPullRequest opens a PR with the next free number, authored by the
// client's login.
func (c *ProviderClient) OpenPullRequest(_ context.Context, n ports.NewPullRequest) (domain.PullRequest, error) {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call(OpOpenPullRequest, c.login, domain.Ref{}, n.Repo); err != nil {
		return domain.PullRequest{}, err
	}
	p.prNumber++
	pr := domain.PullRequest{
		Ref:          domain.Ref{Repo: n.Repo, Kind: domain.KindPR, Number: p.prNumber},
		Title:        n.Title,
		Body:         n.Body,
		SourceBranch: n.Source,
		TargetBranch: n.Target,
		Author:       c.login,
		Draft:        n.Draft,
		Labels:       slices.Clone(n.Labels),
	}
	pr.URL = fmt.Sprintf("https://fake.test/%s/pull/%d", n.Repo, pr.Ref.Number)
	e := &prEntry{pr: pr}
	p.touchPR(e)
	p.prs[pr.Ref] = e
	return clonePR(e.pr), nil
}

// UpdatePullRequest changes the fields set in u.
func (c *ProviderClient) UpdatePullRequest(_ context.Context, ref domain.Ref, u ports.PullRequestUpdate) (domain.PullRequest, error) {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call(OpUpdatePR, c.login, ref, ref.Repo); err != nil {
		return domain.PullRequest{}, err
	}
	e, ok := p.prs[ref]
	if !ok {
		return domain.PullRequest{}, fmt.Errorf("pull request %s: %w", ref.Full(), ports.ErrNotFound)
	}
	if u.Title != nil {
		e.pr.Title = *u.Title
	}
	if u.Body != nil {
		e.pr.Body = *u.Body
	}
	if u.Draft != nil {
		e.pr.Draft = *u.Draft
	}
	p.touchPR(e)
	return clonePR(e.pr), nil
}

// AddReviewComment starts a new review thread holding the comment.
func (c *ProviderClient) AddReviewComment(_ context.Context, ref domain.Ref, rc ports.ReviewComment) (domain.Comment, error) {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call(OpAddReviewComment, c.login, ref, ref.Repo); err != nil {
		return domain.Comment{}, err
	}
	e, ok := p.prs[ref]
	if !ok {
		return domain.Comment{}, fmt.Errorf("pull request %s: %w", ref.Full(), ports.ErrNotFound)
	}
	comment := p.newComment(c.login, rc.Comment.String())
	e.pr.Threads = append(e.pr.Threads, domain.ReviewThread{
		ID: "thread-" + comment.ID, Path: rc.Path, Line: rc.StartLine, Comments: []domain.Comment{comment},
	})
	p.touchPR(e)
	return comment, nil
}

// ReplyToThread appends a comment to a review thread.
func (c *ProviderClient) ReplyToThread(_ context.Context, ref domain.Ref, threadID string, ac domain.AnthillComment) (domain.Comment, error) {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call(OpReplyToThread, c.login, ref, ref.Repo); err != nil {
		return domain.Comment{}, err
	}
	e, t, err := p.thread(ref, threadID)
	if err != nil {
		return domain.Comment{}, err
	}
	comment := p.newComment(c.login, ac.String())
	t.Comments = append(t.Comments, comment)
	p.touchPR(e)
	return comment, nil
}

// ResolveThread marks a review thread resolved.
func (c *ProviderClient) ResolveThread(_ context.Context, ref domain.Ref, threadID string) error {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call(OpResolveThread, c.login, ref, ref.Repo); err != nil {
		return err
	}
	e, t, err := p.thread(ref, threadID)
	if err != nil {
		return err
	}
	t.Resolved = true
	p.touchPR(e)
	return nil
}

func (p *Provider) thread(ref domain.Ref, id string) (*prEntry, *domain.ReviewThread, error) {
	e, ok := p.prs[ref]
	if !ok {
		return nil, nil, fmt.Errorf("pull request %s: %w", ref.Full(), ports.ErrNotFound)
	}
	for i := range e.pr.Threads {
		if e.pr.Threads[i].ID == id {
			return e, &e.pr.Threads[i], nil
		}
	}
	return nil, nil, fmt.Errorf("thread %s on %s: %w", id, ref.Full(), ports.ErrNotFound)
}
