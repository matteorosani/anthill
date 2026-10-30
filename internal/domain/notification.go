package domain

// NotificationKind is what a desktop notification for the queen is about.
// Each kind can be turned off with the notifications setting (spec
// queen-console, "Notifications").
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

// notificationKinds lists every kind, in the order of queen-console.
var notificationKinds = []NotificationKind{
	NotifyProposal, NotifyQuestion, NotifyStaleQuestion, NotifyExtension, NotifyWarning,
	NotifySucceeded, NotifyFailed, NotifyTimedOut, NotifyNoChange,
	NotifyFollowUpStarted, NotifyFollowUpProposed, NotifyFollowUpLimit,
	NotifyChangeStored, NotifyClaimLost, NotifyPollFailed,
}

// NotificationKinds returns every kind, in the order of queen-console.
func NotificationKinds() []NotificationKind {
	return append([]NotificationKind(nil), notificationKinds...)
}

// Valid reports whether k is a known kind.
func (k NotificationKind) Valid() bool {
	for _, known := range notificationKinds {
		if k == known {
			return true
		}
	}
	return false
}
