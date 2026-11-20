package domain

import (
	"fmt"
	"strings"
)

// RoundFinding is one finding of a round as the round's summary marker
// carries it (RoundFields.Findings).
type RoundFinding struct {
	File           string         `json:"file,omitempty"`
	StartLine      int            `json:"start_line,omitempty"`
	EndLine        int            `json:"end_line,omitempty"`
	Severity       Severity       `json:"severity"`
	Classification Classification `json:"classification"`
	Message        string         `json:"message"`
	SuggestedFix   string         `json:"suggested_fix,omitempty"`
	// Comment is the ID of the first comment of the finding's review
	// thread: the inline comment the round posted, or the comment of the
	// open thread the finding repeats. It is empty for a finding only in
	// the summary.
	Comment string `json:"comment,omitempty"`
}

// NewRoundFinding returns the marker form of f, in the thread whose
// first comment is comment, or in no thread when comment is empty.
func NewRoundFinding(f Finding, comment string) RoundFinding {
	return RoundFinding{
		File: f.File, StartLine: f.StartLine, EndLine: f.EndLine,
		Severity: f.Severity, Classification: f.Classification,
		Message: f.Message, SuggestedFix: f.SuggestedFix,
		Comment: comment,
	}
}

// Finding returns the finding.
func (r RoundFinding) Finding() Finding {
	return Finding{
		Location:       Location{File: r.File, StartLine: r.StartLine, EndLine: r.EndLine},
		Severity:       r.Severity,
		Classification: r.Classification,
		Message:        r.Message,
		SuggestedFix:   r.SuggestedFix,
	}
}

// RoundOf reads the round that a round's summary comment records: its
// head commit, whether it was requested, and its findings, each placed in
// its review thread among threads, the PR's threads. It returns false for
// any other comment, including the run summaries of ants. Whether the
// author has write access, so that a forged marker does not count, is for
// the caller to check, as is Handed (HandedOf).
func RoundOf(c Comment, threads []ReviewThread) (PRRound, bool) {
	m, ok := FindMarker(c.Body)
	if !ok || m.Kind != CommentSummary || m.RoundFields == nil {
		return PRRound{}, false
	}
	r := PRRound{Head: m.Head, Requested: m.Requested}
	for _, rf := range m.Findings {
		f := PRFinding{Finding: rf.Finding()}
		if t, ok := findingThread(rf, threads); ok {
			f.Thread, f.Resolved = t.ID, t.Resolved
		}
		r.Findings = append(r.Findings, f)
	}
	return r, true
}

// findingThread finds the review thread of a round's finding: the one
// whose first comment is rf.Comment, else, for a comment that is gone,
// the first thread whose finding marker matches the finding (Matches),
// an open one before a resolved one.
func findingThread(rf RoundFinding, threads []ReviewThread) (ReviewThread, bool) {
	if rf.Comment == "" {
		return ReviewThread{}, false
	}
	for _, t := range threads {
		if len(t.Comments) > 0 && t.Comments[0].ID == rf.Comment {
			return t, true
		}
	}
	f := rf.Finding()
	var resolved *ReviewThread
	for i, t := range threads {
		if len(t.Comments) == 0 {
			continue
		}
		m, ok := FindMarker(t.Comments[0].Body)
		if !ok || m.Kind != CommentFinding || m.FindingFields == nil || !f.Matches(PriorFinding{FindingFields: *m.FindingFields, ThreadID: t.ID}) {
			continue
		}
		if !t.Resolved {
			return t, true
		}
		if resolved == nil {
			resolved = &threads[i]
		}
	}
	if resolved != nil {
		return *resolved, true
	}
	return ReviewThread{}, false
}

// HandedMarkerID is the marker ID of the status comment that the owning
// instance posts once it has handed the findings of the round on head to
// its ant (spec change-reaction, "Shepherd findings on ant PRs"), so that
// every instance sees the round as handed.
func HandedMarkerID(head string) string {
	return "handed-" + head
}

// HandedOf returns the head commit whose round's findings the comment
// says were handed to the owning ant, and false for any other comment.
// Whether the author has write access is for the caller to check.
func HandedOf(c Comment) (string, bool) {
	m, ok := FindMarker(c.Body)
	if !ok || m.Kind != CommentStatus {
		return "", false
	}
	head, ok := strings.CutPrefix(m.ID, HandedMarkerID(""))
	if !ok || head == "" {
		return "", false
	}
	return head, true
}

// HandedText is the body of the status comment that a round's findings
// were handed to the owning ant.
func HandedText(head string, n int) string {
	noun := "findings"
	if n == 1 {
		noun = "finding"
	}
	return fmt.Sprintf("Handed the %d %s of the review round on %s to the ant's next follow-up run.", n, noun, shortHash(head))
}
