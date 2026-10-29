package ports_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/ports"
)

func TestRateLimitError(t *testing.T) {
	reset := time.Date(2026, 10, 1, 10, 42, 0, 0, time.UTC)
	err := fmt.Errorf("poll github: %w", &ports.RateLimitError{Reset: reset})
	if !errors.Is(err, ports.ErrRateLimited) {
		t.Error("a RateLimitError is not ErrRateLimited")
	}
	var limited *ports.RateLimitError
	if !errors.As(err, &limited) || !limited.Reset.Equal(reset) {
		t.Errorf("errors.As = %+v", limited)
	}
	if got := err.Error(); got != "poll github: rate limited until 2026-10-01T10:42:00Z" {
		t.Errorf("Error() = %q", got)
	}
	if errors.Is(err, ports.ErrConflict) {
		t.Error("a RateLimitError is ErrConflict")
	}
}
