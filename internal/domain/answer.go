package domain

import (
	"regexp"
	"strings"
	"time"
)

// PendingQuestion is an ant's question that has no answer yet, as answer
// detection sees it (spec agent-conversation, "Answers resume the
// session").
type PendingQuestion struct {
	// On is the ticket or PR the question was posted on.
	On        Ref
	CommentID string
	// Thread is the thread the question comment opened on a threaded
	// provider, or "" where comments are not threaded.
	Thread string
	// Text is the question's text as the ant wrote it, without the 🐜
	// prefix and the marker.
	Text    string
	AskedAt time.Time
}

// minQuote is the shortest quoted text that counts as quoting a question,
// unless the question itself is shorter: a quoted "yes" or "ok" occurs in
// almost any question.
const minQuote = 10

// anthillMention matches @anthill as a mention of its own, not as the
// start of a longer login such as @anthill-bot.
var anthillMention = regexp.MustCompile(`(?i)(^|[^\w@./-])@anthill($|[^\w-])`)

// IsAnswer reports whether the change e answers the pending question q
// (spec agent-conversation, "Answers resume the session"). It must be a
// new human comment or review on the ticket or PR of the question, made
// after it by a user with write access (design, Risks: only answers from
// write-access users are acted on), and must hold no /anthill command,
// which is handled as a command instead. Then it answers when it:
//   - is a reply in the question's thread, on a threaded provider;
//   - quotes the question: a quoted line whose text occurs in the
//     question's; or
//   - mentions @anthill outside quotes and code blocks.
//
// An edited comment never becomes an answer: the first human comment
// after the question that answers it counts, and an edit is not a new
// comment.
func IsAnswer(q PendingQuestion, e ChangeEvent, writers UserSet) bool {
	switch {
	case e.Kind != ChangeCommentAdded && e.Kind != ChangeReviewSubmitted,
		e.Origin == OriginAnthill,
		e.Ref != q.On,
		e.CommentID == "" || e.CommentID == q.CommentID,
		e.At.Before(q.AskedAt),
		!writers.Contains(e.Actor),
		strings.TrimSpace(e.Body) == "",
		len(ParseCommands(e.Body)) > 0:
		return false
	}
	if q.Thread != "" && e.ThreadID == q.Thread {
		return true
	}
	quoted, plain := splitQuoted(e.Body)
	return quotes(quoted, q.Text) || anthillMention.MatchString(plain)
}

// splitQuoted splits a Markdown comment into its quoted lines, without
// the quote marks, and its other lines outside fenced code blocks.
func splitQuoted(body string) (quoted []string, plain string) {
	var b strings.Builder
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		switch {
		case inFence:
		case strings.HasPrefix(trimmed, ">"):
			quoted = append(quoted, strings.TrimLeft(trimmed, "> \t"))
		default:
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return quoted, b.String()
}

// quotes reports whether one of the quoted lines is part of the
// question's text. Whitespace and case do not matter.
func quotes(quoted []string, question string) bool {
	q := normalizeSpace(question)
	if q == "" {
		return false
	}
	for _, line := range quoted {
		l := normalizeSpace(line)
		if len(l) >= min(minQuote, len(q)) && strings.Contains(q, l) {
			return true
		}
	}
	return false
}

// normalizeSpace lowercases s and collapses its runs of white space.
func normalizeSpace(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
