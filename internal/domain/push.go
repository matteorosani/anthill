package domain

import (
	"errors"
	"fmt"
	"path"
)

// PushKind tells who asks for a push.
type PushKind int

// The kinds of push Anthill executes (spec branching-policy, "Push
// restrictions").
const (
	// PushAnt is an ant's push tool call: only its own branch.
	PushAnt PushKind = iota + 1
	// PushAutoFix is the shepherd pushing a verified fix pass to a human
	// PR's branch.
	PushAutoFix
)

// PushRequest is one push to check against the branching policy.
type PushRequest struct {
	Kind PushKind
	// Branch is the branch that would be pushed: the worktree's current
	// branch, empty when HEAD is detached.
	Branch string
	// Allowed is the one branch this push may target: the ant branch in
	// the ant's record, or the head branch of the human PR.
	Allowed string
	// OptedIn is set when the human PR opted in to auto-fix.
	OptedIn bool
	// Base and Default are the base branch and the remote's default
	// branch. Both are always protected.
	Base    string
	Default string
	// Protected holds the protected branch patterns: the configured
	// protected-branches and those the provider reports. A pattern uses
	// path.Match syntax, such as release/*.
	Protected []string
	// Force asks for a force-push, which is never allowed.
	Force bool
}

// ErrPushRefused is returned, wrapped in a *PushRefusedError, for every
// push the branching policy refuses.
var ErrPushRefused = errors.New("push refused")

// PushRefusedError says why a push was refused, in words the agent can
// act on.
type PushRefusedError struct {
	Reason string
}

func (e *PushRefusedError) Error() string { return e.Reason }

// Is makes errors.Is(err, ErrPushRefused) true.
func (e *PushRefusedError) Is(target error) bool { return target == ErrPushRefused }

// MessageForcePush is the refusal of a force-push.
const MessageForcePush = "force-push is not allowed: keep the published history and add a new commit"

// MessageHistoryRewritten is what the ant is told when a push is
// rejected because it rewrote history that was already pushed (spec
// branching-policy, "Amend after the first push").
const MessageHistoryRewritten = "the push was rejected as non-fast-forward: the branch rewrites commits that were already pushed. Keep the published history and add a new commit"

// CheckPush applies the branching policy to a push. It refuses force
// pushes, a detached HEAD, the base branch, the default branch and every
// protected branch, then any branch other than the one the request
// allows. A refusal is a *PushRefusedError.
func CheckPush(r PushRequest) error {
	refuse := func(format string, args ...any) error {
		return &PushRefusedError{Reason: fmt.Sprintf(format, args...)}
	}
	switch {
	case r.Force:
		return refuse("%s", MessageForcePush)
	case r.Branch == "":
		return refuse("push needs a checked-out branch, and HEAD is detached")
	case r.Branch == r.Base || r.Branch == r.Default || IsProtected(r.Branch, r.Protected):
		return refuse("push to %s is not allowed", r.Branch)
	}
	switch r.Kind {
	case PushAnt:
		if r.Branch != r.Allowed {
			return refuse("push to %s is not allowed: an ant pushes only its own branch %s", r.Branch, r.Allowed)
		}
	case PushAutoFix:
		if !r.OptedIn {
			return refuse("push to %s is not allowed: the PR has not opted in to auto-fix", r.Branch)
		}
		if r.Branch != r.Allowed {
			return refuse("push to %s is not allowed: an auto-fix pushes only the PR branch %s", r.Branch, r.Allowed)
		}
	default:
		return refuse("push to %s is not allowed", r.Branch)
	}
	return nil
}

// IsProtected reports whether branch matches one of the patterns.
func IsProtected(branch string, patterns []string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, branch); err == nil && ok {
			return true
		}
	}
	return false
}
