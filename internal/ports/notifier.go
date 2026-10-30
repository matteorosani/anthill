package ports

import (
	"context"

	"github.com/matteorosani/anthill/internal/domain"
)

// NotificationKind is what a notification is about. Each kind can be
// turned off with the notifications setting (spec queen-console,
// "Notifications"). The kinds live in the domain, so that configuration
// validation can name them.
type NotificationKind = domain.NotificationKind

// The notification kinds, one per event listed in queen-console.
const (
	NotifyProposal         = domain.NotifyProposal
	NotifyQuestion         = domain.NotifyQuestion
	NotifyStaleQuestion    = domain.NotifyStaleQuestion
	NotifyExtension        = domain.NotifyExtension
	NotifyWarning          = domain.NotifyWarning
	NotifySucceeded        = domain.NotifySucceeded
	NotifyFailed           = domain.NotifyFailed
	NotifyTimedOut         = domain.NotifyTimedOut
	NotifyNoChange         = domain.NotifyNoChange
	NotifyFollowUpStarted  = domain.NotifyFollowUpStarted
	NotifyFollowUpProposed = domain.NotifyFollowUpProposed
	NotifyFollowUpLimit    = domain.NotifyFollowUpLimit
	NotifyChangeStored     = domain.NotifyChangeStored
	NotifyClaimLost        = domain.NotifyClaimLost
	NotifyPollFailed       = domain.NotifyPollFailed
)

// NotificationKinds lists every kind.
var NotificationKinds = domain.NotificationKinds()

// Notification is one desktop notification for the queen.
type Notification struct {
	Kind  NotificationKind
	Title string
	Body  string
	// Ref is the item or PR it is about, or nil.
	Ref *domain.Ref
}

// Notifier raises local notifications (design D10). The desktop adapter
// shows every notification it gets; app.Notifications wraps it and drops
// the kinds the notifications setting turns off.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}
