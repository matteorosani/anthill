package domain

import "fmt"

// The labels that opt a human PR in to the shepherd (spec pr-shepherd).
// They are fixed, unlike the opt-in, hold and claim labels.
const (
	// LabelReview puts a PR in the labelled shepherd scope.
	LabelReview = "anthill:review"
	// LabelAutofix opts a human PR in to auto-fix.
	LabelAutofix = "anthill:autofix"
)

// IsAntPR reports whether pr was opened by an ant: its source branch is an
// ant branch under the repository's branch prefix, as session index
// recovery reads it (BranchItem).
func IsAntPR(pr PullRequest, branchPrefix string) bool {
	_, ok := BranchItem(branchPrefix, pr.SourceBranch, pr.Ref.Repo)
	return ok
}

// InScope reports whether the shepherd reviews pr on its own under the
// repository's shepherd scope (spec pr-shepherd, "Shepherd scope"): ant
// PRs for ant-authored; ant PRs and human PRs with the review label for
// labelled; every PR for all. Whether it is a draft is left to the
// playbook. A PR outside the scope is reviewed only when a developer
// requests it.
func InScope(scope ShepherdScope, pr PullRequest, antPR bool) bool {
	switch scope {
	case ScopeAntAuthored:
		return antPR
	case ScopeLabelled:
		return antPR || HasLabel(pr.Labels, LabelReview)
	case ScopeAll:
		return true
	}
	return false
}

// RoundLimitText is the body of the comment that a PR's automatic rounds
// are used up (spec pr-shepherd, "Round limit").
func RoundLimitText(limit int) string {
	if limit == 0 {
		return "This repository has no automatic review rounds. Comment `/anthill review` to request one."
	}
	return fmt.Sprintf("This PR has had its %d automatic review rounds, so new commits start no round. "+
		"Comment `/anthill review` to request another one.", limit)
}
