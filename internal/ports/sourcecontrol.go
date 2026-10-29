package ports

import (
	"context"

	"github.com/matteorosani/anthill/internal/domain"
)

// Commit is one commit, as Anthill needs it.
type Commit struct {
	Hash    string
	Subject string
}

// Clone is a local clone of a configured repository, as OpenClone found
// it.
type Clone struct {
	// Path is the clone's working folder.
	Path string
	// Remote is the name of the git remote that points to the configured
	// repository, such as origin. Fetches and pushes use it.
	Remote string
}

// BranchInfo describes an existing branch.
type BranchInfo struct {
	Name string
	// Commit is the branch tip: the local one when there is a local
	// branch, otherwise the fetched remote one.
	Commit string
	// Owners are the Anthill-Item and Anthill-Instance trailer pairs of the
	// branch's commits that are not on the base branch, so that a foreign
	// branch with the same name is detected (spec branching-policy,
	// "Branch naming").
	Owners []domain.BranchOwner
	// Local and Remote say where the branch exists. Remote means a fetched
	// remote-tracking branch.
	Local  bool
	Remote bool
}

// WorktreeStatus is the state of an ant's worktree.
type WorktreeStatus struct {
	// Branch is the checked-out branch, empty when HEAD is detached.
	Branch string
	// Dirty is set when the worktree has uncommitted changes, untracked
	// files included.
	Dirty bool
}

// SourceControl runs git for Anthill in a local clone and the worktrees
// made from it (design D1). Branch arguments are plain branch names, such
// as main or ant/gh-12-fix; where a call reads the remote's side, it uses
// the clone's fetched remote-tracking branches.
type SourceControl interface {
	// OpenClone checks that path holds a git clone with a remote that
	// points to repo, and returns it. Otherwise it returns ErrNoClone
	// (spec anthill-configuration, "Repository listed without a clone").
	OpenClone(ctx context.Context, path string, repo domain.Repository) (Clone, error)
	// Fetch updates the clone's remote-tracking branches and drops those
	// whose branch was deleted on the remote (spec branching-policy, "Base
	// branch resolution" and "Worktree cleanup"). It never changes the
	// clone's own branches or working copy.
	Fetch(ctx context.Context, c Clone) error
	// RemoteDefaultBranch is the remote's default branch, the last
	// fallback for the base branch.
	RemoteDefaultBranch(ctx context.Context, c Clone) (string, error)
	// Branch looks up a local or fetched remote branch, reading the owners
	// from its commits after base (none when base is empty). It returns
	// ErrNotFound when there is no such branch.
	Branch(ctx context.Context, c Clone, name, base string) (BranchInfo, error)
	// CreateBranch creates the local branch name at the fetched remote
	// branch from, without changing any working copy. It returns
	// ErrNotFound when from is not on the remote.
	CreateBranch(ctx context.Context, c Clone, name, from string) error
	// AddWorktree checks out a local branch in a new worktree at path, and
	// sets it up for an agent: the prepare-commit-msg hook that adds
	// trailers, hooks that call the repository's own hooks, a pre-push
	// hook that refuses, and no push credentials (spec branching-policy,
	// "Worktree location" and "Commit identity and traceability"; design
	// D4).
	AddWorktree(ctx context.Context, c Clone, path, branch string, trailers domain.Trailers) error
	// WorktreeStatus returns the state of a worktree of the clone, or
	// ErrNotFound when there is none at path.
	WorktreeStatus(ctx context.Context, c Clone, path string) (WorktreeStatus, error)
	// RemoveWorktree removes a worktree; force also removes uncommitted
	// changes. The branch stays.
	RemoveWorktree(ctx context.Context, c Clone, path string, force bool) error
	// Push pushes the local branch to the same branch on the remote, never
	// with force, and returns git's output. When the remote rejects it,
	// Push fetches the remote branch, so that the ant can merge it. The
	// rejection is ErrHistoryRewritten when the remote branch has commits
	// of owner, the pusher's item and instance, that the local branch
	// lacks, which means owner rewrote what it had already pushed (spec
	// branching-policy, "History rewriting"). Otherwise someone else
	// pushed, and it is ErrConflict (spec ant-tools, "Push tool").
	Push(ctx context.Context, c Clone, branch string, owner domain.BranchOwner) (string, error)
	// NewCommits lists, oldest first, the commits of the worktree's HEAD
	// that are not on the fetched remote branch base (spec ant-tools,
	// "Finish tool").
	NewCommits(ctx context.Context, c Clone, worktree, base string) ([]Commit, error)
	// Unpushed lists, oldest first, the commits of a local branch that are
	// on no fetched remote branch and are not pushed, the PR's last head
	// commit, or before it (spec branching-policy, "Unpushed work").
	// pushed may be empty. It lets a merged PR whose branch the provider
	// deleted, after a squash or rebase merge, count as pushed.
	Unpushed(ctx context.Context, c Clone, branch, pushed string) ([]Commit, error)
	// DeleteBranch deletes a local branch, even when it is not merged, or
	// the remote one when remote is set. It returns ErrNotFound when the
	// branch does not exist.
	DeleteBranch(ctx context.Context, c Clone, name string, remote bool) error
}
