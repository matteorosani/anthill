package domain

import (
	"errors"
	"fmt"
	"slices"
)

// MaxBranchSuffix is the highest numeric suffix ChooseBranchName tries.
const MaxBranchSuffix = 100

// BranchCandidate is what is known about one possible branch name.
type BranchCandidate struct {
	// Exists is set when a local or remote branch has the name.
	Exists bool
	// Owners are the trailer pairs on the branch's own commits, the ones
	// not on the base branch.
	Owners []BranchOwner
}

// ChooseBranchName picks the ant branch: the first of name, name-2,
// name-3, … that is free or already belongs to owner, that is, carries
// owner's item and instance trailers on one of its commits. A name taken
// by anyone else is left untouched (spec branching-policy, "Name already
// taken remotely by someone else"). reuse is true when the chosen branch
// exists and belongs to owner. lookup reports on one name.
func ChooseBranchName(name string, owner BranchOwner, lookup func(name string) (BranchCandidate, error)) (chosen string, reuse bool, err error) {
	for n := 1; n <= MaxBranchSuffix; n++ {
		candidate := WithSuffix(name, n)
		c, err := lookup(candidate)
		if err != nil {
			return "", false, fmt.Errorf("looking up branch %s: %w", candidate, err)
		}
		if !c.Exists {
			return candidate, false, nil
		}
		if slices.Contains(c.Owners, owner) {
			return candidate, true, nil
		}
	}
	return "", false, fmt.Errorf("branch names %s to %s are all taken", name, WithSuffix(name, MaxBranchSuffix))
}

// ErrBaseBranchMissing is returned, wrapped in a *BaseBranchMissingError,
// when the resolved base branch is not on the remote.
var ErrBaseBranchMissing = errors.New("base branch not found")

// BaseBranchMissingError names the base branch that is not on the remote
// (spec branching-policy, "Base branch missing").
type BaseBranchMissingError struct {
	Branch string
}

func (e *BaseBranchMissingError) Error() string {
	return fmt.Sprintf("base branch %s not found", e.Branch)
}

// Is makes errors.Is(err, ErrBaseBranchMissing) true.
func (e *BaseBranchMissingError) Is(target error) bool {
	return target == ErrBaseBranchMissing
}

// ResolveBase returns the base branch: configured, which is the effective
// base-branch setting after the item's base: label (Effective.ForItem),
// or the remote's default branch when it is empty (spec branching-policy,
// "Base branch resolution").
func ResolveBase(configured, remoteDefault string) string {
	if configured != "" {
		return configured
	}
	return remoteDefault
}
