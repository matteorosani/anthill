package app

import (
	"context"

	"github.com/matteorosani/anthill/internal/ports"
)

// Notifications is the ports.Notifier the application services use. It
// passes a notification on to the desktop notifier unless the
// notifications setting turns its kind off (spec queen-console,
// "Notifications"). It reads the configuration in use on each call, so a
// reload applies to the next notification.
type Notifications struct {
	config *Configuration
	next   ports.Notifier
}

var _ ports.Notifier = (*Notifications)(nil)

// NewNotifications returns a notifier that filters by config and shows
// what passes with next.
func NewNotifications(config *Configuration, next ports.Notifier) *Notifications {
	return &Notifications{config: config, next: next}
}

// Notify shows n, or drops it when its kind is turned off. Before the
// configuration is loaded, every kind is on.
func (s *Notifications) Notify(ctx context.Context, n ports.Notification) error {
	if snap := s.config.Current(); snap != nil && !snap.Global.NotificationOn(n.Kind) {
		return nil
	}
	return s.next.Notify(ctx, n)
}
