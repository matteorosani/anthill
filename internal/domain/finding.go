package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Severity is how much a finding matters (spec pr-shepherd).
type Severity string

// The severities.
const (
	SeverityBlocking   Severity = "blocking"
	SeveritySuggestion Severity = "suggestion"
	SeverityNit        Severity = "nit"
)

// Valid reports whether s is one of the severities.
func (s Severity) Valid() bool {
	return s == SeverityBlocking || s == SeveritySuggestion || s == SeverityNit
}

// Classification tells whether a finding can be fixed without a developer.
type Classification string

// The classifications.
const (
	AutoFixable    Classification = "auto-fixable"
	NeedsDeveloper Classification = "needs-developer"
)

// Valid reports whether c is one of the classifications.
func (c Classification) Valid() bool {
	return c == AutoFixable || c == NeedsDeveloper
}

// FindingProximity is how many lines apart two findings with the same
// fingerprint may be and still be the same problem, for code that moved.
const FindingProximity = 10

// Location is where a finding is. An empty File means the location is not
// known, and the finding goes in the round's summary. StartLine and
// EndLine are 1-based and inclusive. A StartLine of 0 means the whole
// file, and an EndLine of 0 means the range is the single StartLine.
type Location struct {
	File      string
	StartLine int
	EndLine   int
}

// lines returns the range with EndLine filled in.
func (l Location) lines() (start, end int) {
	if l.EndLine == 0 {
		return l.StartLine, l.StartLine
	}
	return l.StartLine, l.EndLine
}

// Known reports whether the location names a file.
func (l Location) Known() bool {
	return l.File != ""
}

// Finding is one problem a review round reports through submit_review
// (spec ant-tools).
type Finding struct {
	Location
	Severity       Severity
	Classification Classification
	Message        string
	// SuggestedFix is optional.
	SuggestedFix string
}

// Validate checks the finding's fields.
func (f Finding) Validate() error {
	switch {
	case !f.Severity.Valid():
		return fmt.Errorf("finding severity %q is not valid", f.Severity)
	case !f.Classification.Valid():
		return fmt.Errorf("finding classification %q is not valid", f.Classification)
	case strings.TrimSpace(f.Message) == "":
		return fmt.Errorf("finding has no message")
	case f.StartLine < 0 || (f.EndLine != 0 && f.EndLine < f.StartLine):
		return fmt.Errorf("finding has an invalid line range %d-%d", f.StartLine, f.EndLine)
	case !f.Known() && f.StartLine != 0:
		return fmt.Errorf("finding has lines but no file")
	}
	return nil
}

// Fingerprint identifies the problem independently of where it is
// reported and how it is rated (design D9). It hashes the normalized file
// path and the normalized message with SHA-256 and keeps the first 16 hex
// characters. Severity and classification are left out, so a problem
// rated differently in a later round still matches. A reworded message
// does not.
func (f Finding) Fingerprint() string {
	sum := sha256.Sum256([]byte(normalizePath(f.File) + "\x00" + normalizeMessage(f.Message)))
	return hex.EncodeToString(sum[:])[:16]
}

// MarkerFields returns the fields a finding comment's marker carries.
func (f Finding) MarkerFields() *FindingFields {
	start, end := f.lines()
	return &FindingFields{
		Fingerprint: f.Fingerprint(),
		File:        normalizePath(f.File),
		StartLine:   start,
		EndLine:     end,
	}
}

// normalizePath uses forward slashes and drops a leading "./" or "/".
func normalizePath(p string) string {
	if p == "" {
		return ""
	}
	p = path.Clean(strings.ReplaceAll(p, `\`, "/"))
	return strings.TrimPrefix(p, "/")
}

// normalizeMessage reduces a message to its words, so that changes in
// case, spacing, punctuation and numbers (often line numbers) do not
// change the fingerprint. Each run of digits becomes "0". A "." or "_"
// between two word characters is kept, as in "auth.go" or "max_retries".
func normalizeMessage(msg string) string {
	runes := []rune(strings.ToLower(norm.NFKC.String(msg)))
	isWord := func(i int) bool {
		return i >= 0 && i < len(runes) && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]))
	}
	var b strings.Builder
	space := false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case unicode.IsDigit(r):
			for i+1 < len(runes) && unicode.IsDigit(runes[i+1]) {
				i++
			}
			r = '0'
		case unicode.IsLetter(r):
		case (r == '.' || r == '_') && isWord(i-1) && isWord(i+1):
		default:
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// PriorFinding is a finding posted by an earlier round whose thread is
// still unresolved.
type PriorFinding struct {
	FindingFields
	// ThreadID is the review thread of the finding, so a new round can
	// link to it.
	ThreadID string
}

// PriorFindings reads the finding markers of a PR's unresolved review
// threads. A thread counts when its first comment is an Anthill finding
// with a fingerprint.
func PriorFindings(threads []ReviewThread) []PriorFinding {
	var prior []PriorFinding
	for _, t := range threads {
		if t.Resolved || len(t.Comments) == 0 {
			continue
		}
		m, ok := FindMarker(t.Comments[0].Body)
		if !ok || m.Kind != CommentFinding || m.FindingFields == nil {
			continue
		}
		prior = append(prior, PriorFinding{FindingFields: *m.FindingFields, ThreadID: t.ID})
	}
	return prior
}

// Matches reports whether f is the same problem as an earlier finding:
// the same file and fingerprint, and line ranges at most FindingProximity
// lines apart. When either has no line range, file and fingerprint
// decide alone.
func (f Finding) Matches(p PriorFinding) bool {
	if normalizePath(f.File) != p.File || f.Fingerprint() != p.Fingerprint {
		return false
	}
	if f.StartLine == 0 || p.StartLine == 0 {
		return true
	}
	start, end := f.lines()
	pStart, pEnd := Location{StartLine: p.StartLine, EndLine: p.EndLine}.lines()
	return lineGap(start, end, pStart, pEnd) <= FindingProximity
}

// lineGap is the number of lines between two ranges, 0 when they overlap.
func lineGap(start1, end1, start2, end2 int) int {
	switch {
	case end1 < start2:
		return start2 - end1
	case end2 < start1:
		return start1 - end2
	}
	return 0
}

// MatchPrior returns the earlier finding that f repeats, if any. The round
// then links to its thread instead of posting f again (spec pr-shepherd,
// "Findings are not repeated").
func MatchPrior(f Finding, prior []PriorFinding) (PriorFinding, bool) {
	for _, p := range prior {
		if f.Matches(p) {
			return p, true
		}
	}
	return PriorFinding{}, false
}
