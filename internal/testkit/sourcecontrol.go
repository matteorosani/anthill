package testkit

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/matteorosani/anthill/internal/ports"
)

// The SourceControl operations, for Fail.
const (
	GitFetch          = "fetch"
	GitDefaultBranch  = "default-branch"
	GitBranch         = "branch"
	GitCreateBranch   = "create-branch"
	GitAddWorktree    = "add-worktree"
	GitRemoveWorktree = "remove-worktree"
	GitPush           = "push"
	GitNewCommits     = "new-commits"
	GitDeleteBranch   = "delete-branch"
)

// SourceControl is an in-memory ports.SourceControl. A clone has a remote
// with branches, local branches and worktrees; a branch is a list of
// commits, the last one being its tip. Fetch copies the remote's branches
// to origin/<name>. Set up clones with AddClone and SetRemoteBranch, and
// commit in a worktree with Commit.
type SourceControl struct {
	mu     sync.Mutex
	clones map[string]*fakeClone
	fail   map[string]error
	hashes int
}

type fakeBranch struct {
	commits []ports.Commit
	// instance is the Anthill-Instance trailer of the tip.
	instance string
}

type fakeClone struct {
	defaultBranch string
	remote        map[string]fakeBranch
	local         map[string]fakeBranch
	// worktrees maps a worktree path to its branch.
	worktrees map[string]string
}

var _ ports.SourceControl = (*SourceControl)(nil)

// NewSourceControl returns a source control with no clones.
func NewSourceControl() *SourceControl {
	return &SourceControl{clones: map[string]*fakeClone{}, fail: map[string]error{}}
}

// AddClone adds a clone at path whose remote has one commit on its
// default branch.
func (s *SourceControl) AddClone(path, defaultBranch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &fakeClone{
		defaultBranch: defaultBranch,
		remote:        map[string]fakeBranch{},
		local:         map[string]fakeBranch{},
		worktrees:     map[string]string{},
	}
	c.remote[defaultBranch] = fakeBranch{commits: []ports.Commit{s.newCommit("initial commit")}}
	s.clones[path] = c
}

// SetRemoteBranch makes the remote of clone have branch name with the
// given commit subjects on top of the default branch, the tip carrying
// the instance trailer. It returns the tip.
func (s *SourceControl) SetRemoteBranch(clone, name, instance string, subjects ...string) ports.Commit {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.mustClone(clone)
	b := fakeBranch{commits: slices.Clone(c.remote[c.defaultBranch].commits), instance: instance}
	for _, subject := range subjects {
		b.commits = append(b.commits, s.newCommit(subject))
	}
	c.remote[name] = b
	return b.commits[len(b.commits)-1]
}

// Commit adds a commit to the branch of a worktree, with the instance
// trailer, and returns it.
func (s *SourceControl) Commit(worktree, subject, instance string) ports.Commit {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, branch, ok := s.worktree(worktree)
	if !ok {
		panic(fmt.Sprintf("testkit.SourceControl: no worktree %s", worktree))
	}
	b := c.local[branch]
	commit := s.newCommit(subject)
	b.commits = append(slices.Clone(b.commits), commit)
	b.instance = instance
	c.local[branch] = b
	return commit
}

// RemoteCommits returns the commits of a remote branch, for assertions.
func (s *SourceControl) RemoteCommits(clone, branch string) []ports.Commit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.mustClone(clone).remote[branch].commits)
}

// Worktrees returns the worktree paths of a clone, sorted.
func (s *SourceControl) Worktrees(clone string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var paths []string
	for p := range s.mustClone(clone).worktrees {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return paths
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

func (s *SourceControl) newCommit(subject string) ports.Commit {
	s.hashes++
	return ports.Commit{Hash: "commit-" + strconv.Itoa(s.hashes), Subject: subject}
}

func (s *SourceControl) mustClone(path string) *fakeClone {
	c, ok := s.clones[path]
	if !ok {
		panic(fmt.Sprintf("testkit.SourceControl: no clone %s", path))
	}
	return c
}

func (s *SourceControl) clone(op, path string) (*fakeClone, error) {
	if err := s.fail[op]; err != nil {
		return nil, err
	}
	c, ok := s.clones[path]
	if !ok {
		return nil, fmt.Errorf("clone %s: %w", path, ports.ErrNotFound)
	}
	return c, nil
}

func (s *SourceControl) worktree(path string) (*fakeClone, string, bool) {
	for _, c := range s.clones {
		if branch, ok := c.worktrees[path]; ok {
			return c, branch, true
		}
	}
	return nil, "", false
}

// Fetch copies the remote branches to origin/<name>.
func (s *SourceControl) Fetch(_ context.Context, clone string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitFetch, clone)
	if err != nil {
		return err
	}
	for name, b := range c.remote {
		c.local["origin/"+name] = b
	}
	return nil
}

// RemoteDefaultBranch returns the default branch given to AddClone.
func (s *SourceControl) RemoteDefaultBranch(_ context.Context, clone string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitDefaultBranch, clone)
	if err != nil {
		return "", err
	}
	return c.defaultBranch, nil
}

