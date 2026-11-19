package domain

import (
	"fmt"
	"strings"
)

// RenderChangeMessage renders an actionable change as a message to the
// ant's active run (spec change-reaction, "Delivery to an active run"):
// an edit of the ticket's title, body or acceptance criteria as a diff of
// the field, any other change as it would appear in a run's input. People
// wrote the text, so it is quoted like the item and its comments.
func RenderChangeMessage(c AntChange) string {
	var b strings.Builder
	b.WriteString("Anthill: a change arrived while you work. Take it into account, then continue.\n\n")
	b.WriteString(untrustedNotice + "\n\n")
	e := c.Event
	if e.Kind != ChangeItemEdited {
		writeChange(&b, c)
		return b.String()
	}
	fmt.Fprintf(&b, "%s edited the %s of %s at %s. The diff, with - for removed lines and + for added ones:\n\n",
		actor(e.Actor), fieldName(e.Field), e.Ref.String(), stamp(e.At))
	writeQuoted(&b, LineDiff(e.Old, e.Body))
	return b.String()
}

// DescribeChange describes a change in one line, for the queen.
func DescribeChange(e ChangeEvent) string {
	who, where := actor(e.Actor), e.Ref.String()
	switch e.Kind {
	case ChangeItemEdited:
		return fmt.Sprintf("%s edited the %s of %s", who, fieldName(e.Field), where)
	case ChangeCommentAdded:
		return fmt.Sprintf("%s commented on %s", who, where)
	case ChangeCommentEdited:
		return fmt.Sprintf("%s edited a comment on %s", who, where)
	case ChangeReviewSubmitted:
		return fmt.Sprintf("%s submitted a review (%s) on %s", who, oneLine(string(e.Review)), where)
	}
	return fmt.Sprintf("%s changed %s (%s)", who, where, oneLine(string(e.Kind)))
}

// maxDiffLines bounds the lines LineDiff compares, as its table grows
// with the product of both sides. Longer texts are shown whole instead.
const maxDiffLines = 2000

// LineDiff is a line diff from old to new: unchanged lines start with two
// spaces, removed lines with "- " and added lines with "+ ". It finds a
// longest common subsequence of lines, so a small edit shows as a small
// diff. Texts longer than maxDiffLines lines are shown as all of old
// removed and all of new added.
func LineDiff(old, new string) string {
	a, b := diffLines(old), diffLines(new)
	var out strings.Builder
	line := func(prefix, s string) { out.WriteString(prefix + s + "\n") }
	if len(a) > maxDiffLines || len(b) > maxDiffLines {
		for _, s := range a {
			line("- ", s)
		}
		for _, s := range b {
			line("+ ", s)
		}
		return out.String()
	}
	// lcs[i][j] is the length of a longest common subsequence of a[i:]
	// and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			line("  ", a[i])
			i, j = i+1, j+1
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			// Removed lines come before the lines added in their place.
			line("- ", a[i])
			i++
		default:
			line("+ ", b[j])
			j++
		}
	}
	return out.String()
}

// diffLines splits text into lines without their line ends. Empty text
// has no lines.
func diffLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
