package testkit

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// The SourceControl operations, for Fail.
const (
	GitOpenClone      = "open-clone"
	GitFetch          = "fetch"
	GitDefaultBranch  = "default-branch"
	GitBranch         = "branch"
	GitCreateBranch   = "create-branch"
	GitAddWorktree    = "add-worktree"
	GitWorktreeStatus = "worktree-status"
	GitRemoveWorktree = "remove-worktree"
	GitPush           = "push"
	GitNewCommits     = "new-commits"
	GitUnpushed       = "unpushed"
	GitDeleteBranch   = "delete-branch"
)

// FakeRemote is the remote name of every fake clone.
const FakeRemote = "origin"

// SourceControl is an in-memory ports.SourceControl. A clone has a remote
// with branches, the fetched copies of those branches, local branches and
// worktrees. A branch is a list of commits, the last one being its tip,
// and each commit has the owner its trailers name. Set up clones with
// AddClone and SetRemoteBranch, and commit in a worktree with Commit,
// which adds the worktree's trailers as the hook does.
type SourceControl struct {
	mu     sync.Mutex
	clones map[string]*fakeClone
	fail   map[string]error
	hashes int
}

type fakeCommit struct {
	commit ports.Commit
	owner  domain.BranchOwner
}

type fakeWorktree struct {
	branch   string
	trailers domain.Trailers
	dirty    bool
}

type fakeClone struct {
	repo          domain.Repository
	defaultBranch string
	remote        map[string][]fakeCommit
	fetched       map[string][]fakeCommit
	local         map[string][]fakeCommit
	worktrees     map[string]*fakeWorktree
}

var _ ports.SourceControl = (*SourceControl)(nil)

// NewSourceControl returns a source control with no clones.
func NewSourceControl() *SourceControl {
	return &SourceControl{clones: map[string]*fakeClone{}, fail: map[string]error{}}
}

// AddClone adds a clone of repo at path whose remote has one commit on
// its default branch, and returns it as OpenClone would.
func (s *SourceControl) AddClone(path string, repo domain.Repository, defaultBranch string) ports.Clone {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &fakeClone{
		repo:          repo,
		defaultBranch: defaultBranch,
		remote:        map[string][]fakeCommit{},
		fetched:       map[string][]fakeCommit{},
		local:         map[string][]fakeCommit{},
		worktrees:     map[string]*fakeWorktree{},
	}
	c.remote[defaultBranch] = []fakeCommit{s.newCommit("initial commit", domain.BranchOwner{})}
	s.clones[path] = c
	return ports.Clone{Path: path, Remote: FakeRemote}
}

// SetRemoteBranch makes the remote of clone have branch name with the
// given commit subjects on top of the default branch, each commit owned
// by owner. It returns the tip.
func (s *SourceControl) SetRemoteBranch(clone, name string, owner domain.BranchOwner, subjects ...string) ports.Commit {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.mustClone(clone)
	b := slices.Clone(c.remote[c.defaultBranch])
	for _, subject := range subjects {
		b = append(b, s.newCommit(subject, owner))
	}
	c.remote[name] = b
	return b[len(b)-1].commit
}

// DeleteRemoteBranch deletes a branch on the remote, as a provider does
// when a PR is merged.
func (s *SourceControl) DeleteRemoteBranch(clone, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.mustClone(clone).remote, name)
}

// Commit adds a commit to the branch of a worktree, with the worktree's
// trailers, and returns it.
func (s *SourceControl) Commit(worktree, subject string) ports.Commit {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, w := s.mustWorktree(worktree)
	commit := s.newCommit(subject, w.trailers.Owner())
	c.local[w.branch] = append(slices.Clone(c.local[w.branch]), commit)
	return commit.commit
}

