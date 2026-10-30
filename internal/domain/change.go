package domain

import (
	"strings"
	"time"
)

// ChangeKind is what kind of change a change event records.
type ChangeKind string

// The change kinds.
const (
	ChangeItemEdited      ChangeKind = "item-edited"
	ChangeCommentAdded    ChangeKind = "comment-added"
	ChangeCommentEdited   ChangeKind = "comment-edited"
	ChangeReviewSubmitted ChangeKind = "review-submitted"
	ChangeLabelAdded      ChangeKind = "label-added"
	ChangeLabelRemoved    ChangeKind = "label-removed"
	ChangeStateChanged    ChangeKind = "state-changed"
	ChangeCommitsPushed   ChangeKind = "commits-pushed"
	// ChangeItemIngested is an item seen opted in for the first time, or
	// opted in again after losing the opt-in label, so that eligibility
	// evaluates it (spec work-item-sources, "Opt-in label added later").
	ChangeItemIngested ChangeKind = "item-ingested"
	// ChangePRIngested is an open PR seen for the first time.
	ChangePRIngested       ChangeKind = "pr-ingested"
	ChangeAssigneesChanged ChangeKind = "assignees-changed"
	// ChangeLinksChanged covers dependency, parent and child links.
	ChangeLinksChanged ChangeKind = "links-changed"
	// ChangeDraftChanged is a PR marked as draft or ready for review.
	ChangeDraftChanged ChangeKind = "draft-changed"
)

// The State of a ChangeDraftChanged event.
const (
	PRDraft = "draft"
	PRReady = "ready"
)

// ItemField is a field of a ticket whose edits are always actionable.
type ItemField string

// The item fields.
const (
	FieldTitle              ItemField = "title"
	FieldBody               ItemField = "body"
	FieldAcceptanceCriteria ItemField = "acceptance-criteria"
)

// ReviewState is the verdict of a submitted review.
type ReviewState string

// The review states.
const (
	ReviewApproved         ReviewState = "approved"
	ReviewChangesRequested ReviewState = "changes-requested"
	ReviewCommented        ReviewState = "commented"
)

// ChangeEvent is one change on an item or pull request, found by
// comparing polls (design D7).
type ChangeEvent struct {
	Ref  Ref
	Kind ChangeKind
	// Actor is the login that made the change, or "" when the provider
	// does not say.
	Actor  string
	At     time.Time
	Origin Origin
	// Field is set for ChangeItemEdited, with Old and Body holding the
	// field's text before and after the edit, so that the change can be
	// delivered as a diff (spec change-reaction, "Delivery to an active
	// run").
	Field ItemField
	Old   string
	// CommentID and Body are set for comments and reviews. ThreadID is
	// also set for a comment in a PR review thread.
	CommentID string
	ThreadID  string
	Body      string
	// Review is set for ChangeReviewSubmitted.
	Review ReviewState
	// Label is set for label changes.
	Label string
	// State is set for ChangeStateChanged (an ItemState) and for
	// ChangeDraftChanged (PRDraft or PRReady).
	State string
	// Head is the new head commit of a ChangeCommitsPushed event.
	Head string
}

// Relevance is how a change is classified (spec change-reaction).
type Relevance string

// The relevance classes.
const (
	// Actionable: requires code or an answer.
	Actionable Relevance = "actionable"
	// Informational: no action.
	Informational Relevance = "informational"
	// RelevanceCommand: the change holds an /anthill command.
	RelevanceCommand Relevance = "command"
	// NeedsClassifier: a comment the rules cannot decide; a short agent
	// call classifies it.
	NeedsClassifier Relevance = "needs-classifier"
	// Ignored: Anthill made the change, so it triggers no reaction.
	Ignored Relevance = "ignored"
)

// CanTriggerWork applies the "who can trigger work" rule: users with write
// access can, and so can the ticket's author when editing the ticket's
// title, body or acceptance criteria. An edit whose editor is unknown
// counts as allowed, because both providers let only the author or users
// with write or triage access edit those fields.
func CanTriggerWork(e ChangeEvent, writers UserSet, ticketAuthor string) bool {
	if writers.Contains(e.Actor) {
		return true
	}
	if e.Kind == ChangeItemEdited && e.Ref.Kind == KindItem {
		return e.Actor == "" || sameUser(e.Actor, ticketAuthor)
	}
	return false
}

// Classify applies the classification rules to a change, in this order:
//  1. Anthill's own changes are ignored.
//  2. A comment or review holding an /anthill command is a command,
//     whoever wrote it; command authorization then decides whether it
//     runs or gets a reply that permission is missing.
//  3. Changes by someone who cannot trigger work are informational.
//  4. Edits to a title, body or acceptance criteria are actionable.
//  5. Comments and reviews with text need the classifier; an approval
//     without text is informational.
//  6. Label, state and push changes are informational here; hold,
//     withdrawal, closing and new commits have their own rules.
func Classify(e ChangeEvent, writers UserSet, ticketAuthor string) Relevance {
	if e.Origin == OriginAnthill {
		return Ignored
	}
	hasText := e.Kind == ChangeCommentAdded || e.Kind == ChangeCommentEdited || e.Kind == ChangeReviewSubmitted
	if hasText && len(ParseCommands(e.Body)) > 0 {
		return RelevanceCommand
	}
	if !CanTriggerWork(e, writers, ticketAuthor) {
		return Informational
	}
	switch e.Kind {
	case ChangeItemEdited:
		return Actionable
	case ChangeCommentAdded, ChangeCommentEdited:
		return NeedsClassifier
	case ChangeReviewSubmitted:
		if e.Review == ReviewApproved && strings.TrimSpace(e.Body) == "" {
			return Informational
		}
		return NeedsClassifier
	}
	return Informational
}
