package domain

import (
	"errors"
	"fmt"
	"time"
)

// The budget defaults (spec anthill-configuration).
const (
	DefaultBaseBudget      = 60 * time.Minute
	DefaultWarningPercent  = 80
	DefaultCeilingFactor   = 3
	DefaultGracePeriod     = 3 * time.Minute
	DefaultFollowUpPercent = 50
)

// BudgetSource tells where a base budget came from.
type BudgetSource string

// The budget sources, from the strongest to the weakest.
const (
	BudgetFromQueen      BudgetSource = "queen"
	BudgetFromLabel      BudgetSource = "label"
	BudgetFromRepository BudgetSource = "repository"
	BudgetFromGlobal     BudgetSource = "global"
	BudgetFromDefault    BudgetSource = "default"
)

// BaseBudget picks the base budget in the order of spec ant-timebox: the
// queen's override at acceptance, the budget: label, the repository
// setting, the global setting, then the default of 60 minutes. Each value
// is in minutes, and 0 means not set. The label must already be capped at
// the maximum base budget, as ParseSettingLabels does; the other sources
// are not capped here.
func BaseBudget(queen, label, repository, global int) (time.Duration, BudgetSource) {
	for _, s := range []struct {
		minutes int
		source  BudgetSource
	}{
		{queen, BudgetFromQueen},
		{label, BudgetFromLabel},
		{repository, BudgetFromRepository},
		{global, BudgetFromGlobal},
	} {
		if s.minutes > 0 {
			return time.Duration(s.minutes) * time.Minute, s.source
		}
	}
	return DefaultBaseBudget, BudgetFromDefault
}

// FollowUpBudget is the budget of a follow-up run: percent of the ant's
// base budget. A percent of 0 or less means DefaultFollowUpPercent.
func FollowUpBudget(base time.Duration, percent int) time.Duration {
	if percent <= 0 {
		percent = DefaultFollowUpPercent
	}
	return base * time.Duration(percent) / 100
}

// ExtensionAllowance is how much a repository lets Anthill approve without
// the queen: requests of at most MaxMinutes, Times times per budget. The
// zero allowance, the built-in default, sends every request to the queen.
type ExtensionAllowance struct {
	MaxMinutes int
	Times      int
}

// BudgetPolicy holds the settings that shape a budget.
type BudgetPolicy struct {
	// WarningPercent is the warning threshold, as a share of the budget.
	// 0 turns the warning off.
	WarningPercent int
	// CeilingFactor is the most a budget can grow to, as a multiple of its
	// initial length. 0 means no ceiling.
	CeilingFactor int
	Grace         time.Duration
	AutoApprove   ExtensionAllowance
}

// DefaultBudgetPolicy returns the built-in settings: a warning at 80%, a
// ceiling of 3× the budget, a 3-minute grace period and no auto-approve.
func DefaultBudgetPolicy() BudgetPolicy {
	return BudgetPolicy{
		WarningPercent: DefaultWarningPercent,
		CeilingFactor:  DefaultCeilingFactor,
		Grace:          DefaultGracePeriod,
	}
}

// Budget is the time an ant may spend in active runs (spec ant-timebox).
// It belongs to the ant and carries over between runs. The ant starts and
// pauses its counting on each transition, so only time in starting,
// running and finishing counts. The fields are exported so the budget can
// be stored; change them only through methods.
type Budget struct {
	Policy BudgetPolicy
	// Initial is the length the budget started with: the base budget, a
	// follow-up budget or a continue.
	Initial time.Duration
	// Extended is the sum of granted extensions.
	Extended time.Duration
	// Used is the time counted until CountingSince, which is zero while
	// the budget is paused.
	Used          time.Duration
	CountingSince time.Time
	// ExhaustedAt records when the budget ran out, once counting paused
	// after that moment. While counting, it is computed instead.
	ExhaustedAt time.Time
	// Warned tells whether the warning for the current total was given.
	Warned bool
	// AutoApproved counts the extensions approved under the allowance.
	AutoApproved int
	// Open is the extension request waiting for the queen, if any.
	Open *ExtensionRequest
}

// NewBudget returns a paused budget of the given length.
func NewBudget(initial time.Duration, policy BudgetPolicy) Budget {
	return Budget{Policy: policy, Initial: initial}
}

// Total is the budget's length with its extensions.
func (b *Budget) Total() time.Duration {
	return b.Initial + b.Extended
}

// Ceiling is the most the total may grow to, or 0 for no ceiling.
func (b *Budget) Ceiling() time.Duration {
	return b.Initial * time.Duration(b.Policy.CeilingFactor)
}

// Counting reports whether the budget is counting.
func (b *Budget) Counting() bool {
	return !b.CountingSince.IsZero()
}

// UsedAt is the time used by now.
func (b *Budget) UsedAt(now time.Time) time.Duration {
	if b.Counting() && now.After(b.CountingSince) {
		return b.Used + now.Sub(b.CountingSince)
	}
	return b.Used
}

