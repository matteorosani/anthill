package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

var t0 = time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC)

const (
	instanceA = "mrosani@LAPTOP-42"
	instanceB = "lbianchi@DESKTOP-7"
)

var writers = domain.NewUserSet("mrosani", "lbianchi")

// claimComment returns a comment holding a claim, created at created and
// last edited at updated.
func claimComment(id, author string, c domain.Claim, created, updated time.Time) domain.Comment {
	if c.Lease == 0 {
		c.Lease = domain.DefaultLease
	}
	if c.Kind == "" {
		c.Kind = domain.ClaimTask
	}
	if c.Ref == "" {
		c.Ref = "gh-12"
	}
	return domain.Comment{
		ID:        id,
		Author:    author,
		Body:      c.Comment(updated.Add(c.Lease)).String(),
		CreatedAt: created,
		UpdatedAt: updated,
	}
}

func TestClaimStatus(t *testing.T) {
	tests := []struct {
		name  string
		claim domain.Claim
		now   time.Time
		want  domain.ClaimStatus
	}{
		{"within the lease", domain.Claim{UpdatedAt: t0, Lease: 15 * time.Minute}, t0.Add(14 * time.Minute), domain.ClaimLive},
		{"machine goes offline: lease ran out", domain.Claim{UpdatedAt: t0, Lease: 15 * time.Minute}, t0.Add(16 * time.Minute), domain.ClaimExpired},
		{"expires exactly at the end of the lease", domain.Claim{UpdatedAt: t0, Lease: 15 * time.Minute}, t0.Add(15 * time.Minute), domain.ClaimExpired},
		{"renewed by editing the comment", domain.Claim{CreatedAt: t0, UpdatedAt: t0.Add(30 * time.Minute), Lease: 15 * time.Minute}, t0.Add(40 * time.Minute), domain.ClaimLive},
		{"parked claim never expires", domain.Claim{UpdatedAt: t0, Lease: 15 * time.Minute, Parked: true}, t0.Add(7 * 24 * time.Hour), domain.ClaimParked},
		{"released claim", domain.Claim{UpdatedAt: t0, Lease: 15 * time.Minute, Released: true}, t0, domain.ClaimReleased},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.claim.Status(tt.now); got != tt.want {
				t.Errorf("Status() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveClaim(t *testing.T) {
	item := ghItem(12)
	tests := []struct {
		name     string
		comments []domain.Comment
		kind     domain.ClaimKind
		now      time.Time
		want     string // winning instance, "" for no claim
	}{
		{
			name: "two instances claim simultaneously: the earliest comment wins",
			comments: []domain.Comment{
				claimComment("101", "lbianchi", domain.Claim{Instance: instanceB}, t0.Add(time.Second), t0.Add(time.Second)),
				claimComment("100", "mrosani", domain.Claim{Instance: instanceA}, t0, t0),
			},
			now:  t0.Add(5 * time.Second),
			want: instanceA,
		},
		{
			name: "same creation time: the lower comment id wins, compared as numbers",
			comments: []domain.Comment{
				claimComment("100", "lbianchi", domain.Claim{Instance: instanceB}, t0, t0),
				claimComment("99", "mrosani", domain.Claim{Instance: instanceA}, t0, t0),
			},
			now:  t0,
			want: instanceA,
		},
		{
			name: "same developer, two machines compete like any two instances",
			comments: []domain.Comment{
				claimComment("100", "mrosani", domain.Claim{Instance: "mrosani@DESKTOP-1"}, t0, t0),
				claimComment("101", "mrosani", domain.Claim{Instance: instanceA}, t0.Add(time.Second), t0.Add(time.Second)),
			},
			now:  t0,
			want: "mrosani@DESKTOP-1",
		},
		{
			name: "forged claim marker by a user without write access",
			comments: []domain.Comment{
				claimComment("100", "outsider", domain.Claim{Instance: "outsider@PC"}, t0, t0),
				claimComment("101", "mrosani", domain.Claim{Instance: instanceA}, t0.Add(time.Second), t0.Add(time.Second)),
			},
			now:  t0,
			want: instanceA,
		},
		{
			name: "write access compares logins without case",
			comments: []domain.Comment{
				claimComment("100", "MRosani", domain.Claim{Instance: instanceA}, t0, t0),
			},
			now:  t0,
			want: instanceA,
		},
		{
			name: "machine goes offline: an expired claim counts as no claim",
			comments: []domain.Comment{
				claimComment("100", "mrosani", domain.Claim{Instance: instanceA}, t0, t0),
			},
			now: t0.Add(16 * time.Minute),
		},
		{
			name: "an expired earlier claim does not beat a live later one",
			comments: []domain.Comment{
				claimComment("100", "mrosani", domain.Claim{Instance: instanceA}, t0, t0),
				claimComment("101", "lbianchi", domain.Claim{Instance: instanceB}, t0.Add(20*time.Minute), t0.Add(20*time.Minute)),
			},
			now:  t0.Add(25 * time.Minute),
			want: instanceB,
		},
		{
			name: "ant finished, PR open: the parked claim stays live",
			comments: []domain.Comment{
				claimComment("100", "mrosani", domain.Claim{Instance: instanceA, Parked: true}, t0, t0),
			},
			now:  t0.Add(7 * 24 * time.Hour),
			want: instanceA,
		},
		{
			name: "queen abandons an ant: the released claim counts as no claim",
			comments: []domain.Comment{
				claimComment("100", "mrosani", domain.Claim{Instance: instanceA, Released: true}, t0, t0),
			},
			now: t0,
		},
		{
			name: "different machine reviews the PR: a review claim on the PR is not a task claim on the item",
			comments: []domain.Comment{
				claimComment("100", "lbianchi", domain.Claim{Instance: instanceB, Kind: domain.ClaimReview, Ref: "gh-pr-34"}, t0, t0),
			},
			now: t0,
		},
		{
			name: "a claim for another item is ignored",
			comments: []domain.Comment{
				claimComment("100", "mrosani", domain.Claim{Instance: instanceA, Ref: "gh-13"}, t0, t0),
			},
			now: t0,
		},
		{
			name: "human comments are ignored",
			comments: []domain.Comment{
				{ID: "100", Author: "mrosani", Body: "I'll take this one", CreatedAt: t0, UpdatedAt: t0},
			},
			now: t0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind := tt.kind
			if kind == "" {
				kind = domain.ClaimTask
			}
			got, found := domain.ResolveClaim(tt.comments, item, kind, writers, tt.now)
			if found != (tt.want != "") || got.Instance != tt.want {
				t.Errorf("ResolveClaim() = %q, %v; want %q", got.Instance, found, tt.want)
			}
		})
	}
}

func TestReviewClaimIndependentOfTaskClaim(t *testing.T) {
	pr := domain.Ref{Repo: ghRepo, Kind: domain.KindPR, Number: 34}
	comments := []domain.Comment{
		claimComment("200", "lbianchi", domain.Claim{Instance: instanceB, Kind: domain.ClaimReview, Ref: "gh-pr-34"}, t0, t0),
	}
	got, found := domain.ResolveClaim(comments, pr, domain.ClaimReview, writers, t0)
	if !found || got.Instance != instanceB {
		t.Errorf("review claim = %q, %v; want %q", got.Instance, found, instanceB)
	}
	if _, found := domain.ResolveClaim(comments, pr, domain.ClaimTask, writers, t0); found {
		t.Error("a review claim was reported as a task claim")
	}
}

func TestTwoInstancesClaimSimultaneouslyLoserLoses(t *testing.T) {
	comments := []domain.Comment{
		claimComment("100", "mrosani", domain.Claim{Instance: instanceA}, t0, t0),
		claimComment("101", "lbianchi", domain.Claim{Instance: instanceB}, t0.Add(time.Second), t0.Add(time.Second)),
	}
	winner, found := domain.ResolveClaim(comments, ghItem(12), domain.ClaimTask, writers, t0.Add(3*time.Second))
	if domain.ClaimLost(winner, found, instanceA) {
		t.Error("A lost its claim, want A to hold it")
	}
	if !domain.ClaimLost(winner, found, instanceB) {
		t.Error("B holds the claim, want B to have lost the race")
	}
}

func TestHolderReturnsAfterExpiry(t *testing.T) {
	// A's lease expired while its laptop slept, and B claimed #12.
	comments := []domain.Comment{
		claimComment("100", "mrosani", domain.Claim{Instance: instanceA}, t0, t0),
		claimComment("101", "lbianchi", domain.Claim{Instance: instanceB}, t0.Add(30*time.Minute), t0.Add(30*time.Minute)),
	}
	now := t0.Add(2 * time.Hour)
	winner, found := domain.ResolveClaim(comments, ghItem(12), domain.ClaimTask, writers, now)
	if !domain.ClaimLost(winner, found, instanceA) {
		t.Fatal("A still holds the claim, want it lost")
	}
	ant, err := domain.NewAnt(ghItem(12), domain.AntStarting, time.Hour, domain.DefaultBudgetPolicy(), t0, "accepted")
	if err != nil {
		t.Fatal(err)
	}
	if err := ant.Transition(domain.AntRunning, t0.Add(time.Minute), "session started"); err != nil {
		t.Fatal(err)
	}
	if err := ant.Transition(domain.AntAbandoned, now, "claim lost"); err != nil {
		t.Fatalf("Transition(abandoned) = %v", err)
	}
	if h := ant.History(); h[len(h)-1].Reason != "claim lost" {
		t.Errorf("last reason = %q, want %q", h[len(h)-1].Reason, "claim lost")
	}
}

func TestClaimVisibleToHumans(t *testing.T) {
	claim := domain.Claim{Kind: domain.ClaimTask, Instance: instanceA, Ref: "gh-12", Lease: domain.DefaultLease}
	text := claim.Comment(time.Date(2026, 10, 29, 11, 30, 0, 0, time.UTC)).String()
	for _, want := range []string{"🐜 Ant (mrosani@LAPTOP-42)", "Claimed by mrosani@LAPTOP-42 until 11:30 UTC"} {
		if !strings.Contains(text, want) {
			t.Errorf("claim comment %q does not contain %q", text, want)
		}
	}
	got, ok := domain.ParseClaim(domain.Comment{ID: "1", Author: "mrosani", Body: text, CreatedAt: t0, UpdatedAt: t0})
	if !ok {
		t.Fatalf("ParseClaim(%q) found no claim", text)
	}
	if got.Instance != instanceA || got.Kind != domain.ClaimTask || got.Ref != "gh-12" || got.Lease != domain.DefaultLease {
		t.Errorf("ParseClaim() = %+v", got)
	}
	if !got.ExpiresAt().Equal(t0.Add(15 * time.Minute)) {
		t.Errorf("ExpiresAt() = %s, want the comment's provider timestamp plus 15 minutes", got.ExpiresAt())
	}
}

func TestClaimCommentText(t *testing.T) {
	until := time.Date(2026, 10, 29, 11, 30, 0, 0, time.UTC)
	tests := []struct {
		name  string
		claim domain.Claim
		want  string
	}{
		{"parked", domain.Claim{Kind: domain.ClaimTask, Instance: instanceA, Ref: "gh-12", Lease: domain.DefaultLease, Parked: true}, "Claimed by mrosani@LAPTOP-42 (parked"},
		{"released", domain.Claim{Kind: domain.ClaimTask, Instance: instanceA, Ref: "gh-12", Lease: domain.DefaultLease, Released: true}, "Released by mrosani@LAPTOP-42."},
		{"review claim by the shepherd", domain.Claim{Kind: domain.ClaimReview, Instance: instanceA, Ref: "gh-pr-34", Lease: domain.DefaultLease}, "🐜 Shepherd (mrosani@LAPTOP-42)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.claim.Comment(until)
			if text := c.String(); !strings.Contains(text, tt.want) {
				t.Errorf("comment %q does not contain %q", text, tt.want)
			}
			if err := c.Marker.Validate(); err != nil {
				t.Errorf("marker is not valid: %v", err)
			}
		})
	}
}
