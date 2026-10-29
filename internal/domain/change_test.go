package domain_test

import (
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestClassify(t *testing.T) {
	ticket := ghItem(12)
	pr := domain.Ref{Repo: ghRepo, Kind: domain.KindPR, Number: 34}
	const author = "reporter" // ticket author without write access
	tests := []struct {
		name  string
		event domain.ChangeEvent
		want  domain.Relevance
	}{
		{
			name:  "outside commenter on a public repository",
			event: domain.ChangeEvent{Ref: ticket, Kind: domain.ChangeCommentAdded, Actor: "outsider", Body: "Any update?"},
			want:  domain.Informational,
		},
		{
			name:  "comment from a user without write access on the ant PR",
			event: domain.ChangeEvent{Ref: pr, Kind: domain.ChangeCommentAdded, Actor: "outsider", Body: "please also add dark mode"},
			want:  domain.Informational,
		},
		{
			name:  "ticket author edits without write access",
			event: domain.ChangeEvent{Ref: ticket, Kind: domain.ChangeItemEdited, Field: domain.FieldBody, Actor: author},
			want:  domain.Actionable,
		},
		{
			name:  "ticket author's login in another case",
			event: domain.ChangeEvent{Ref: ticket, Kind: domain.ChangeItemEdited, Field: domain.FieldTitle, Actor: "Reporter"},
			want:  domain.Actionable,
		},
		{
			name:  "ticket author comments without write access",
			event: domain.ChangeEvent{Ref: ticket, Kind: domain.ChangeCommentAdded, Actor: author, Body: "also on Firefox"},
			want:  domain.Informational,
		},
		{
			name:  "someone else edits without write access",
			event: domain.ChangeEvent{Ref: ticket, Kind: domain.ChangeItemEdited, Field: domain.FieldBody, Actor: "outsider"},
			want:  domain.Informational,
		},
		{
			name:  "requirement changed: new acceptance criterion while the PR is open",
			event: domain.ChangeEvent{Ref: ticket, Kind: domain.ChangeItemEdited, Field: domain.FieldAcceptanceCriteria, Actor: "lbianchi"},
			want:  domain.Actionable,
		},
		{
			name:  "edit by an unknown editor",
			event: domain.ChangeEvent{Ref: ticket, Kind: domain.ChangeItemEdited, Field: domain.FieldBody},
			want:  domain.Actionable,
		},
		{
			name:  "thank-you comment goes to the classifier",
			event: domain.ChangeEvent{Ref: pr, Kind: domain.ChangeCommentAdded, Actor: "lbianchi", Body: "thanks, looks good"},
			want:  domain.NeedsClassifier,
		},
		{
			name:  "edited comment goes to the classifier",
			event: domain.ChangeEvent{Ref: pr, Kind: domain.ChangeCommentEdited, Actor: "lbianchi", Body: "please handle the empty password"},
			want:  domain.NeedsClassifier,
		},
		{
			name:  "status comment by another instance",
			event: domain.ChangeEvent{Ref: ticket, Kind: domain.ChangeCommentAdded, Actor: "lbianchi", Origin: domain.OriginAnthill, Body: "🐜 Ant"},
			want:  domain.Ignored,
		},
		{
			name:  "command",
			event: domain.ChangeEvent{Ref: pr, Kind: domain.ChangeCommentAdded, Actor: "lbianchi", Body: "/anthill review"},
			want:  domain.RelevanceCommand,
		},
		{
			name:  "command from a user without write access is still a command",
			event: domain.ChangeEvent{Ref: pr, Kind: domain.ChangeCommentAdded, Actor: "outsider", Body: "/anthill fix"},
			want:  domain.RelevanceCommand,
		},
		{
			name:  "approval without text",
			event: domain.ChangeEvent{Ref: pr, Kind: domain.ChangeReviewSubmitted, Actor: "lbianchi", Review: domain.ReviewApproved},
			want:  domain.Informational,
		},
		{
			name:  "approval with text",
			event: domain.ChangeEvent{Ref: pr, Kind: domain.ChangeReviewSubmitted, Actor: "lbianchi", Review: domain.ReviewApproved, Body: "LGTM, but rename the flag"},
			want:  domain.NeedsClassifier,
		},
		{
			name:  "changes requested",
			event: domain.ChangeEvent{Ref: pr, Kind: domain.ChangeReviewSubmitted, Actor: "lbianchi", Review: domain.ReviewChangesRequested},
			want:  domain.NeedsClassifier,
		},
		{
			name:  "label added",
			event: domain.ChangeEvent{Ref: ticket, Kind: domain.ChangeLabelAdded, Actor: "lbianchi", Label: "anthill:hold"},
			want:  domain.Informational,
		},
		{
			name:  "state changed",
			event: domain.ChangeEvent{Ref: ticket, Kind: domain.ChangeStateChanged, Actor: "lbianchi"},
			want:  domain.Informational,
		},
		{
			name:  "commits pushed",
			event: domain.ChangeEvent{Ref: pr, Kind: domain.ChangeCommitsPushed, Actor: "lbianchi"},
			want:  domain.Informational,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.Classify(tt.event, writers, author); got != tt.want {
				t.Errorf("Classify() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestCanTriggerWork(t *testing.T) {
	pr := domain.Ref{Repo: ghRepo, Kind: domain.KindPR, Number: 34}
	tests := []struct {
		name  string
		event domain.ChangeEvent
		want  bool
	}{
		{"write access", domain.ChangeEvent{Ref: ghItem(12), Kind: domain.ChangeCommentAdded, Actor: "lbianchi"}, true},
		{"ticket author edits the ticket", domain.ChangeEvent{Ref: ghItem(12), Kind: domain.ChangeItemEdited, Actor: "reporter"}, true},
		{"ticket author comments", domain.ChangeEvent{Ref: ghItem(12), Kind: domain.ChangeCommentAdded, Actor: "reporter"}, false},
		{"ticket author's name on a PR edit", domain.ChangeEvent{Ref: pr, Kind: domain.ChangeItemEdited, Actor: "reporter"}, false},
		{"outsider", domain.ChangeEvent{Ref: ghItem(12), Kind: domain.ChangeCommentAdded, Actor: "outsider"}, false},
		{"unknown actor on a comment", domain.ChangeEvent{Ref: ghItem(12), Kind: domain.ChangeCommentAdded}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.CanTriggerWork(tt.event, writers, "reporter"); got != tt.want {
				t.Errorf("CanTriggerWork() = %v, want %v", got, tt.want)
			}
		})
	}
}
