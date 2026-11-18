package domain

import (
	"fmt"
	"time"
)

// The timekeeper's view of a budget (spec ant-timebox; design D21): when
// the warning is due, when the budget runs out, and when the executor
// must end the run.

// RunsOutAt returns when the budget runs out: in the future while it
// counts and has time left, or the moment it ran out. It returns false
// for a paused budget that has not run out, whose end is not known.
func (b *Budget) RunsOutAt() (time.Time, bool) {
	if !b.ExhaustedAt.IsZero() {
		return b.ExhaustedAt, true
	}
	if !b.Counting() {
		return time.Time{}, false
	}
	if b.Used >= b.Total() {
		return b.CountingSince, true
	}
	return b.CountingSince.Add(b.Total() - b.Used), true
}

// Deadline is when the executor must end the run: the moment the budget
// runs out plus the grace period (spec ant-timebox, "Budget exhaustion").
// It returns the zero time, no deadline, for a paused budget, as while
// request_extension waits (design D21).
func (b *Budget) Deadline() time.Time {
	if !b.Counting() {
		return time.Time{}
	}
	t, _ := b.RunsOutAt()
	return t.Add(b.Policy.Grace)
}

// WarningAt returns when the warning threshold is reached, while the
// budget counts and the warning for the current total was not given. It
// returns false when the warning is off, already given, or the budget is
// paused.
func (b *Budget) WarningAt() (time.Time, bool) {
	p := b.Policy.WarningPercent
	if b.Warned || p <= 0 || b.Total() <= 0 || !b.Counting() {
		return time.Time{}, false
	}
	threshold := b.Total() * time.Duration(p) / 100
	if b.Used >= threshold {
		return b.CountingSince, true
	}
	return b.CountingSince.Add(threshold - b.Used), true
}

// StopNotice is the message delivered to the ant when its budget runs
// out: it is told to stop and commit its work within the grace period
// (spec ant-timebox, "Budget exhaustion").
func StopNotice(grace time.Duration) string {
	return fmt.Sprintf("Your time budget is used up: stop and commit your work in progress now. The run ends in %s.", FormatRemaining(grace))
}

// RetryNote is the note given to the run of a retry, before its input,
// about how the previous run ended (spec ant-execution, "Retry after
// crash"). reason is the reason of the previous run's end.
func RetryNote(from AntState, reason string) string {
	switch from {
	case AntFailed:
		if reason == "" {
			return "Your previous run failed. The queen retried it: carry on with the work."
		}
		return fmt.Sprintf("Your previous run failed (%s). The queen retried it: carry on with the work.", reason)
	case AntStopped:
		return "Your previous run was stopped. The queen retried it: carry on with the work."
	}
	return ""
}

// ContinueNote is the note given to the run of a continue, before its
// input: the budget ran out and the queen gave a new one (spec
// ant-timebox, "Queen continues a timed-out ant").
func ContinueNote(budget time.Duration) string {
	return fmt.Sprintf("Your previous run used up its time budget. The queen gave you a new budget of %s: carry on with the work.", FormatRemaining(budget))
}