// Remaining is the time left at now, never below zero. It answers the
// time_remaining tool.
func (b *Budget) Remaining(now time.Time) time.Duration {
	return max(0, b.Total()-b.UsedAt(now))
}

// exhaustedAt returns when the budget ran out, or false when it has not
// by now.
func (b *Budget) exhaustedAt(now time.Time) (time.Time, bool) {
	if !b.ExhaustedAt.IsZero() {
		return b.ExhaustedAt, true
	}
	if !b.Counting() {
		return time.Time{}, false
	}
	if b.Used >= b.Total() {
		return b.CountingSince, true
	}
	t := b.CountingSince.Add(b.Total() - b.Used)
	return t, !now.Before(t)
}

// Exhausted reports whether the budget is used up at now. Anthill then
// tells the ant to stop and the grace period starts.
func (b *Budget) Exhausted(now time.Time) bool {
	_, ok := b.exhaustedAt(now)
	return ok
}

// GraceOver reports whether the grace period after exhaustion has ended
// by now. The grace period is wall-clock time and is not taken from the
// budget.
func (b *Budget) GraceOver(now time.Time) bool {
	t, ok := b.exhaustedAt(now)
	return ok && !now.Before(t.Add(b.Policy.Grace))
}

// resume starts counting at the given time.
func (b *Budget) resume(at time.Time) {
	if !b.Counting() {
		b.CountingSince = at
	}
}

// pause stops counting at the given time, and records the moment of
// exhaustion if it happened while counting.
func (b *Budget) pause(at time.Time) {
	if !b.Counting() {
		return
	}
	before := b.Used
	b.Used = b.UsedAt(at)
	if b.ExhaustedAt.IsZero() && before < b.Total() && b.Used >= b.Total() {
		b.ExhaustedAt = b.CountingSince.Add(b.Total() - before)
	}
	b.CountingSince = time.Time{}
}

// WarningDue reports whether the ant must be warned now: the warning
// threshold is reached and the warning for the current total was not
// given yet. Call MarkWarned once it is delivered.
func (b *Budget) WarningDue(now time.Time) bool {
	return !b.Warned && b.pastWarning(b.UsedAt(now))
}

func (b *Budget) pastWarning(used time.Duration) bool {
	p := b.Policy.WarningPercent
	return p > 0 && b.Total() > 0 && used*100 >= b.Total()*time.Duration(p)
}

// MarkWarned records that the warning was delivered.
func (b *Budget) MarkWarned() {
	b.Warned = true
}

// WarningMessage is the warning delivered to the ant, such as "12 minutes
// remaining: wrap up and open a PR, or request more time".
func (b *Budget) WarningMessage(now time.Time) string {
	return FormatRemaining(b.Remaining(now)) + " remaining: wrap up and open a PR, or request more time"
}

// FormatRemaining prints a remaining time in whole minutes, rounded down:
// "12 minutes", "1 minute", or "less than 1 minute".
func FormatRemaining(d time.Duration) string {
	switch minutes := int(d / time.Minute); minutes {
	case 0:
		return "less than 1 minute"
	case 1:
		return "1 minute"
	default:
		return fmt.Sprintf("%d minutes", minutes)
	}
}

// grant adds an extension at the given time. A warning given before is
// given again at the new threshold, and an exhaustion recorded before is
// undone when the new total is not used up.
func (b *Budget) grant(minutes int, at time.Time) {
	b.Extended += time.Duration(minutes) * time.Minute
	used := b.UsedAt(at)
	if used < b.Total() {
		b.ExhaustedAt = time.Time{}
	}
	if b.Warned && !b.pastWarning(used) {
		b.Warned = false
	}
}

// maxGrant is the most minutes that can still be granted under the
// ceiling, or -1 when there is no ceiling.
func (b *Budget) maxGrant() int {
	if b.Ceiling() == 0 {
		return -1
	}
	return int(max(0, b.Ceiling()-b.Total()) / time.Minute)
}

// ExtensionStatus is where an extension request stands. It is also what
// request_extension returns (spec ant-tools).
type ExtensionStatus string

// The extension statuses.
const (
	// ExtensionPending: open, waiting for the queen.
	ExtensionPending ExtensionStatus = "pending"
	// ExtensionApproved: granted in full, by the allowance or the queen.
	ExtensionApproved ExtensionStatus = "approved"
	// ExtensionReduced: the queen granted fewer minutes than requested.
	ExtensionReduced ExtensionStatus = "reduced"
	ExtensionDenied  ExtensionStatus = "denied"
	// ExtensionRejected: refused automatically, over the ceiling.
	ExtensionRejected ExtensionStatus = "rejected"
	// ExtensionClosed: closed without a decision taking effect, because
	// the ant moved on.
	ExtensionClosed ExtensionStatus = "closed"
)

