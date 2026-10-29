package domain_test

import (
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

var nullDeref = domain.Finding{
	Location:       domain.Location{File: "src/auth.ts", StartLine: 42, EndLine: 44},
	Severity:       domain.SeverityBlocking,
	Classification: domain.NeedsDeveloper,
	Message:        "Possible null dereference of `user.session` on line 42.",
}

// findingThread posts f as a round's inline comment would, in a thread.
func findingThread(id string, f domain.Finding, resolved bool) domain.ReviewThread {
	c := domain.AnthillComment{
		Role: domain.RoleShepherd,
		Body: f.Message,
		Marker: domain.Marker{Version: domain.MarkerVersion, Instance: instance, Kind: domain.CommentFinding,
			Ref: "gh-pr-34", FindingFields: f.MarkerFields()},
	}
	return domain.ReviewThread{ID: id, Path: f.File, Line: f.StartLine, Resolved: resolved,
		Comments: []domain.Comment{{ID: id + "-1", Author: "mrosani", Body: c.String()}}}
}

func TestFindingMatchesEarlierFinding(t *testing.T) {
	moved := func(start, end int) domain.Finding {
		f := nullDeref
		f.StartLine, f.EndLine = start, end
		return f
	}
	tests := []struct {
		name  string
		round domain.Finding
		want  bool
	}{
		{"unchanged problem in round 2", nullDeref, true},
		{"moved by a few lines still matches", moved(47, 49), true},
		{"moved 10 lines away still matches", moved(54, 56), true},
		{"moved 11 lines away is another problem", moved(55, 57), false},
		{"moved up still matches", moved(35, 36), true},
		{"the line number in the message changed", func() domain.Finding {
			f := moved(47, 49)
			f.Message = "Possible null dereference of `user.session` on line 47."
			return f
		}(), true},
		{"case, spacing and punctuation changed", func() domain.Finding {
			f := nullDeref
			f.Message = "possible NULL dereference of user.session   on line 42"
			return f
		}(), true},
		{"rated differently", func() domain.Finding {
			f := nullDeref
			f.Severity, f.Classification = domain.SeveritySuggestion, domain.AutoFixable
			return f
		}(), true},
		{"path written with ./ and backslashes", func() domain.Finding {
			f := nullDeref
			f.File = `.\src\auth.ts`
			return f
		}(), true},
		{"another file", func() domain.Finding {
			f := nullDeref
			f.File = "src/login.ts"
			return f
		}(), false},
		{"another problem at the same place", func() domain.Finding {
			f := nullDeref
			f.Message = "Unused import `crypto`."
			return f
		}(), false},
		{"a dotted name is not split", func() domain.Finding {
			f := nullDeref
			f.Message = "Possible null dereference of `user` `session` on line 42."
			return f
		}(), false},
	}
	prior := domain.PriorFindings([]domain.ReviewThread{findingThread("T1", nullDeref, false)})
	if len(prior) != 1 {
		t.Fatalf("PriorFindings() = %+v, want one", prior)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := domain.MatchPrior(tt.round, prior)
			if ok != tt.want {
				t.Fatalf("MatchPrior() = %v, want %v (fingerprints %s and %s)", ok, tt.want, tt.round.Fingerprint(), prior[0].Fingerprint)
			}
			if ok && got.ThreadID != "T1" {
				t.Errorf("thread = %q, want T1", got.ThreadID)
			}
		})
	}
}

func TestPriorFindingsSkipsResolvedAndHumanThreads(t *testing.T) {
	threads := []domain.ReviewThread{
		findingThread("T1", nullDeref, true),
		{ID: "T2", Comments: []domain.Comment{{Body: "Please rename this."}}},
		{ID: "T3"},
	}
	if prior := domain.PriorFindings(threads); len(prior) != 0 {
		t.Errorf("PriorFindings() = %+v, want none", prior)
	}
}

