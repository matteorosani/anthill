package domain_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

var (
	roundNullDeref = domain.Finding{
		Location: domain.Location{File: "src/auth.ts", StartLine: 42},
		Severity: domain.SeverityBlocking, Classification: domain.NeedsDeveloper,
		Message: "user may be null here",
	}
	roundUnusedImport = domain.Finding{
		Location: domain.Location{File: "src/util.ts", StartLine: 1},
		Severity: domain.SeverityNit, Classification: domain.AutoFixable,
		Message: "unused import of fs", SuggestedFix: "",
	}
	roundNoTests = domain.Finding{
		Severity: domain.SeveritySuggestion, Classification: domain.NeedsDeveloper,
		Message: "the change has no tests",
	}
)

// inlineThread is a review thread whose first comment is the inline
// comment of f, with ID comment.
func inlineThread(id, comment string, f domain.Finding, resolved bool) domain.ReviewThread {
	body := domain.AnthillComment{Role: domain.RoleShepherd, Body: f.Message, Marker: domain.Marker{
		Version: 1, Instance: instance, Kind: domain.CommentFinding, Ref: "gh-pr-34", FindingFields: f.MarkerFields(),
	}}.String()
	return domain.ReviewThread{ID: id, Path: f.File, Line: f.StartLine, Resolved: resolved, Comments: []domain.Comment{{ID: comment, Body: body}}}
}

func TestRoundMarkerCarriesFindings(t *testing.T) {
	m := roundMarker(domain.CommentSummary, &domain.RoundFields{Head: "c1", Findings: []domain.RoundFinding{
		domain.NewRoundFinding(roundNullDeref, "101"),
		domain.NewRoundFinding(domain.Finding{Severity: domain.SeverityNit, Classification: domain.NeedsDeveloper, Message: "x --> y <b>"}, ""),
	}})
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if strings.Contains(m.String()[len("<!-- anthill "):len(m.String())-len(" -->")], "-->") {
		t.Fatalf("marker %s closes the HTML comment early", m)
	}
	got, ok := domain.FindMarker("text\n\n" + m.String())
	if !ok {
		t.Fatalf("FindMarker(%s) found nothing", m)
	}
	assertMarker(t, got, m)

	bad := roundMarker(domain.CommentSummary, &domain.RoundFields{Head: "c1", Findings: []domain.RoundFinding{{Severity: "major", Classification: domain.AutoFixable, Message: "m"}}})
	if err := bad.Validate(); err == nil {
		t.Error("Validate() of a round finding with an invalid severity = nil")
	}
}

// RoundOf places each finding in its thread: by the thread's first
// comment, else by its finding marker, an open thread first. A finding
// only in the summary has no thread and is never resolved.
func TestRoundOfPlacesFindings(t *testing.T) {
	threads := []domain.ReviewThread{
		inlineThread("T1", "101", roundNullDeref, false),
		inlineThread("T2", "102", roundUnusedImport, true),
		// The open thread of an earlier problem with the same message.
		inlineThread("T3", "103", domain.Finding{Location: domain.Location{File: "src/x.ts", StartLine: 5}, Severity: domain.SeverityNit, Classification: domain.NeedsDeveloper, Message: "rename"}, true),
		inlineThread("T4", "104", domain.Finding{Location: domain.Location{File: "src/x.ts", StartLine: 7}, Severity: domain.SeverityNit, Classification: domain.NeedsDeveloper, Message: "rename"}, false),
	}
	moved := domain.Finding{Location: domain.Location{File: "src/x.ts", StartLine: 6}, Severity: domain.SeverityNit, Classification: domain.NeedsDeveloper, Message: "rename"}
	c := domain.Comment{Author: "mrosani", Body: domain.AnthillComment{Role: domain.RoleShepherd, Body: "2 findings", Marker: roundMarker(domain.CommentSummary, &domain.RoundFields{
		Head: "c2", Requested: true, Findings: []domain.RoundFinding{
			domain.NewRoundFinding(roundNullDeref, "101"),
			domain.NewRoundFinding(roundUnusedImport, "102"),
			domain.NewRoundFinding(roundNoTests, ""),
			// Its comment was deleted: placed by its marker.
			domain.NewRoundFinding(moved, "999"),
		},
	})}.String()}
	got, ok := domain.RoundOf(c, threads)
	want := domain.PRRound{Head: "c2", Requested: true, Findings: []domain.PRFinding{
		{Finding: roundNullDeref, Thread: "T1"},
		{Finding: roundUnusedImport, Thread: "T2", Resolved: true},
		{Finding: roundNoTests},
		{Finding: moved, Thread: "T4"},
	}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("RoundOf() =\n%+v, %v\nwant\n%+v", got, ok, want)
	}
}

func TestHandedOf(t *testing.T) {
	status := func(id string) domain.Comment {
		return domain.Comment{Body: domain.AnthillComment{Role: domain.RoleAnt, Body: domain.HandedText("c1abcdef9", 2), Marker: domain.Marker{
			Version: 1, ID: id, Instance: instance, Kind: domain.CommentStatus, Ref: "gh-pr-34",
		}}.String()}
	}
	if head, ok := domain.HandedOf(status(domain.HandedMarkerID("c1abcdef9"))); !ok || head != "c1abcdef9" {
		t.Errorf("HandedOf(handed comment) = %q, %v", head, ok)
	}
	for _, c := range []domain.Comment{status("round-limit"), status("handed-"), {Body: "handed-c1"}} {
		if head, ok := domain.HandedOf(c); ok {
			t.Errorf("HandedOf(%q) = %q, true", c.Body, head)
		}
	}
	if text := domain.HandedText("c1abcdef9", 1); !strings.Contains(text, "1 finding of") {
		t.Errorf("HandedText(1) = %q", text)
	}
}
