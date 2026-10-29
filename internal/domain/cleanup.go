package domain

// PRState is the state of an ant's PR, as cleanup needs it.
type PRState int

// The PR states.
const (
	PRNone PRState = iota
	PROpen
	PRMerged
	PRClosed
)

// CleanupInput is what Anthill knows about an ant when deciding whether
// to clean it up.
type CleanupInput struct {
	PR PRState
	// Abandoned is set for an abandoned ant, and Requested when the queen
	// asked to clean it up.
	Abandoned bool
	Requested bool
	// Confirmed is set when the queen confirmed deleting unpushed work.
	Confirmed bool
	// Worktree is set when the worktree still exists.
	Worktree bool
	// RemoteBranch is set when the provider still has the ant branch
	// after a fetch that prunes deleted branches.
	RemoteBranch bool
	// UnpushedCommits counts the local branch's commits that are on no
	// remote branch, and Uncommitted is set when the worktree has
	// uncommitted changes.
	UnpushedCommits int
	Uncommitted     bool
}

// CleanupPlan is what cleanup does. When NeedsConfirmation is set,
// nothing else is set: Anthill asks the queen first.
type CleanupPlan struct {
	RemoveWorktree bool
	// Force removes a worktree that has uncommitted changes.
	Force             bool
	DeleteLocalBranch bool
	// DeleteRemoteBranch is set only for a merged PR whose branch the
	// provider kept.
	DeleteRemoteBranch bool
	NeedsConfirmation  bool
}

// Due reports whether the ant may be cleaned up at all: its PR is merged
// or closed, or it is abandoned and the queen asked for cleanup (spec
// branching-policy, "Worktree cleanup").
func (in CleanupInput) Due() bool {
	return in.PR == PRMerged || in.PR == PRClosed || in.Abandoned && in.Requested
}

// PlanCleanup decides what to remove. Unpushed commits and uncommitted
// changes need the queen's confirmation first. The remote branch is
// deleted only when the PR is merged and the provider has not deleted it
// already. A cleanup that is not due plans nothing.
func PlanCleanup(in CleanupInput) CleanupPlan {
	if !in.Due() {
		return CleanupPlan{}
	}
	unsaved := in.UnpushedCommits > 0 || in.Worktree && in.Uncommitted
	if unsaved && !in.Confirmed {
		return CleanupPlan{NeedsConfirmation: true}
	}
	return CleanupPlan{
		RemoveWorktree:     in.Worktree,
		Force:              in.Worktree && in.Uncommitted,
		DeleteLocalBranch:  true,
		DeleteRemoteBranch: in.PR == PRMerged && in.RemoteBranch,
	}
}
