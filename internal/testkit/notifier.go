package testkit

import (
	"context"
	"sync"

	"github.com/matteorosani/anthill/internal/ports"
)

// Notifier is a ports.Notifier that records notifications. When Err is
// set, Notify returns it and records nothing.
type Notifier struct {
	mu   sync.Mutex
	Err  error
	sent []ports.Notification
}

var _ ports.Notifier = (*Notifier)(nil)

// Notify records n.
func (n *Notifier) Notify(_ context.Context, note ports.Notification) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.Err != nil {
		return n.Err
	}
	n.sent = append(n.sent, note)
	return nil
}

// Sent returns the notifications recorded so far, in order.
func (n *Notifier) Sent() []ports.Notification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]ports.Notification(nil), n.sent...)
}

// Kinds returns the kind of each recorded notification, in order.
func (n *Notifier) Kinds() []ports.NotificationKind {
	n.mu.Lock()
	defer n.mu.Unlock()
	kinds := make([]ports.NotificationKind, len(n.sent))
	for i, s := range n.sent {
		kinds[i] = s.Kind
	}
	return kinds
}
