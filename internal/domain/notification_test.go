package domain_test

import (
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

// queen-console names fifteen events that raise a notification, each
// with its own kind.
func TestNotificationKinds(t *testing.T) {
	kinds := domain.NotificationKinds()
	if len(kinds) != 15 {
		t.Errorf("%d kinds, want 15", len(kinds))
	}
	seen := map[domain.NotificationKind]bool{}
	for _, k := range kinds {
		if !k.Valid() || seen[k] {
			t.Errorf("kind %q is invalid or listed twice", k)
		}
		seen[k] = true
	}
	if domain.NotificationKind("coffee-ready").Valid() {
		t.Error("unknown kind is valid")
	}
}
