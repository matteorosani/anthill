package ports

import "context"

// Commit is one commit, as Anthill needs it.
type Commit struct {
	Hash    string
	Subject string
}

// BranchInfo describes an existing branch.
type BranchInfo struct {
	Name string
	// Commit is the branch tip.
	Commit string
	// Instance is the Anthill-Instance trailer of the tip, empty when it
	// has none (spec branching-policy, "Branch naming").
	Instance string
	// Remote is set when the branch exists on the remote.
	Remote bool
}

// SourceControl runs git for Anthill in a local clone and the worktrees
// made from it (design D1). The adapter for task group 6 may refine these
// calls.
type SourceControl interface {
	// Fetch updates the clone's remote branches (spec branching-policy,
	// "Base branch resolution").
	Fetch(ctx context.Context, clone string) error
	// RemoteDefaultBranch is the remote's default branch, the last
	// fallback for the base branch.
	RemoteDefaultBranch(ctx context.Context, clone string) (string, error)
	// Branch looks up a local or remote branch, so that a foreign branch
	// with the same name is detected. It returns ErrNotFound when there is
	// none.
	Branch(ctx context.Context, clone, name string) (BranchInfo, error)
	// CreateBranch creates a local branch from a fetched base.
	CreateBranch(ctx context.Context, clone, name, from string) error
	// AddWorktree and RemoveWorktree manage an ant's worktree without
	// touching the developer's working copy (spec branching-policy,
	// "Worktree location" and "Worktree cleanup").
	AddWorktree(ctx context.Context, clone, path, branch string) error
	RemoveWorktree(ctx context.Context, clone, path string) error
	// Push pushes the worktree's current branch, never with force, and
	// returns git's output. A rejection because the remote moved is
	// ErrConflict (spec ant-tools, "Push tool").
	Push(ctx context.Context, worktree string) (string, error)
	// NewCommits lists the commits in the worktree after since, a commit
	// or ref, such as the base for finish checks or the upstream for
	// unpushed work (spec ant-tools, "Finish tool").
	NewCommits(ctx context.Context, worktree, since string) ([]Commit, error)
	// DeleteBranch deletes a local branch, or the remote one when remote
	// is set (spec branching-policy, "Worktree cleanup").
	DeleteBranch(ctx context.Context, clone, name string, remote bool) error
}
