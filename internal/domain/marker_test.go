package domain_test

import (
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

const instance = "mrosani@LAPTOP-42"

func TestMarkerRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		marker domain.Marker
	}{
		{"status", domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentStatus, Ref: "gh-12"}},
		{"question with id", domain.Marker{Version: 1, ID: "run-7/q1", Instance: instance, Kind: domain.CommentQuestion, Ref: "gh-12"}},
		{"finding on a pr", domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentFinding, Ref: "ado-pr-34"}},
		{"task claim", domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentClaim, Ref: "gh-12",
			ClaimFields: &domain.ClaimFields{Claim: domain.ClaimTask, LeaseMinutes: 15}}},
		{"parked claim", domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentClaim, Ref: "gh-12",
			ClaimFields: &domain.ClaimFields{Claim: domain.ClaimTask, LeaseMinutes: 15, Parked: true}}},
		{"released review claim", domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentClaim, Ref: "gh-pr-34",
			ClaimFields: &domain.ClaimFields{Claim: domain.ClaimReview, LeaseMinutes: 15, Released: true}}},
		{"instance that tries to close the comment", domain.Marker{Version: 1, Instance: "x --> y", Kind: domain.CommentReply, Ref: "gh-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := tt.marker.String()
			if !strings.HasPrefix(text, "<!-- anthill {") || strings.Count(text, "-->") != 1 {
				t.Fatalf("String() = %q, want one hidden HTML comment", text)
			}
			got, ok := domain.FindMarker("Some text\n\n" + text + "\n")
			if !ok {
				t.Fatalf("FindMarker(%q) found no marker", text)
			}
			assertMarker(t, got, tt.marker)
		})
	}
}

func TestMarkerJSON(t *testing.T) {
	m := domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentClaim, Ref: "gh-12",
		ClaimFields: &domain.ClaimFields{Claim: domain.ClaimTask, LeaseMinutes: 15}}
	want := `<!-- anthill {"v":1,"instance":"mrosani@LAPTOP-42","kind":"claim","ref":"gh-12","claim":"task","lease_minutes":15,"parked":false,"released":false} -->`
	if got := m.String(); got != want {
		t.Errorf("String() =\n%s\nwant\n%s", got, want)
	}
	status := domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentStatus, Ref: "gh-12"}
	want = `<!-- anthill {"v":1,"instance":"mrosani@LAPTOP-42","kind":"status","ref":"gh-12"} -->`
	if got := status.String(); got != want {
		t.Errorf("String() =\n%s\nwant\n%s", got, want)
	}
}

func TestTextWithoutMarkerIsHuman(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"developer comments from the same account", "Please also handle the Safari case."},
		{"empty", ""},
		{"other html comment", "<!-- just a note --> hello"},
		{"invalid json", `<!-- anthill {"v":1, -->`},
		{"unknown kind", `<!-- anthill {"v":1,"instance":"a@b","kind":"chat","ref":"gh-12"} -->`},
		{"missing instance", `<!-- anthill {"v":1,"kind":"status","ref":"gh-12"} -->`},
		{"missing ref", `<!-- anthill {"v":1,"instance":"a@b","kind":"status"} -->`},
		{"invalid ref", `<!-- anthill {"v":1,"instance":"a@b","kind":"status","ref":"12"} -->`},
		{"unsupported version", `<!-- anthill {"v":2,"instance":"a@b","kind":"status","ref":"gh-12"} -->`},
		{"claim without lease", `<!-- anthill {"v":1,"instance":"a@b","kind":"claim","ref":"gh-12","claim":"task"} -->`},
		{"claim with unknown claim kind", `<!-- anthill {"v":1,"instance":"a@b","kind":"claim","ref":"gh-12","claim":"other","lease_minutes":15} -->`},
		{"claim fields on a status", `<!-- anthill {"v":1,"instance":"a@b","kind":"status","ref":"gh-12","claim":"task","lease_minutes":15} -->`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.OriginOf(tt.body); got != domain.OriginHuman {
				t.Errorf("OriginOf(%q) = %q, want human", tt.body, got)
			}
			c := domain.Comment{Body: tt.body}
			if got := c.Origin(); got != domain.OriginHuman {
				t.Errorf("Comment.Origin() = %q, want human", got)
			}
		})
	}
}

