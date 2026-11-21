package domain

// Ended reports whether the PR was merged or closed: the shepherd then
// stops all activity on it, and its ant becomes done (spec pr-shepherd,
// "PR closure is human").
func (s PRState) Ended() bool {
	return s == PRMerged || s == PRClosed
}

// String names the state, as reasons and logs show it.
func (s PRState) String() string {
	switch s {
	case PROpen:
		return "open"
	case PRMerged:
		return "merged"
	case PRClosed:
		return "closed"
	}
	return "none"
}

// FixPushable reports whether Anthill can push an auto-fix to the PR's
// branch: a branch of the PR's own repository, or of a fork that lets the
// repository's maintainers push to it and still exists (spec pr-shepherd,
// "Fork branch not pushable").
func FixPushable(pr PullRequest) bool {
	if !pr.Fork {
		return true
	}
	return pr.MaintainerCanModify && pr.ForkURL != "" && pr.ForkRepo.Org != ""
}
