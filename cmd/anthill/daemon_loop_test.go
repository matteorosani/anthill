package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/testkit"
)

// Task 11.6: report logs the error of background work that failed while
// the daemon runs, and none once the daemon is stopping, whatever the
// error, since the stop caused it.
func TestReportSkipsErrorsOnceStopping(t *testing.T) {
	var log testkit.SyncBuffer
	l := &runLoop{logger: slog.New(slog.NewTextHandler(&log, nil))}
	ctx, cancel := context.WithCancel(context.Background())

	l.report(ctx, "polling", errors.New("provider down"))
	l.report(ctx, "nothing", nil)
	cancel()
	l.report(ctx, "listing the ants", context.Canceled)
	l.report(ctx, "reading ant 1", errors.New("sql: interrupted"))

	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `level=ERROR msg="polling failed" error="provider down"`) {
		t.Errorf("log = %q, want only the error from before the stop", log.String())
	}
}