func TestAnthillCommentIsAnthillAuthored(t *testing.T) {
	body := "Working on it.\n\n" + `<!-- anthill {"v":1,"instance":"a@b","kind":"status","ref":"gh-12"} -->`
	if got := domain.OriginOf(body); got != domain.OriginAnthill {
		t.Errorf("OriginOf = %q, want anthill", got)
	}
}

func TestFirstValidMarkerWins(t *testing.T) {
	body := `<!-- anthill {"v":1,"kind":"status"} --> text ` +
		`<!-- anthill {"v":1,"instance":"a@b","kind":"status","ref":"gh-1"} --> ` +
		`<!-- anthill {"v":1,"instance":"c@d","kind":"reply","ref":"gh-2"} -->`
	m, ok := domain.FindMarker(body)
	if !ok || m.Instance != "a@b" || m.Ref != "gh-1" {
		t.Errorf("FindMarker = %+v, %v; want the marker of a@b", m, ok)
	}
}

func TestAnthillCommentRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		comment domain.AnthillComment
	}{
		{"status comment", domain.AnthillComment{Role: domain.RoleAnt, Body: "Progress: tests added.",
			Marker: domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentStatus, Ref: "gh-12"}}},
		{"shepherd finding with markdown", domain.AnthillComment{Role: domain.RoleShepherd, Body: "**Blocking**\n\n- unused import\n- `x := 1`",
			Marker: domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentFinding, Ref: "gh-pr-34"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := tt.comment.String()
			got, ok := domain.ParseAnthillComment(text)
			if !ok {
				t.Fatalf("ParseAnthillComment(%q) failed", text)
			}
			if got.Role != tt.comment.Role || got.Body != tt.comment.Body {
				t.Errorf("got role %q body %q, want %q %q", got.Role, got.Body, tt.comment.Role, tt.comment.Body)
			}
			assertMarker(t, got.Marker, tt.comment.Marker)
		})
	}
}

func TestStatusCommentPrefix(t *testing.T) {
	// Scenario "Status comment" (agent-conversation).
	c := domain.AnthillComment{Role: domain.RoleAnt, Body: "Progress summary.",
		Marker: domain.Marker{Version: 1, Instance: instance, Kind: domain.CommentStatus, Ref: "gh-12"}}
	text := c.String()
	if !strings.HasPrefix(text, "🐜 Ant (mrosani@LAPTOP-42)") {
		t.Errorf("comment %q does not start with the 🐜 prefix", text)
	}
	m, ok := domain.FindMarker(text)
	if !ok || m.Kind != domain.CommentStatus || m.Ref != "gh-12" {
		t.Errorf("FindMarker = %+v, %v; want a status marker for gh-12", m, ok)
	}
}

func TestParseAnthillCommentWithoutPrefix(t *testing.T) {
	text := "No prefix here.\n\n" + `<!-- anthill {"v":1,"instance":"a@b","kind":"status","ref":"gh-12"} -->`
	if _, ok := domain.ParseAnthillComment(text); ok {
		t.Error("ParseAnthillComment accepted a comment without the 🐜 prefix")
	}
}

func assertMarker(t *testing.T, got, want domain.Marker) {
	t.Helper()
	if (got.ClaimFields == nil) != (want.ClaimFields == nil) {
		t.Fatalf("claim fields = %+v, want %+v", got.ClaimFields, want.ClaimFields)
	}
	if got.ClaimFields != nil && *got.ClaimFields != *want.ClaimFields {
		t.Errorf("claim fields = %+v, want %+v", *got.ClaimFields, *want.ClaimFields)
	}
	got.ClaimFields, want.ClaimFields = nil, nil
	if got != want {
		t.Errorf("marker = %+v, want %+v", got, want)
	}
}
