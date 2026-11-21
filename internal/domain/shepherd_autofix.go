package domain

import "slices"

// AutofixUnavailableReason says why a human PR that opted in to auto-fix
// gets no fix pass.
type AutofixUnavailableReason string

// The reasons auto-fix is not possible (spec pr-shepherd, "Automatic
// fixes").
const (
	// AutofixNoVerification: the repository has no verification command.
	AutofixNoVerification AutofixUnavailableReason = "no-verification-command"
	// AutofixNotPushable: the PR's branch is in a fork that does not let
	// the repository's maintainers push to it.
	AutofixNotPushable AutofixUnavailableReason = "fork-not-pushable"
)

// AutofixUnavailableText is the body of the comment that says why the
// auto-fixable findings of a PR that opted in stay suggested changes.
func AutofixUnavailableText(r AutofixUnavailableReason) string {
	switch r {
	case AutofixNoVerification:
		return "Auto-fix needs a verification command, and this repository has none, so the auto-fixable findings " +
			"are left as suggested changes and nothing is pushed. Set `verification-command` in `.anthill.yml` to turn auto-fix on."
	case AutofixNotPushable:
		return "This PR's branch is in a fork that does not allow edits by maintainers, so the auto-fixable findings " +
			"are left as suggested changes and nothing is pushed."
	}
	return "Auto-fix is not possible on this PR, so the auto-fixable findings are left as suggested changes."
}

// PostAutofixUnavailable posts the comment that auto-fix is not possible
// on a human PR that opted in and has findings to fix, and why (spec
// pr-shepherd, "No verification command", "Fork branch not pushable").
// Its effect: the comment for Reason is on the PR.
type PostAutofixUnavailable struct {
	Reason AutofixUnavailableReason
}

// autofixUnavailable decides the comment that auto-fix is not possible.
// A missing verification command is reported before a fork that takes no
// push.
func (s PRSnapshot) autofixUnavailable() (PostAutofixUnavailable, bool) {
	if len(s.fixable()) == 0 {
		return PostAutofixUnavailable{}, false
	}
	var reason AutofixUnavailableReason
	switch {
	case !s.VerificationCommand:
		reason = AutofixNoVerification
	case !s.Pushable:
		reason = AutofixNotPushable
	default:
		return PostAutofixUnavailable{}, false
	}
	if slices.Contains(s.UnavailablePosted, reason) {
		return PostAutofixUnavailable{}, false
	}
	return PostAutofixUnavailable{Reason: reason}, true
}
