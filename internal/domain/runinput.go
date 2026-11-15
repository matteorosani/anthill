package domain

import (
	"fmt"
	"strings"
	"time"
)

// RenderRunInput renders what a run gets besides its brief: the answer to
// the ant's question and the stored changes, shepherd findings included
// (spec ant-execution, "Sessions are persisted for resumption"; spec
// change-reaction, "Changes while the ant is between runs"). It returns
// "" when there is neither. People wrote this text, so it is quoted like
// the item and its comments.
func RenderRunInput(answer *ChangeEvent, changes []AntChange) string {
	if answer == nil && len(changes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Input for this run\n\n")
	b.WriteString(untrustedNotice + "\n\n")
	if answer != nil {
		b.WriteString("### Answer to your question\n\n")
		fmt.Fprintf(&b, "%s answered at %s", actor(answer.Actor), stamp(answer.At))
		if answer.ThreadID != "" {
			b.WriteString(" in the question's thread")
		}
		b.WriteString(":\n\n")
		writeQuoted(&b, answer.Body)
		b.WriteString("\n")
	}
	if len(changes) > 0 {
		b.WriteString("### Changes since your last run\n\n")
		for i, c := range changes {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "%d. ", i+1)
			writeChange(&b, c)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// RenderResumeInput renders the input of a run that resumes the ant's
// session: the answer and the stored changes, or a request to continue
// when there is neither, as after a retry.
func RenderResumeInput(answer *ChangeEvent, changes []AntChange) string {
	in := RenderRunInput(answer, changes)
	if in == "" {
		return "Anthill started a new run of your session. Check the worktree, then continue your work and finish through the ant tools.\n"
	}
	return "Anthill started a new run of your session with the input below. Continue your work with it, and finish through the ant tools.\n\n" + in
}

// writeChange describes one change and quotes its text.
func writeChange(b *strings.Builder, c AntChange) {
	e := c.Event
	who, when, where := actor(e.Actor), stamp(e.At), e.Ref.String()
	switch e.Kind {
	case ChangeItemEdited:
		fmt.Fprintf(b, "%s edited the %s of %s at %s.\n\nBefore:\n\n", who, fieldName(e.Field), where, when)
		writeQuoted(b, e.Old)
		b.WriteString("\nAfter:\n\n")
		writeQuoted(b, e.Body)
	case ChangeCommentAdded, ChangeCommentEdited:
		verb := "commented on"
		if e.Kind == ChangeCommentEdited {
			verb = "edited a comment on"
		}
		fmt.Fprintf(b, "%s %s %s at %s", who, verb, where, when)
		if e.ThreadID != "" {
			fmt.Fprintf(b, " in review thread %s", code(e.ThreadID))
		}
		b.WriteString(":\n\n")
		writeQuoted(b, e.Body)
	case ChangeReviewSubmitted:
		fmt.Fprintf(b, "%s submitted a review (%s) on %s at %s", who, oneLine(string(e.Review)), where, when)
		if strings.TrimSpace(e.Body) == "" {
			b.WriteString(".\n")
		} else {
			b.WriteString(":\n\n")
			writeQuoted(b, e.Body)
		}
	case ChangeLabelAdded, ChangeLabelRemoved:
		verb := "added"
		if e.Kind == ChangeLabelRemoved {
			verb = "removed"
		}
		fmt.Fprintf(b, "%s %s the label %s on %s at %s.\n", who, verb, code(e.Label), where, when)
	case ChangeStateChanged, ChangeDraftChanged:
		fmt.Fprintf(b, "%s changed the state of %s to %s at %s.\n", who, where, code(e.State), when)
	case ChangeCommitsPushed:
		fmt.Fprintf(b, "%s pushed commits to %s at %s; the head is now %s. Fetch them through your worktree before you change anything.\n", who, where, when, code(shortHash(e.Head)))
	default:
		fmt.Fprintf(b, "%s changed %s (%s) at %s.\n", who, where, oneLine(string(e.Kind)), when)
		if strings.TrimSpace(e.Body) != "" {
			b.WriteString("\n")
			writeQuoted(b, e.Body)
		}
	}
	if len(c.Findings) > 0 {
		fmt.Fprintf(b, "\nThe shepherd's findings on %s, to fix and to answer with `%s` on each finding's thread:\n\n", where, ToolReplyToThread)
		for _, f := range c.Findings {
			writeFinding(b, f)
		}
	}
}

func writeFinding(b *strings.Builder, f Finding) {
	fmt.Fprintf(b, "- %s, %s", oneLine(string(f.Severity)), oneLine(string(f.Classification)))
	if f.File != "" {
		fmt.Fprintf(b, ", %s", code(f.File))
		if start, end := f.lines(); start > 0 {
			if end > start {
				fmt.Fprintf(b, " lines %d-%d", start, end)
			} else {
				fmt.Fprintf(b, " line %d", start)
			}
		}
	}
	b.WriteString(":\n\n")
	writeQuoted(b, f.Message)
	if strings.TrimSpace(f.SuggestedFix) != "" {
		b.WriteString("\n  Suggested fix:\n\n")
		writeQuoted(b, f.SuggestedFix)
	}
	b.WriteString("\n")
}

func fieldName(f ItemField) string {
	switch f {
	case FieldTitle:
		return "title"
	case FieldBody:
		return "description"
	case FieldAcceptanceCriteria:
		return "acceptance criteria"
	}
	return oneLine(string(f))
}

// actor names who made a change, which a provider may not say.
func actor(login string) string {
	if login = oneLine(login); login != "" {
		return login
	}
	return "Someone"
}

func stamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// code writes a value as inline code. Backticks in it are replaced, so it
// cannot end the code span.
func code(s string) string {
	return "`" + strings.ReplaceAll(oneLine(s), "`", "'") + "`"
}