// Amend replaces the tip of a worktree's branch with a new commit, and
// returns it.
func (s *SourceControl) Amend(worktree, subject string) ports.Commit {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, w := s.mustWorktree(worktree)
	b := slices.Clone(c.local[w.branch])
	b[len(b)-1] = s.newCommit(subject, w.trailers.Owner())
	c.local[w.branch] = b
	return b[len(b)-1].commit
}

// Checkout switches a worktree to another local branch, creating it from
// the fetched remote branch when needed, or detaches it when branch is
// empty.
func (s *SourceControl) Checkout(worktree, branch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, w := s.mustWorktree(worktree)
	if _, ok := c.local[branch]; !ok && branch != "" {
		c.local[branch] = slices.Clone(c.fetched[branch])
	}
	w.branch = branch
}

// SetDirty marks a worktree as having uncommitted changes or not.
func (s *SourceControl) SetDirty(worktree string, dirty bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, w := s.mustWorktree(worktree)
	w.dirty = dirty
}

// RemoteCommits returns the commits of a remote branch, for assertions.
func (s *SourceControl) RemoteCommits(clone, branch string) []ports.Commit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return commits(s.mustClone(clone).remote[branch])
}

// RemoteBranches and LocalBranches return the branch names of a clone,
// sorted.
func (s *SourceControl) RemoteBranches(clone string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedKeys(s.mustClone(clone).remote)
}

// LocalBranches returns the clone's local branch names, sorted.
func (s *SourceControl) LocalBranches(clone string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedKeys(s.mustClone(clone).local)
}

// Worktrees returns the worktree paths of a clone, sorted.
func (s *SourceControl) Worktrees(clone string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedKeys(s.mustClone(clone).worktrees)
}

// Trailers returns the trailers a worktree was set up with.
func (s *SourceControl) Trailers(worktree string) domain.Trailers {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, w := s.mustWorktree(worktree)
	return w.trailers
}

// Fail makes every call of op, one of the Git constants, fail with err
// until Fail(op, nil).
func (s *SourceControl) Fail(op string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.fail, op)
		return
	}
	s.fail[op] = err
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func commits(b []fakeCommit) []ports.Commit {
	out := make([]ports.Commit, len(b))
	for i, c := range b {
		out[i] = c.commit
	}
	return out
}

// isPrefix reports whether every commit of a is, in order, the start of b.
func isPrefix(a, b []fakeCommit) bool {
	return len(a) <= len(b) && slices.Equal(commits(a), commits(b[:len(a)]))
}

// after returns the commits of b that are not in base.
func after(b, base []fakeCommit) []fakeCommit {
	var out []fakeCommit
	for _, c := range b {
		if !slices.ContainsFunc(base, func(x fakeCommit) bool { return x.commit == c.commit }) {
			out = append(out, c)
		}
	}
	return out
}

func (s *SourceControl) newCommit(subject string, owner domain.BranchOwner) fakeCommit {
	s.hashes++
	return fakeCommit{commit: ports.Commit{Hash: "commit-" + strconv.Itoa(s.hashes), Subject: subject}, owner: owner}
}

func (s *SourceControl) mustClone(path string) *fakeClone {
	c, ok := s.clones[path]
	if !ok {
		panic(fmt.Sprintf("testkit.SourceControl: no clone %s", path))
	}
	return c
}

func (s *SourceControl) mustWorktree(path string) (*fakeClone, *fakeWorktree) {
	for _, c := range s.clones {
		if w, ok := c.worktrees[path]; ok {
			return c, w
		}
	}
	panic(fmt.Sprintf("testkit.SourceControl: no worktree %s", path))
}

func (s *SourceControl) clone(op string, c ports.Clone) (*fakeClone, error) {
	if err := s.fail[op]; err != nil {
		return nil, err
	}
	fc, ok := s.clones[c.Path]
	if !ok {
		return nil, fmt.Errorf("clone %s: %w", c.Path, ports.ErrNoClone)
	}
	return fc, nil
}

