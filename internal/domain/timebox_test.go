package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestBudgetDeadline(t *testing.T) {
	ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
	b := &ant.Budget
	if out, ok := b.RunsOutAt(); !ok || !out.Equal(at(60)) {
		t.Errorf("RunsOutAt = %v, %v; want at(60)", out, ok)
	}
	if got := b.Deadline(); !got.Equal(at(63)) {
		t.Errorf("Deadline = %v, want at(63): the budget plus the grace period", got)
	}

	// Waiting for an extension pauses the budget: no deadline, and the
	// end is not known.
	mustTransition(t, ant, domain.AntAwaitingExtension, at(30))
	if got := b.Deadline(); !got.IsZero() {
		t.Errorf("Deadline while waiting = %v, want none", got)
	}
	if _, ok := b.RunsOutAt(); ok {
		t.Error("RunsOutAt while paused = true")
	}
	mustTransition(t, ant, domain.AntRunning, at(40))
	if got := b.Deadline(); !got.Equal(at(73)) {
		t.Errorf("Deadline after a 10-minute wait = %v, want at(73)", got)
	}

	// Once the budget has run out, the deadline stays at that moment
	// plus the grace period.
	mustTransition(t, ant, domain.AntAwaitingExtension, at(75))
	if out, ok := b.RunsOutAt(); !ok || !out.Equal(at(70)) {
		t.Errorf("RunsOutAt after running out = %v, %v; want at(70)", out, ok)
	}
	mustTransition(t, ant, domain.AntRunning, at(76))
	if got := b.Deadline(); !got.Equal(at(73)) {
		t.Errorf("Deadline = %v, want at(73)", got)
	}
}

func TestBudgetWarningAt(t *testing.T) {
	ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
	b := &ant.Budget
	if w, ok := b.WarningAt(); !ok || !w.Equal(at(48)) {
		t.Errorf("WarningAt = %v, %v; want at(48)", w, ok)
	}
	mustTransition(t, ant, domain.AntAwaitingExtension, at(50))
	if _, ok := b.WarningAt(); ok {
		t.Error("WarningAt while paused = true")
	}
	mustTransition(t, ant, domain.AntRunning, at(55))
	if w, ok := b.WarningAt(); !ok || !w.Equal(at(55)) {
		t.Errorf("WarningAt past the threshold = %v, %v; want at(55), now", w, ok)
	}
	b.MarkWarned()
	if _, ok := b.WarningAt(); ok {
		t.Error("WarningAt once warned = true")
	}

	off := domain.DefaultBudgetPolicy()
	off.WarningPercent = 0
	quiet := runningAnt(t, time.Hour, off)
	if _, ok := quiet.Budget.WarningAt(); ok {
		t.Error("WarningAt with the warning off = true")
	}
}

func TestQueenActionNotes(t *testing.T) {
	if got := domain.StopNotice(3 * time.Minute); !strings.Contains(got, "commit your work in progress") || !strings.Contains(got, "3 minutes") {
		t.Errorf("StopNotice = %q", got)
	}
	if got := domain.RetryNote(domain.AntFailed, "exited without finish"); !strings.Contains(got, "failed (exited without finish)") {
		t.Errorf("RetryNote(failed) = %q", got)
	}
	if got := domain.RetryNote(domain.AntStopped, "stopped by the queen"); !strings.Contains(got, "was stopped") {
		t.Errorf("RetryNote(stopped) = %q", got)
	}
	if got := domain.RetryNote(domain.AntRunning, ""); got != "" {
		t.Errorf("RetryNote(running) = %q, want none", got)
	}
	if got := domain.ContinueNote(30 * time.Minute); !strings.Contains(got, "new budget of 30 minutes") {
		t.Errorf("ContinueNote = %q", got)
	}
}
