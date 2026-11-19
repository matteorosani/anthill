package app

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
)

// ItemHolds tells whether an item carries its repository's hold label,
// read fresh from the tracker (spec change-reaction, "Hold freezes the
// ant"). Reading the label when a run would start, rather than keeping a
// local copy, means a label changed between polls or while the daemon was
// down is never missed.
type ItemHolds struct {
	tracker TrackerRoute
	route   WorkspaceRoute
}

// NewItemHolds returns the hold check over the item's tracker, with the
// hold label from the item's settings.
func NewItemHolds(tracker TrackerRoute, route WorkspaceRoute) *ItemHolds {
	return &ItemHolds{tracker: tracker, route: route}
}

// OnHold reports whether the item carries the hold label.
func (h *ItemHolds) OnHold(ctx context.Context, item domain.Ref) (bool, error) {
	ws, err := h.route(ctx, item)
	if err != nil {
		return false, fmt.Errorf("finding the settings of %s: %w", item, err)
	}
	tracker, err := h.tracker(ctx, item)
	if err != nil {
		return false, fmt.Errorf("reaching the tracker of %s: %w", item, err)
	}
	wi, _, err := tracker.Item(ctx, item)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", item, err)
	}
	return slices.ContainsFunc(wi.Labels, func(l string) bool { return strings.EqualFold(l, ws.Settings.HoldLabel) }), nil
}
