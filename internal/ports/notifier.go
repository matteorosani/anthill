package ports

import (
	"context"

	"github.com/matteorosani/anthill/internal/domain"
)

// NotificationKind is what a notification is about. Each kind can be
// turned off (spec queen-console, "Notifications").
type NotificationKind string

// The notification kinds, one per event listed in queen-console.
const (
	NotifyProposal         NotificationKind = "proposal"
	NotifyQuestion         NotificationKind = "question"
	NotifyStaleQuestion    NotificationKind = "stale-question"
	NotifyExtension        NotificationKind = "extension-request"
	NotifyWarning          NotificationKind = "warning"
	NotifySucceeded        NotificationKind = "succeeded"
	NotifyFailed           NotificationKind = "failed"
	NotifyTimedOut         NotificationKind = "timed-out"
	NotifyNoChange         NotificationKind = "no-change"
	NotifyFollowUpStarted  NotificationKind = "follow-up-started"
	NotifyFollowUpProposed NotificationKind = "follow-up-proposed"
	NotifyFollowUpLimit    NotificationKind = "follow-up-limit"
	NotifyChangeStored     NotificationKind = "change-stored"
	NotifyClaimLost        NotificationKind = "claim-lost"
	NotifyPollFailed       NotificationKind = "poll-failed"
)

// NotificationKinds lists every kind.
var NotificationKinds = []NotificationKind{
	NotifyProposal, NotifyQuestion, NotifyStaleQuestion, NotifyExtension, NotifyWarning,
	NotifySucceeded, NotifyFailed, NotifyTimedOut, NotifyNoChange,
	NotifyFollowUpStarted, NotifyFollowUpProposed, NotifyFollowUpLimit,
	NotifyChangeStored, NotifyClaimLost, NotifyPollFailed,
}

// Notification is one desktop notification for the queen.
type Notification struct {
	Kind  NotificationKind
	Title string
	Body  string
	// Ref is the item or PR it is about, or nil.
	Ref *domain.Ref
}

// Notifier raises local notifications (design D10). Whether a kind is
// turned off is decided by the caller.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}
