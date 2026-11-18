package domain

import (
	"fmt"
	"strings"
)

// The delimiters of the open points section of an ant PR's body. They
// let a later partial finish replace the section instead of adding a
// second one.
const (
	openPointsStart = "<!-- anthill:open-points -->"
	openPointsEnd   = "<!-- /anthill:open-points -->"
)

// OpenPointsSection is the section of an ant PR's body that lists the
// open points of a partial finish or a time-out (spec ant-execution,
// "Draft PRs for incomplete work"), or "" when there are none.
func OpenPointsSection(points []string) string {
	if len(points) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(openPointsStart + "\n### Open points\n\n")
	for _, p := range points {
		fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(p))
	}
	b.WriteString(openPointsEnd)
	return b.String()
}

// WithOpenPoints returns body with its open points section replaced by
// one listing points, or removed when points is empty. A body without
// the section gets it at the end.
func WithOpenPoints(body string, points []string) string {
	section := OpenPointsSection(points)
	if i := strings.Index(body, openPointsStart); i >= 0 {
		if j := strings.Index(body[i:], openPointsEnd); j >= 0 {
			rest := body[i+j+len(openPointsEnd):]
			if section != "" {
				return body[:i] + section + rest
			}
			before, after := strings.TrimRight(body[:i], " \t\r\n"), strings.TrimLeft(rest, " \t\r\n")
			if before == "" || after == "" {
				return before + after
			}
			return before + "\n\n" + after
		}
	}
	if section == "" {
		return body
	}
	body = strings.TrimRight(body, " \t\r\n")
	if body == "" {
		return section
	}
	return body + "\n\n" + section
}

// FinishPRBody is the body of an ant PR that Anthill opens itself: the
// finish summary, or the progress summary of a time-out, with the open
// points (spec ant-execution, "Finish without opening a PR"). The
// closing reference is added by WithClosingReference.
func FinishPRBody(summary string, points []string) string {
	return WithOpenPoints(strings.TrimSpace(summary), points)
}

// MessageNoChangeExplanation is the ticket comment of a no-change finish
// (spec ant-execution, "No change needed").
func MessageNoChangeExplanation(explanation, holdLabel string) string {
	return fmt.Sprintf("No code change is needed for this item:\n\n%s\n\nThe `%s` label was added and the claim released. Close the item, or remove the label to make it eligible again.",
		strings.TrimSpace(explanation), holdLabel)
}

// WrapUpNote is what Anthill did when it wrapped up an ant that ended
// without finishing, for the ticket comment.
type WrapUpNote struct {
	// Reason is why the run ended, such as "exited without finish".
	Reason string
	// Text is the failure summary or the progress summary.
	Text string
	// Branch is the ant branch, and Pushed reports that it was pushed.
	Branch string
	Pushed bool
	// PR is the link to the ant PR, or "", and Draft reports that it is
	// a draft now. Opened reports that Anthill opened it.
	PR     string
	Draft  bool
	Opened bool
}

// WrapUpComment is the ticket comment of an ant that ended without
// finishing (spec ant-execution, "Exit without finishing"; spec
// ant-timebox, "Budget exhaustion").
func WrapUpComment(n WrapUpNote) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The run ended without finishing: %s.\n", n.Reason)
	if text := strings.TrimSpace(n.Text); text != "" {
		fmt.Fprintf(&b, "\n%s\n", text)
	}
	b.WriteString("\n")
	switch {
	case n.Pushed:
		fmt.Fprintf(&b, "The new commits were pushed to `%s`.", n.Branch)
	default:
		b.WriteString("Nothing was pushed: the branch has no new commits.")
	}
	switch {
	case n.PR != "" && n.Opened:
		fmt.Fprintf(&b, " A draft pull request was opened: %s", n.PR)
	case n.PR != "" && n.Draft:
		fmt.Fprintf(&b, " The pull request %s is a draft until a run finishes complete.", n.PR)
	case n.PR != "":
		fmt.Fprintf(&b, " Pull request: %s", n.PR)
	}
	return strings.TrimRight(b.String(), " ") + "\n"
}
