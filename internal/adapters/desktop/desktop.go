// Package desktop shows notifications for the queen on the desktop with
// beeep: the notification centre on macOS, a toast on Windows and the
// D-Bus notification service on Linux. It implements ports.Notifier.
package desktop

import (
	"context"
	"log/slog"

	"github.com/gen2brain/beeep"

	"github.com/matteorosani/anthill/internal/ports"
)

// AppName is the application name the desktop shows with each
// notification.
const AppName = "Anthill"

// Notifier shows every notification it gets. Which kinds are turned off
// is decided before, by app.Notifications.
type Notifier struct {
	// show displays one notification. It is beeep.Notify, or a fake in
	// tests so that they never pop a real notification.
	show   func(title, message string, icon any) error
	logger *slog.Logger
}

var _ ports.Notifier = (*Notifier)(nil)

// New returns a notifier that shows notifications with beeep and logs
// those it cannot show to logger.
func New(logger *slog.Logger) *Notifier {
	beeep.AppName = AppName
	return &Notifier{show: beeep.Notify, logger: logger}
}

// Notify shows n. A notification that cannot be shown is logged and
// returned as an error, and never retried: the queen still finds the
// event in the console.
func (d *Notifier) Notify(_ context.Context, n ports.Notification) error {
	if err := d.show(n.Title, n.Body, ""); err != nil {
		attrs := []any{"kind", string(n.Kind), "error", err}
		if n.Ref != nil {
			attrs = append(attrs, "item", n.Ref.String())
		}
		d.logger.Warn("desktop notification not shown", attrs...)
		return err
	}
	return nil
}