// Branch looks up a local branch, then a remote one.
func (s *SourceControl) Branch(_ context.Context, clone, name string) (ports.BranchInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitBranch, clone)
	if err != nil {
		return ports.BranchInfo{}, err
	}
	b, ok := c.local[name]
	remote := false
	if !ok {
		b, ok = c.remote[name]
		remote = true
	}
	if !ok {
		return ports.BranchInfo{}, fmt.Errorf("branch %s: %w", name, ports.ErrNotFound)
	}
	return ports.BranchInfo{Name: name, Commit: b.commits[len(b.commits)-1].Hash, Instance: b.instance, Remote: remote}, nil
}

// CreateBranch creates a local branch from a local or fetched branch.
func (s *SourceControl) CreateBranch(_ context.Context, clone, name, from string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitCreateBranch, clone)
	if err != nil {
		return err
	}
	if _, ok := c.local[name]; ok {
		return fmt.Errorf("branch %s already exists", name)
	}
	base, ok := c.local[from]
	if !ok {
		return fmt.Errorf("base %s: %w", from, ports.ErrNotFound)
	}
	c.local[name] = fakeBranch{commits: slices.Clone(base.commits)}
	return nil
}

// AddWorktree checks out a local branch at path.
func (s *SourceControl) AddWorktree(_ context.Context, clone, path, branch string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitAddWorktree, clone)
	if err != nil {
		return err
	}
	if _, ok := c.local[branch]; !ok {
		return fmt.Errorf("branch %s: %w", branch, ports.ErrNotFound)
	}
	if _, _, ok := s.worktree(path); ok {
		return fmt.Errorf("worktree %s already exists", path)
	}
	c.worktrees[path] = branch
	return nil
}

// RemoveWorktree removes a worktree; its branch stays.
func (s *SourceControl) RemoveWorktree(_ context.Context, clone, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitRemoveWorktree, clone)
	if err != nil {
		return err
	}
	if _, ok := c.worktrees[path]; !ok {
		return fmt.Errorf("worktree %s: %w", path, ports.ErrNotFound)
	}
	delete(c.worktrees, path)
	return nil
}

// Push makes the remote branch equal to the worktree's branch. It fails
// with ports.ErrConflict when the remote has commits the worktree lacks,
// since it never forces.
func (s *SourceControl) Push(_ context.Context, worktree string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail[GitPush]; err != nil {
		return "", err
	}
	c, branch, ok := s.worktree(worktree)
	if !ok {
		return "", fmt.Errorf("worktree %s: %w", worktree, ports.ErrNotFound)
	}
	local := c.local[branch]
	if remote, ok := c.remote[branch]; ok {
		n := len(remote.commits)
		if n > len(local.commits) || !slices.Equal(remote.commits, local.commits[:n]) {
			return "! [rejected] " + branch + " (fetch first)", fmt.Errorf("push %s: %w", branch, ports.ErrConflict)
		}
	}
	c.remote[branch] = fakeBranch{commits: slices.Clone(local.commits), instance: local.instance}
	c.local["origin/"+branch] = c.remote[branch]
	return branch + " -> " + branch, nil
}

// NewCommits lists the commits of the worktree's branch after since, a
// commit hash or a branch name such as origin/<name>.
func (s *SourceControl) NewCommits(_ context.Context, worktree, since string) ([]ports.Commit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail[GitNewCommits]; err != nil {
		return nil, err
	}
	c, branch, ok := s.worktree(worktree)
	if !ok {
		return nil, fmt.Errorf("worktree %s: %w", worktree, ports.ErrNotFound)
	}
	if b, ok := c.local[since]; ok {
		since = b.commits[len(b.commits)-1].Hash
	}
	commits := c.local[branch].commits
	i := slices.IndexFunc(commits, func(cm ports.Commit) bool { return cm.Hash == since })
	if i < 0 {
		return nil, fmt.Errorf("commit %s: %w", since, ports.ErrNotFound)
	}
	return slices.Clone(commits[i+1:]), nil
}

// DeleteBranch deletes a local or remote branch.
func (s *SourceControl) DeleteBranch(_ context.Context, clone, name string, remote bool) error {
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
	return nil
}