func TestFindingWithoutLocation(t *testing.T) {
	general := domain.Finding{Severity: domain.SeveritySuggestion, Classification: domain.NeedsDeveloper,
		Message: "The PR has no tests for the new endpoint."}
	prior := []domain.PriorFinding{{FindingFields: *general.MarkerFields(), ThreadID: "S1"}}
	if _, ok := domain.MatchPrior(general, prior); !ok {
		t.Error("a finding without a location did not match itself")
	}
	wholeFile := domain.Finding{Location: domain.Location{File: "src/auth.ts"}, Severity: domain.SeverityNit,
		Classification: domain.AutoFixable, Message: nullDeref.Message}
	if _, ok := domain.MatchPrior(wholeFile, domain.PriorFindings([]domain.ReviewThread{findingThread("T1", nullDeref, false)})); !ok {
		t.Error("a whole-file finding did not match the same problem on lines")
	}
}

func TestFindingFingerprintIsStable(t *testing.T) {
	fp := nullDeref.Fingerprint()
	if len(fp) != 16 {
		t.Errorf("Fingerprint() = %q, want 16 hex characters", fp)
	}
	if again := nullDeref.Fingerprint(); again != fp {
		t.Errorf("Fingerprint() changed from %q to %q", fp, again)
	}
}

func TestFindingValidate(t *testing.T) {
	tests := []struct {
		name   string
		change func(*domain.Finding)
		ok     bool
	}{
		{"valid", func(*domain.Finding) {}, true},
		{"single line", func(f *domain.Finding) { f.EndLine = 0 }, true},
		{"no location", func(f *domain.Finding) { f.Location = domain.Location{} }, true},
		{"unknown severity", func(f *domain.Finding) { f.Severity = "major" }, false},
		{"unknown classification", func(f *domain.Finding) { f.Classification = "maybe" }, false},
		{"no message", func(f *domain.Finding) { f.Message = " " }, false},
		{"reversed lines", func(f *domain.Finding) { f.StartLine, f.EndLine = 44, 42 }, false},
		{"lines without a file", func(f *domain.Finding) { f.File = "" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := nullDeref
			tt.change(&f)
			if err := f.Validate(); (err == nil) != tt.ok {
				t.Errorf("Validate() = %v, want ok %v", err, tt.ok)
			}
		})
	}
}

func TestFindingMarkerRoundTrip(t *testing.T) {
	single := nullDeref
	single.EndLine = 0
	fields := single.MarkerFields()
	if fields.StartLine != 42 || fields.EndLine != 42 || fields.File != "src/auth.ts" {
		t.Errorf("MarkerFields() = %+v, want lines 42-42 in src/auth.ts", fields)
	}
	m := domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentFinding, Ref: "gh-pr-34", FindingFields: fields}
	got, ok := domain.FindMarker(m.String())
	if !ok || got.FindingFields == nil || *got.FindingFields != *fields || got.ClaimFields != nil {
		t.Errorf("FindMarker() = %+v, %v; want the finding fields back", got, ok)
	}
	// A finding marker without fields stays valid, and a claim has none.
	if got, ok := domain.FindMarker(domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentFinding, Ref: "gh-pr-34"}.String()); !ok || got.FindingFields != nil {
		t.Errorf("finding marker without fields = %+v, %v", got, ok)
	}
}

func TestFindingMarkerValidate(t *testing.T) {
	tests := []struct {
		name string
		m    domain.Marker
	}{
		{"finding fields on a status marker", domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentStatus, Ref: "gh-12",
			FindingFields: &domain.FindingFields{Fingerprint: "abc"}}},
		{"no fingerprint", domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentFinding, Ref: "gh-pr-34",
			FindingFields: &domain.FindingFields{File: "a.go"}}},
		{"reversed lines", domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentFinding, Ref: "gh-pr-34",
			FindingFields: &domain.FindingFields{Fingerprint: "abc", StartLine: 5, EndLine: 4}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.m.Validate(); err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}