// MessageMaxBudget is the reason given for a request over the ceiling.
const MessageMaxBudget = "maximum budget reached"

// ExtensionRequest is one request for more time.
type ExtensionRequest struct {
	Minutes int
	Reason  string
	At      time.Time
	Status  ExtensionStatus
	// Granted is the minutes granted, for approved and reduced requests.
	Granted int
	// Message explains the outcome to the ant.
	Message string
}

// RequestExtension handles a request_extension call at the given time.
// A request over the ceiling is rejected whole. A request within the
// allowance is approved at once. Otherwise it stays open as pending for
// the queen. While a request is open, a new one returns the open one
// unchanged.
func (b *Budget) RequestExtension(minutes int, reason string, at time.Time) (ExtensionRequest, error) {
	if minutes <= 0 {
		return ExtensionRequest{}, fmt.Errorf("an extension must be more than 0 minutes, not %d", minutes)
	}
	if b.Open != nil {
		return *b.Open, nil
	}
	req := ExtensionRequest{Minutes: minutes, Reason: reason, At: at}
	allow := b.Policy.AutoApprove
	switch {
	case b.maxGrant() >= 0 && minutes > b.maxGrant():
		req.Status = ExtensionRejected
		req.Message = MessageMaxBudget
	case minutes <= allow.MaxMinutes && b.AutoApproved < allow.Times:
		b.grant(minutes, at)
		b.AutoApproved++
		req.Status = ExtensionApproved
		req.Granted = minutes
		req.Message = fmt.Sprintf("%d minutes added", minutes)
	default:
		req.Status = ExtensionPending
		req.Message = "waiting for the queen's decision"
		open := req
		b.Open = &open
	}
	return req, nil
}

// ExtensionEffect is what the application must do after a decision on an
// open request.
type ExtensionEffect string

// The extension effects (spec ant-timebox, "Late extension decisions").
const (
	// EffectBudgetGrown: the active run's budget grew; tell the run.
	EffectBudgetGrown ExtensionEffect = "budget-grown"
	// EffectWrapUp: denied during a run; tell the run to wrap up.
	EffectWrapUp ExtensionEffect = "wrap-up"
	// EffectContinue: approved on a timed-out ant, which now has a new
	// budget of the granted minutes; start a run as for continue.
	EffectContinue ExtensionEffect = "continue"
	// EffectNone: denied on a timed-out ant; nothing changes.
	EffectNone ExtensionEffect = "none"
	// EffectClosed: the ant is in another state; the request is closed.
	EffectClosed ExtensionEffect = "closed"
)

// ErrNoOpenRequest is returned when the queen decides on an ant without an
// open extension request.
var ErrNoOpenRequest = errors.New("no open extension request")

// DecideExtension applies the queen's decision on the ant's open request
// at the given time. minutes is the time granted: 0 denies, the requested
// minutes approve, and fewer reduce. On an ant with an active run a grant
// above the ceiling is refused and the request stays open.
func (a *Ant) DecideExtension(minutes int, at time.Time) (ExtensionRequest, ExtensionEffect, error) {
	b := &a.Budget
	if b.Open == nil {
		return ExtensionRequest{}, "", ErrNoOpenRequest
	}
	req := *b.Open
	if minutes < 0 || minutes > req.Minutes {
		return req, "", fmt.Errorf("grant between 0 and %d minutes, not %d", req.Minutes, minutes)
	}
	status := ExtensionApproved
	switch {
	case minutes == 0:
		status = ExtensionDenied
	case minutes < req.Minutes:
		status = ExtensionReduced
	}

	var effect ExtensionEffect
	switch {
	case a.state.HasActiveRun():
		if status == ExtensionDenied {
			effect = EffectWrapUp
			req.Message = "extension denied: wrap up within the remaining time"
			break
		}
		if limit := b.maxGrant(); limit >= 0 && minutes > limit {
			return req, "", fmt.Errorf("%s: at most %d minutes can be granted", MessageMaxBudget, limit)
		}
		b.grant(minutes, at)
		effect = EffectBudgetGrown
		req.Message = fmt.Sprintf("%d minutes added", minutes)
	case a.state == AntTimedOut:
		if status == ExtensionDenied {
			effect = EffectNone
			req.Message = "extension denied"
			break
		}
		a.Budget = NewBudget(time.Duration(minutes)*time.Minute, b.Policy)
		effect = EffectContinue
		req.Message = fmt.Sprintf("continuing with a %d-minute budget", minutes)
	default:
		status = ExtensionClosed
		minutes = 0
		effect = EffectClosed
		req.Message = fmt.Sprintf("closed: the ant is %s", a.state)
	}
	req.Status = status
	req.Granted = minutes
	if status == ExtensionDenied {
		req.Granted = 0
	}
	a.Budget.Open = nil
	return req, effect, nil
}
