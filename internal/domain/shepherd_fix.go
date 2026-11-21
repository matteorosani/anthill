package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// The limits of a fix pass (spec pr-shepherd, "Fix agent pass"). Neither
// is a setting.
const (
	// FixAgentMinutes is how long the fix agent may run before the
	// executor ends it.
	FixAgentMinutes = 30
	// VerificationMinutes is how long the verification command may run
	// before it is ended and counts as failed.
	VerificationMinutes = 15
)

// FixFailure is why a whole fix pass fell back to developers (spec
// pr-shepherd, "Failed auto-fix falls back to developer").
type FixFailure string

// The reasons a fix pass pushes nothing.
const (
	FixNotReported    FixFailure = "the fix agent ended without reporting its fixes"
	FixVerifyFailed   FixFailure = "the verification command failed"
	FixVerifyTimedOut FixFailure = "the verification command ran out of time"
	FixBranchMoved    FixFailure = "the PR branch moved while the fix was made"
	FixPushRefused    FixFailure = "the push was refused"
	FixDidNotApply    FixFailure = "the fixes did not apply cleanly"
)

// FixedText is the reply on the thread of a finding a verified fix pass
// fixed and pushed.
func FixedText(commit, link string) string {
	short := commit
	if len(short) > 12 {
		short = short[:12]
	}
	if link == "" {
		return fmt.Sprintf("Fixed automatically in `%s`.", short)
	}
	return fmt.Sprintf("Fixed automatically in [`%s`](%s).", short, link)
}

// SkippedText is the reply on the thread of a finding the fix agent
// skipped, with its reason: the finding is now needs-developer.
func SkippedText(reason string) string {
	return fmt.Sprintf("The automatic fix skipped this finding: %s\n\nIt is now `%s` and needs a developer.",
		strings.TrimSpace(reason), NeedsDeveloper)
}

// FixFailedText is the reply on the thread of each finding of a fix pass
// that pushed nothing: the finding is now needs-developer.
func FixFailedText(why FixFailure, detail string) string {
	if detail != "" {
		detail = " (" + detail + ")"
	}
	return fmt.Sprintf("The automatic fix failed: %s%s, so nothing was pushed.\n\nThis finding is now `%s` and needs a developer.",
		why, detail, NeedsDeveloper)
}

// FixBrief is the input of a fix agent: the findings to fix, each with
// the id to report it by, and how to report (spec ant-tools, "Fix report
// tool").
func FixBrief(pr PullRequest, findings []Finding, ids []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are fixing review findings on pull request %s, %q, in this worktree, checked out at its head commit.\n\n", pr.Ref, pr.Title)
	b.WriteString("Fix each finding below with the smallest change, and make one commit per fixed finding. ")
	b.WriteString("Do not push, comment or change anything else. Skip a finding you cannot fix safely, with the reason.\n")
	fmt.Fprintf(&b, "When done, call `%s` once with one result per finding: `fixed` with the commit that fixes it, or `skipped` with the reason.\n\n", ToolFinishFixes)
	b.WriteString("Findings:\n")
	for i, f := range findings {
		where := "location unknown"
		if f.Known() {
			start, end := f.lines()
			switch {
			case start == 0:
				where = f.File
			case end == start:
				where = fmt.Sprintf("%s:%d", f.File, start)
			default:
				where = fmt.Sprintf("%s:%d-%d", f.File, start, end)
			}
		}
		fmt.Fprintf(&b, "\n- id %s (%s, %s): %s", ids[i], f.Severity, where, f.Message)
		if f.SuggestedFix != "" {
			fmt.Fprintf(&b, "\n  Suggested fix: %s", f.SuggestedFix)
		}
	}
	b.WriteString("\n")
	return b.String()
}

// adoPRPath matches the end of an Azure DevOps PR URL.
var adoPRPath = regexp.MustCompile(`/pullrequest/[0-9]+/?$`)

// CommitLink returns the web link to a commit of the PR's branch, or ""
// when the PR has no URL.
func CommitLink(pr PullRequest, commit string) string {
	switch {
	case pr.URL == "":
		return ""
	case pr.Ref.Repo.Provider == AzureDevOps && adoPRPath.MatchString(pr.URL):
		return adoPRPath.ReplaceAllString(pr.URL, "") + "/commit/" + commit
	}
	return strings.TrimRight(pr.URL, "/") + "/commits/" + commit
}

// reclassifiedPrefix starts the marker ID of the reply that reclassifies
// a finding needs-developer after a fix pass.
const reclassifiedPrefix = "reclassified-"

// ReclassifiedMarkerID is the marker ID of the reply, in the finding's
// thread, that reclassifies the finding needs-developer after a fix pass
// skipped it or failed (spec pr-shepherd, "Failed auto-fix falls back to
// developer"). It carries the thread, so that every instance reads the
// finding as needs-developer from then on.
func ReclassifiedMarkerID(thread string) string {
	return reclassifiedPrefix + thread
}

// Reclassified reports whether the comment is the reply that
// reclassifies the finding of thread needs-developer. Whether its author
// has write access, so that a forged marker does not count, is for the
// caller to check.
func Reclassified(c Comment, thread string) bool {
	m, ok := FindMarker(c.Body)
	return ok && m.Kind == CommentReply && m.ID == ReclassifiedMarkerID(thread)
}

// FixedMarkerID is the marker ID of the reply, in the finding's thread,
// that gives the commit of its verified fix.
func FixedMarkerID(thread string) string {
	return "fixed-" + thread
}

// commitHash matches a full SHA-1 or SHA-256 commit hash.
var commitHash = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// IsCommitHash reports whether s is a full commit hash, as opposed to a
// branch name.
func IsCommitHash(s string) bool {
	return commitHash.MatchString(s)
}