// OpenClone returns the clone added at path for repo.
func (s *SourceControl) OpenClone(_ context.Context, path string, repo domain.Repository) (ports.Clone, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail[GitOpenClone]; err != nil {
		return ports.Clone{}, err
	}
	c, ok := s.clones[path]
	if !ok || c.repo != repo {
		return ports.Clone{}, fmt.Errorf("%s: %w %s", path, ports.ErrNoClone, repo)
	}
	return ports.Clone{Path: path, Remote: FakeRemote}, nil
}

// Fetch copies the remote branches, dropping those the remote deleted.
func (s *SourceControl) Fetch(_ context.Context, clone ports.Clone) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitFetch, clone)
	if err != nil {
		return err
	}
	c.fetched = map[string][]fakeCommit{}
	for name, b := range c.remote {
		c.fetched[name] = slices.Clone(b)
	}
	return nil
}

// RemoteDefaultBranch returns the default branch given to AddClone.
func (s *SourceControl) RemoteDefaultBranch(_ context.Context, clone ports.Clone) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitDefaultBranch, clone)
	if err != nil {
		return "", err
	}
	return c.defaultBranch, nil
}

// Branch looks up a local and a fetched branch.
func (s *SourceControl) Branch(_ context.Context, clone ports.Clone, name, base string) (ports.BranchInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitBranch, clone)
	if err != nil {
		return ports.BranchInfo{}, err
	}
	local, isLocal := c.local[name]
	remote, isRemote := c.fetched[name]
	if !isLocal && !isRemote {
		return ports.BranchInfo{}, fmt.Errorf("branch %s: %w", name, ports.ErrNotFound)
	}
	info := ports.BranchInfo{Name: name, Local: isLocal, Remote: isRemote}
	tip := remote
	if isLocal {
		tip = local
	}
	info.Commit = tip[len(tip)-1].commit.Hash
	var baseCommits []fakeCommit
	if base != "" {
		baseCommits = c.fetched[base]
	}
	for _, cm := range after(append(slices.Clone(local), remote...), baseCommits) {
		if cm.owner.Item != "" && cm.owner.Instance != "" && !slices.Contains(info.Owners, cm.owner) {
			info.Owners = append(info.Owners, cm.owner)
		}
	}
	return info, nil
}

// CreateBranch creates a local branch from a fetched remote branch.
func (s *SourceControl) CreateBranch(_ context.Context, clone ports.Clone, name, from string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitCreateBranch, clone)
	if err != nil {
		return err
	}
	if _, ok := c.local[name]; ok {
		return fmt.Errorf("branch %s already exists", name)
	}
	base, ok := c.fetched[from]
	if !ok {
		return fmt.Errorf("remote branch %s: %w", from, ports.ErrNotFound)
	}
	c.local[name] = slices.Clone(base)
	return nil
}

// AddWorktree checks out a local branch at path with the trailers.
func (s *SourceControl) AddWorktree(_ context.Context, clone ports.Clone, path, branch string, trailers domain.Trailers) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitAddWorktree, clone)
	if err != nil {
		return err
	}
	if err := trailers.Validate(); err != nil {
		return err
	}
	if _, ok := c.local[branch]; !ok {
		return fmt.Errorf("branch %s: %w", branch, ports.ErrNotFound)
	}
	for _, other := range s.clones {
		if _, ok := other.worktrees[path]; ok {
			return fmt.Errorf("worktree %s already exists", path)
		}
	}
	c.worktrees[path] = &fakeWorktree{branch: branch, trailers: trailers}
	return nil
}

// WorktreeStatus returns the branch and dirtiness of a worktree.
func (s *SourceControl) WorktreeStatus(_ context.Context, clone ports.Clone, path string) (ports.WorktreeStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitWorktreeStatus, clone)
	if err != nil {
		return ports.WorktreeStatus{}, err
	}
	w, ok := c.worktrees[path]
	if !ok {
		return ports.WorktreeStatus{}, fmt.Errorf("worktree %s: %w", path, ports.ErrNotFound)
	}
	return ports.WorktreeStatus{Branch: w.branch, Dirty: w.dirty}, nil
}

