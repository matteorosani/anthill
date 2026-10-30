package testkit

import (
	"context"
	"sync"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// ClaimLoss is one call of ClaimLossHandler.
type ClaimLoss struct {
	Ref    domain.Ref
	Kind   domain.ClaimKind
	Reason string
}

// ClaimLossHandler is a ports.ClaimLossHandler that records its calls.
// When Err is set, ClaimLost returns it and records nothing.
type ClaimLossHandler struct {
	mu     sync.Mutex
	Err    error
	losses []ClaimLoss
}

var _ ports.ClaimLossHandler = (*ClaimLossHandler)(nil)

// ClaimLost records the loss.
func (h *ClaimLossHandler) ClaimLost(_ context.Context, ref domain.Ref, kind domain.ClaimKind, reason string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.Err != nil {
		return h.Err
	}
	h.losses = append(h.losses, ClaimLoss{Ref: ref, Kind: kind, Reason: reason})
	return nil
}

// Losses returns the recorded losses, in order.
func (h *ClaimLossHandler) Losses() []ClaimLoss {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ClaimLoss(nil), h.losses...)
}
