package domain_test

import (
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

func roundMarker(kind domain.CommentKind, fields *domain.RoundFields) domain.Marker {
	return domain.Marker{Version: 1, Instance: instance, Kind: kind, Ref: "gh-pr-34", RoundFields: fields}
}

func TestRoundMarker(t *testing.T) {
	requested := roundMarker(domain.CommentSummary, &domain.RoundFields{Head: "c2", Requested: true})
	want := `<!-- anthill {"v":1,"instance":"mrosani@LAPTOP-42","kind":"summary","ref":"gh-pr-34","round_head":"c2","round_requested":true} -->`
	if got := requested.String(); got != want {
		t.Errorf("String() =\n%s\nwant\n%s", got, want)
	}
	for _, m := range []domain.Marker{requested, roundMarker(domain.CommentSummary, &domain.RoundFields{Head: "c1"})} {
		got, ok := domain.FindMarker(m.String())
		if !ok {
			t.Fatalf("FindMarker(%s) found nothing", m)
		}
		assertMarker(t, got, m)
	}
	// An ant's run summary has no round fields.
	run, ok := domain.FindMarker(roundMarker(domain.CommentSummary, nil).String())
	if !ok || run.RoundFields != nil {
		t.Errorf("run summary marker = %+v, want no round fields", run)
	}
	for _, bad := range []domain.Marker{
		roundMarker(domain.CommentStatus, &domain.RoundFields{Head: "c1"}),
		roundMarker(domain.CommentSummary, &domain.RoundFields{}),
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("Validate(%s) = nil, want an error", bad)
		}
	}
}

func TestRoundOf(t *testing.T) {
	comment := func(m domain.Marker) domain.Comment {
		return domain.Comment{Body: domain.AnthillComment{Role: domain.RoleShepherd, Body: "No issues found.", Marker: m}.String()}
	}
	tests := []struct {
		name string
		c    domain.Comment
		want domain.PRRound
		ok   bool
	}{
		{"automatic round", comment(roundMarker(domain.CommentSummary, &domain.RoundFields{Head: "c1"})), domain.PRRound{Head: "c1"}, true},
		{"requested round", comment(roundMarker(domain.CommentSummary, &domain.RoundFields{Head: "c2", Requested: true})), domain.PRRound{Head: "c2", Requested: true}, true},
		{"run summary", comment(roundMarker(domain.CommentSummary, nil)), domain.PRRound{}, false},
		{"human comment", domain.Comment{Body: "round_head c1"}, domain.PRRound{}, false},
	}
	for _, tt := range tests {
		got, ok := domain.RoundOf(tt.c, nil)
		if ok != tt.ok || got.Head != tt.want.Head || got.Requested != tt.want.Requested {
			t.Errorf("%s: RoundOf = %+v, %v, want %+v, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

func TestRoundLimitText(t *testing.T) {
	for _, limit := range []int{0, 3} {
		if text := domain.RoundLimitText(limit); !strings.Contains(text, "`/anthill review`") {
			t.Errorf("RoundLimitText(%d) = %q, want how to request a round", limit, text)
		}
	}
}