// RemoveWorktree removes a worktree; its branch stays. A dirty worktree
// needs force.
func (s *SourceControl) RemoveWorktree(_ context.Context, clone ports.Clone, path string, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitRemoveWorktree, clone)
	if err != nil {
		return err
	}
	w, ok := c.worktrees[path]
	if !ok {
		return fmt.Errorf("worktree %s: %w", path, ports.ErrNotFound)
	}
	if w.dirty && !force {
		return fmt.Errorf("worktree %s has uncommitted changes", path)
	}
	delete(c.worktrees, path)
	return nil
}

// Push makes the remote branch equal to the local one. It never forces:
// when the remote has commits the branch lacks, it fetches the remote
// branch and fails with ports.ErrHistoryRewritten when one of them is
// owner's, or ports.ErrConflict otherwise.
func (s *SourceControl) Push(_ context.Context, clone ports.Clone, branch string, owner domain.BranchOwner) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitPush, clone)
	if err != nil {
		return "", err
	}
	local, ok := c.local[branch]
	if !ok {
		return "", fmt.Errorf("branch %s: %w", branch, ports.ErrNotFound)
	}
	if remote, ok := c.remote[branch]; ok && !isPrefix(remote, local) {
		out := "! [rejected] " + branch + " -> " + branch + " (non-fast-forward)"
		c.fetched[branch] = slices.Clone(remote)
		for _, cm := range after(remote, local) {
			if cm.owner == owner {
				return out, fmt.Errorf("push %s: %w", branch, ports.ErrHistoryRewritten)
			}
		}
		return out, fmt.Errorf("push %s: %w", branch, ports.ErrConflict)
	}
	c.remote[branch] = slices.Clone(local)
	c.fetched[branch] = slices.Clone(local)
	return branch + " -> " + branch, nil
}

// NewCommits lists the commits of the worktree's branch that are not on
// the fetched branch base.
func (s *SourceControl) NewCommits(_ context.Context, clone ports.Clone, worktree, base string) ([]ports.Commit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitNewCommits, clone)
	if err != nil {
		return nil, err
	}
	w, ok := c.worktrees[worktree]
	if !ok {
		return nil, fmt.Errorf("worktree %s: %w", worktree, ports.ErrNotFound)
	}
	b, ok := c.fetched[base]
	if !ok {
		return nil, fmt.Errorf("remote branch %s: %w", base, ports.ErrNotFound)
	}
	return commits(after(c.local[w.branch], b)), nil
}

// Unpushed lists the commits of a local branch on no fetched branch,
// and after pushed when the branch has that commit.
func (s *SourceControl) Unpushed(_ context.Context, clone ports.Clone, branch, pushed string) ([]ports.Commit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitUnpushed, clone)
	if err != nil {
		return nil, err
	}
	local, ok := c.local[branch]
	if !ok {
		return nil, fmt.Errorf("branch %s: %w", branch, ports.ErrNotFound)
	}
	var all []fakeCommit
	for _, b := range c.fetched {
		all = append(all, b...)
	}
	if i := slices.IndexFunc(local, func(c fakeCommit) bool { return c.commit.Hash == pushed }); i >= 0 {
		all = append(all, local[:i+1]...)
	}
	return commits(after(local, all)), nil
}

// DeleteBranch deletes a local or remote branch.
func (s *SourceControl) DeleteBranch(_ context.Context, clone ports.Clone, name string, remote bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitDeleteBranch, clone)
	if err != nil {
		return err
	}
	branches := c.local
	if remote {
		branches = c.remote
	}
	if _, ok := branches[name]; !ok {
		return fmt.Errorf("branch %s: %w", name, ports.ErrNotFound)
	}
	delete(branches, name)
	if remote {
		delete(c.fetched, name)
	}
	return nil
}
