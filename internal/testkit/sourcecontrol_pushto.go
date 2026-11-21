package testkit

import (
	"context"
	"fmt"
	"slices"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// SetForkBranch makes the fork at url have branch name, with the given
// commit subjects on top of the default branch of clone, and returns its
// tip. The clone's remote gets the same commits as the PR's head, as
// GitHub offers it from the base repository.
func (s *SourceControl) SetForkBranch(clone, url, name string, subjects ...string) ports.Commit {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.mustClone(clone)
	b := slices.Clone(c.remote[c.defaultBranch])
	for _, subject := range subjects {
		b = append(b, s.newCommit(subject, domain.BranchOwner{}))
	}
	if s.forks == nil {
		s.forks = map[string]map[string][]fakeCommit{}
	}
	if s.forks[url] == nil {
		s.forks[url] = map[string][]fakeCommit{}
	}
	s.forks[url][name] = b
	return b[len(b)-1].commit
}

// ForkCommits returns the commits of a fork's branch, for assertions.
func (s *SourceControl) ForkCommits(url, branch string) []ports.Commit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return commits(s.forks[url][branch])
}

// PushTo makes the target branch equal to the commits of the worktree's
// HEAD. It never forces: a branch that is not at target.Expect, or that
// has commits the worktree lacks, fails with ports.ErrConflict. A fork's
// URL must point to its repository.
func (s *SourceControl) PushTo(_ context.Context, clone ports.Clone, worktree string, target ports.PushTarget) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitPushTo, clone)
	if err != nil {
		return "", err
	}
	w, ok := c.worktrees[worktree]
	if !ok {
		return "", fmt.Errorf("worktree %s: %w", worktree, ports.ErrNotFound)
	}
	head := s.head(c, w)
	branches := c.remote
	if target.URL != "" {
		if !target.Repo.MatchesRemoteURL(target.URL) {
			return "", fmt.Errorf("push to %s: the URL %q does not point to %s", target.Branch, target.URL, target.Repo)
		}
		branches = s.forks[target.URL]
		if branches == nil {
			return "", fmt.Errorf("fork %s: %w", target.URL, ports.ErrNotFound)
		}
	}
	remote, exists := branches[target.Branch]
	if target.Expect != "" && (!exists || remote[len(remote)-1].commit.Hash != target.Expect) {
		return "", fmt.Errorf("push %s: the branch moved: %w", target.Branch, ports.ErrConflict)
	}
	if exists && !isPrefix(remote, head) {
		return "! [rejected] (non-fast-forward)", fmt.Errorf("push %s: %w", target.Branch, ports.ErrConflict)
	}
	branches[target.Branch] = slices.Clone(head)
	if target.URL == "" {
		c.fetched[target.Branch] = slices.Clone(head)
	}
	return "HEAD -> " + target.Branch, nil
}

// head returns the commits of a worktree's HEAD.
func (s *SourceControl) head(c *fakeClone, w *fakeWorktree) []fakeCommit {
	if w.branch == "" && w.commit != "" {
		return w.history
	}
	return c.local[w.branch]
}

// historyOf returns the commits up to commit on a branch of the clone's
// remote, its fetched branches or a fork, or the commit alone when none
// has it.
func (s *SourceControl) historyOf(c *fakeClone, commit string) []fakeCommit {
	find := func(branches map[string][]fakeCommit) []fakeCommit {
		for _, name := range sortedKeys(branches) {
			b := branches[name]
			if i := slices.IndexFunc(b, func(fc fakeCommit) bool { return fc.commit.Hash == commit }); i >= 0 {
				return slices.Clone(b[:i+1])
			}
		}
		return nil
	}
	for _, branches := range []map[string][]fakeCommit{c.remote, c.fetched} {
		if h := find(branches); h != nil {
			return h
		}
	}
	for _, url := range sortedKeys(s.forks) {
		if h := find(s.forks[url]); h != nil {
			return h
		}
	}
	return []fakeCommit{{commit: ports.Commit{Hash: commit}}}
}

// SetTrailers sets the trailers of a worktree's later commits.
func (s *SourceControl) SetTrailers(_ context.Context, clone ports.Clone, path string, trailers domain.Trailers) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.clone(GitSetTrailers, clone)
	if err != nil {
		return err
	}
	if err := trailers.Validate(); err != nil {
		return err
	}
	w, ok := c.worktrees[path]
	if !ok {
		return fmt.Errorf("worktree %s: %w", path, ports.ErrNotFound)
	}
	w.trailers = trailers
	return nil
}
