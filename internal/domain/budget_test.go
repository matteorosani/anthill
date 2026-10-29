package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

// The tests pass fixed times instead of reading a clock: at(m) is m
// minutes after t0.
func at(minutes float64) time.Time {
	return t0.Add(time.Duration(minutes * float64(time.Minute)))
}

// runningAnt returns an ant that started at t0 with the given base budget
// and policy, and is running from at(0).
func runningAnt(t *testing.T, base time.Duration, policy domain.BudgetPolicy) *domain.Ant {
	t.Helper()
	ant, err := domain.NewAnt(ghItem(12), domain.AntStarting, base, policy, t0, "accepted")
	if err != nil {
		t.Fatal(err)
	}
	mustTransition(t, ant, domain.AntRunning, t0)
	return ant
}

func TestBaseBudget(t *testing.T) {
	tests := []struct {
		name                             string
		queen, label, repository, global int
		want                             time.Duration
		source                           domain.BudgetSource
	}{
		{"default budget", 0, 0, 0, 0, 60 * time.Minute, domain.BudgetFromDefault},
		{"budget label", 0, 90, 45, 30, 90 * time.Minute, domain.BudgetFromLabel},
		{"repository setting", 0, 0, 45, 30, 45 * time.Minute, domain.BudgetFromRepository},
		{"global setting", 0, 0, 0, 30, 30 * time.Minute, domain.BudgetFromGlobal},
		{"queen's override wins and is not capped", 240, 90, 45, 30, 240 * time.Minute, domain.BudgetFromQueen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, source := domain.BaseBudget(tt.queen, tt.label, tt.repository, tt.global)
			if got != tt.want || source != tt.source {
				t.Errorf("BaseBudget() = %s, %s; want %s, %s", got, source, tt.want, tt.source)
			}
		})
	}
}

func TestBudgetLabels(t *testing.T) {
	const maxBase = 180
	tests := []struct {
		name    string
		label   string
		want    time.Duration
		source  domain.BudgetSource
		ceiling time.Duration
		warning domain.WarningReason
	}{
		{"budget label: budget:90, ceiling 3 × 90", "budget:90", 90 * time.Minute, domain.BudgetFromLabel, 270 * time.Minute, ""},
		{"budget label above the maximum: budget:600 capped at 180", "budget:600", 180 * time.Minute, domain.BudgetFromLabel, 540 * time.Minute, domain.WarnCapped},
		{"invalid budget label: budget:abc falls back to the next source", "budget:abc", 45 * time.Minute, domain.BudgetFromRepository, 135 * time.Minute, domain.WarnInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings, warnings := domain.ParseSettingLabels([]string{tt.label}, maxBase)
			got, source := domain.BaseBudget(0, settings.BudgetMinutes, 45, 0)
			if got != tt.want || source != tt.source {
				t.Errorf("BaseBudget() = %s, %s; want %s, %s", got, source, tt.want, tt.source)
			}
			b := domain.NewBudget(got, domain.DefaultBudgetPolicy())
			if b.Ceiling() != tt.ceiling {
				t.Errorf("Ceiling() = %s, want %s", b.Ceiling(), tt.ceiling)
			}
			if tt.warning == "" && len(warnings) != 0 || tt.warning != "" && (len(warnings) != 1 || warnings[0].Reason != tt.warning) {
				t.Errorf("warnings = %+v, want %q", warnings, tt.warning)
			}
		})
	}
}

func TestBudgetCarriesOverBetweenRuns(t *testing.T) {
	t.Run("retry after a failure: 40 of 60 minutes used leaves 20", func(t *testing.T) {
		ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
		mustTransition(t, ant, domain.AntFailed, at(40))
		mustTransition(t, ant, domain.AntStarting, at(24*60))
		if got := ant.Budget.Remaining(at(24 * 60)); got != 20*time.Minute {
			t.Errorf("Remaining() = %s, want 20m0s", got)
		}
	})
	t.Run("paused while waiting for an answer", func(t *testing.T) {
		ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
		mustTransition(t, ant, domain.AntAwaitingInput, at(30))
		mustTransition(t, ant, domain.AntStarting, at(150))
		if got := ant.Budget.Remaining(at(150)); got != 30*time.Minute {
			t.Errorf("Remaining() = %s, want 30m0s", got)
		}
	})
	t.Run("stop and retry later keeps the remaining budget", func(t *testing.T) {
		ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
		mustTransition(t, ant, domain.AntStopped, at(15))
		mustTransition(t, ant, domain.AntQueued, at(24*60))
		mustTransition(t, ant, domain.AntStarting, at(24*60+30))
		if got := ant.Budget.Remaining(at(24*60 + 35)); got != 40*time.Minute {
			t.Errorf("Remaining() = %s, want 40m0s", got)
		}
	})
}

func TestBudgetCountsActiveTimeOnly(t *testing.T) {
	ant, err := domain.NewAnt(ghItem(12), domain.AntQueued, time.Hour, domain.DefaultBudgetPolicy(), t0, "accepted")
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		to domain.AntState
		at float64
	}{
		{domain.AntStarting, 10},          // queued 10 minutes: not counted
		{domain.AntRunning, 12},           // starting 2 minutes: counted
		{domain.AntAwaitingExtension, 20}, // running 8 minutes: counted
		{domain.AntRunning, 25},           // waiting 5 minutes: not counted
		{domain.AntFinishing, 30},         // running 5 minutes: counted
		{domain.AntSucceeded, 31},         // finishing 1 minute: counted
	}
	for _, s := range steps {
		mustTransition(t, ant, s.to, at(s.at))
	}
	if got, want := ant.Budget.UsedAt(at(100)), 16*time.Minute; got != want {
		t.Errorf("used = %s, want %s", got, want)
	}
}

func TestWarningThreshold(t *testing.T) {
	ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
	if ant.Budget.WarningDue(at(47.9)) {
		t.Error("warning due before 80%")
	}
	if !ant.Budget.WarningDue(at(48)) {
		t.Fatal("warning not due at 48 of 60 minutes")
	}
	want := "12 minutes remaining: wrap up and open a PR, or request more time"
	if got := ant.Budget.WarningMessage(at(48)); got != want {
		t.Errorf("WarningMessage() = %q, want %q", got, want)
	}
	ant.Budget.MarkWarned()
	if ant.Budget.WarningDue(at(50)) {
		t.Error("warning due again after it was delivered")
	}

	// An extension that brings the ant below the threshold re-arms it.
	ant.Budget.Policy.AutoApprove = domain.ExtensionAllowance{MaxMinutes: 30, Times: 1}
	if _, err := ant.Budget.RequestExtension(30, "more tests", at(50)); err != nil {
		t.Fatal(err)
	}
	if ant.Budget.WarningDue(at(50)) {
		t.Error("warning due at 50 of 90 minutes")
	}
	if !ant.Budget.WarningDue(at(72)) {
		t.Error("warning not due at 72 of 90 minutes")
	}
}

func TestFormatRemaining(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{12*time.Minute + 59*time.Second, "12 minutes"},
		{time.Minute, "1 minute"},
		{59 * time.Second, "less than 1 minute"},
		{0, "less than 1 minute"},
	}
	for _, tt := range tests {
		if got := domain.FormatRemaining(tt.d); got != tt.want {
			t.Errorf("FormatRemaining(%s) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestAntChecksItsBudget(t *testing.T) {
	ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
	if got := ant.Budget.Remaining(at(20)); got != 40*time.Minute {
		t.Errorf("Remaining() = %s, want 40m0s", got)
	}
}

func TestExtensionRequests(t *testing.T) {
	t.Run("auto-approved: 10 minutes within an allowance of 15 once", func(t *testing.T) {
		policy := domain.DefaultBudgetPolicy()
		policy.AutoApprove = domain.ExtensionAllowance{MaxMinutes: 15, Times: 1}
		ant := runningAnt(t, time.Hour, policy)
		req, err := ant.Budget.RequestExtension(10, "one more test", at(30))
		if err != nil || req.Status != domain.ExtensionApproved || req.Granted != 10 {
			t.Fatalf("RequestExtension() = %+v, %v; want approved 10", req, err)
		}
		if ant.Budget.Total() != 70*time.Minute || ant.Budget.Open != nil {
			t.Errorf("total %s, open %v; want 1h10m0s and no open request", ant.Budget.Total(), ant.Budget.Open)
		}
		// The allowance is used up, so the next request goes to the queen.
		req, _ = ant.Budget.RequestExtension(10, "again", at(40))
		if req.Status != domain.ExtensionPending {
			t.Errorf("second request = %s, want pending", req.Status)
		}
	})
	t.Run("built-in allowance of 0 always asks the queen", func(t *testing.T) {
		ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
		req, _ := ant.Budget.RequestExtension(1, "tiny", at(30))
		if req.Status != domain.ExtensionPending || ant.Budget.Open == nil {
			t.Errorf("request = %+v, want pending and open", req)
		}
	})
	t.Run("a request above the allowance goes whole to the queen", func(t *testing.T) {
		policy := domain.DefaultBudgetPolicy()
		policy.AutoApprove = domain.ExtensionAllowance{MaxMinutes: 15, Times: 1}
		ant := runningAnt(t, time.Hour, policy)
		req, _ := ant.Budget.RequestExtension(20, "bigger", at(30))
		if req.Status != domain.ExtensionPending || ant.Budget.Total() != time.Hour {
			t.Errorf("request = %+v, total %s; want pending with nothing granted", req, ant.Budget.Total())
		}
	})
	t.Run("a second request while one is open returns the open one", func(t *testing.T) {
		ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
		first, _ := ant.Budget.RequestExtension(30, "first", at(30))
		second, _ := ant.Budget.RequestExtension(10, "second", at(35))
		if second != first {
			t.Errorf("second = %+v, want the open request %+v", second, first)
		}
	})
	t.Run("zero minutes is refused", func(t *testing.T) {
		ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
		if _, err := ant.Budget.RequestExtension(0, "nothing", at(1)); err == nil {
			t.Error("RequestExtension(0) succeeded")
		}
	})
}

// waitingAnt returns a running ant with 60 minutes, which requested
// minutes at 30 and is awaiting the queen's decision.
func waitingAnt(t *testing.T, minutes int) *domain.Ant {
	t.Helper()
	ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
	if req, _ := ant.Budget.RequestExtension(minutes, "needs more time", at(30)); req.Status != domain.ExtensionPending {
		t.Fatalf("request = %s, want pending", req.Status)
	}
	mustTransition(t, ant, domain.AntAwaitingExtension, at(30))
	return ant
}

func TestQueenDecisions(t *testing.T) {
	tests := []struct {
		name      string
		requested int
		granted   int
		status    domain.ExtensionStatus
		effect    domain.ExtensionEffect
		total     time.Duration
	}{
		{"queen approves", 30, 30, domain.ExtensionApproved, domain.EffectBudgetGrown, 90 * time.Minute},
		{"queen approves a smaller extension: 20 of 45", 45, 20, domain.ExtensionReduced, domain.EffectBudgetGrown, 80 * time.Minute},
		{"queen denies", 30, 0, domain.ExtensionDenied, domain.EffectWrapUp, 60 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ant := waitingAnt(t, tt.requested)
			req, effect, err := ant.DecideExtension(tt.granted, at(32))
			if err != nil {
				t.Fatal(err)
			}
			if req.Status != tt.status || effect != tt.effect || req.Granted != tt.granted {
				t.Errorf("DecideExtension() = %s, %s, granted %d; want %s, %s, %d", req.Status, effect, req.Granted, tt.status, tt.effect, tt.granted)
			}
			if ant.Budget.Total() != tt.total || ant.Budget.Open != nil {
				t.Errorf("total %s, open %v; want %s and no open request", ant.Budget.Total(), ant.Budget.Open, tt.total)
			}
			mustTransition(t, ant, domain.AntRunning, at(32))
			// The two minutes of waiting did not count.
			if got, want := ant.Budget.Remaining(at(32)), tt.total-30*time.Minute; got != want {
				t.Errorf("Remaining() = %s, want %s", got, want)
			}
		})
	}
}

func TestQueenDecisionErrors(t *testing.T) {
	ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
	if _, _, err := ant.DecideExtension(10, at(1)); !errors.Is(err, domain.ErrNoOpenRequest) {
		t.Errorf("decision without a request: %v, want ErrNoOpenRequest", err)
	}
	ant = waitingAnt(t, 30)
	if _, _, err := ant.DecideExtension(31, at(31)); err == nil {
		t.Error("granting more than requested succeeded")
	}
	if ant.Budget.Open == nil {
		t.Error("a refused decision closed the request")
	}
}

func TestNoDecisionWithinTheWait(t *testing.T) {
	ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
	req, _ := ant.Budget.RequestExtension(30, "needs more time", at(56))
	if req.Status != domain.ExtensionPending {
		t.Fatalf("request = %s, want pending", req.Status)
	}
	mustTransition(t, ant, domain.AntAwaitingExtension, at(56))
	mustTransition(t, ant, domain.AntRunning, at(61)) // the 5-minute extension wait ended
	if got := ant.Budget.Remaining(at(61)); got != 4*time.Minute {
		t.Errorf("Remaining() = %s, want 4m0s", got)
	}
	if !ant.Budget.Counting() || ant.Budget.Open == nil {
		t.Error("want the budget counting again and the request still open")
	}
}

func TestLateExtensionDecisions(t *testing.T) {
	t.Run("late approval during a run grows the budget", func(t *testing.T) {
		ant := waitingAnt(t, 30)
		mustTransition(t, ant, domain.AntRunning, at(35))
		req, effect, err := ant.DecideExtension(30, at(40))
		if err != nil || effect != domain.EffectBudgetGrown || req.Status != domain.ExtensionApproved {
			t.Fatalf("DecideExtension() = %+v, %s, %v", req, effect, err)
		}
		if got := ant.Budget.Remaining(at(40)); got != 55*time.Minute {
			t.Errorf("Remaining() = %s, want 55m0s", got)
		}
	})
	t.Run("late denial during a run tells the ant to wrap up", func(t *testing.T) {
		ant := waitingAnt(t, 30)
		mustTransition(t, ant, domain.AntRunning, at(35))
		req, effect, _ := ant.DecideExtension(0, at(40))
		if effect != domain.EffectWrapUp || req.Message == "" {
			t.Errorf("effect %s, message %q; want wrap-up with a message", effect, req.Message)
		}
	})
	t.Run("budget runs out while the request is open", func(t *testing.T) {
		ant := waitingAnt(t, 30)
		mustTransition(t, ant, domain.AntRunning, at(35))
		// 30 minutes used before the wait, 30 left from 35: exhausted at 65,
		// grace period over at 68.
		if !ant.Budget.Exhausted(at(65)) || ant.Budget.Exhausted(at(64.9)) {
			t.Error("want the budget exhausted at 65 and not before")
		}
		if ant.TimeOutDue(at(67.9)) || !ant.TimeOutDue(at(68)) {
			t.Error("want the time-out due when the grace period ends at 68")
		}
		mustTransition(t, ant, domain.AntTimedOut, at(68))
		if ant.Budget.Open == nil || ant.Budget.Open.Status != domain.ExtensionPending {
			t.Error("the request is no longer open after the time-out")
		}
	})
	t.Run("late approval on a timed-out ant acts as continue", func(t *testing.T) {
		ant := waitingAnt(t, 30)
		mustTransition(t, ant, domain.AntRunning, at(35))
		mustTransition(t, ant, domain.AntTimedOut, at(68))
		req, effect, err := ant.DecideExtension(30, at(200))
		if err != nil || effect != domain.EffectContinue || req.Status != domain.ExtensionApproved {
			t.Fatalf("DecideExtension() = %+v, %s, %v", req, effect, err)
		}
		mustTransition(t, ant, domain.AntStarting, at(201))
		if got := ant.Budget.Remaining(at(201)); got != 30*time.Minute {
			t.Errorf("Remaining() = %s, want a new 30-minute budget", got)
		}
	})
	t.Run("late denial on a timed-out ant changes nothing", func(t *testing.T) {
		ant := waitingAnt(t, 30)
		mustTransition(t, ant, domain.AntRunning, at(35))
		mustTransition(t, ant, domain.AntTimedOut, at(68))
		before := ant.Budget.Remaining(at(200))
		req, effect, _ := ant.DecideExtension(0, at(200))
		if effect != domain.EffectNone || req.Status != domain.ExtensionDenied || ant.State() != domain.AntTimedOut || ant.Budget.Remaining(at(200)) != before {
			t.Errorf("effect %s, status %s, state %s; want none, denied, timed-out", effect, req.Status, ant.State())
		}
	})
	t.Run("a decision in any other state closes the request", func(t *testing.T) {
		ant := waitingAnt(t, 30)
		mustTransition(t, ant, domain.AntStopped, at(31))
		req, effect, _ := ant.DecideExtension(30, at(40))
		if effect != domain.EffectClosed || req.Status != domain.ExtensionClosed || req.Granted != 0 {
			t.Errorf("effect %s, status %s, granted %d; want closed with nothing granted", effect, req.Status, req.Granted)
		}
		if ant.Budget.Total() != time.Hour || ant.Budget.Open != nil {
			t.Error("a closed request changed the budget or stayed open")
		}
	})
}

func TestMaximumTotalBudget(t *testing.T) {
	t.Run("ceiling reached: 120 extra minutes on a 60-minute base", func(t *testing.T) {
		policy := domain.DefaultBudgetPolicy()
		policy.AutoApprove = domain.ExtensionAllowance{MaxMinutes: 120, Times: 1}
		ant := runningAnt(t, time.Hour, policy)
		if req, _ := ant.Budget.RequestExtension(120, "big change", at(10)); req.Status != domain.ExtensionApproved {
			t.Fatalf("first request = %s, want approved", req.Status)
		}
		req, err := ant.Budget.RequestExtension(10, "a bit more", at(20))
		if err != nil || req.Status != domain.ExtensionRejected || req.Message != "maximum budget reached" {
			t.Errorf("RequestExtension() = %+v, %v; want rejected with \"maximum budget reached\"", req, err)
		}
		if ant.Budget.Open != nil {
			t.Error("a rejected request stayed open")
		}
	})
	t.Run("a request crossing the ceiling is rejected whole", func(t *testing.T) {
		ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
		req, _ := ant.Budget.RequestExtension(121, "too much", at(10))
		if req.Status != domain.ExtensionRejected || ant.Budget.Total() != time.Hour {
			t.Errorf("request = %s, total %s; want rejected with nothing granted", req.Status, ant.Budget.Total())
		}
	})
	t.Run("the queen cannot grant above the ceiling", func(t *testing.T) {
		policy := domain.DefaultBudgetPolicy()
		policy.AutoApprove = domain.ExtensionAllowance{MaxMinutes: 100, Times: 1}
		ant := runningAnt(t, time.Hour, policy)
		_, _ = ant.Budget.RequestExtension(100, "big", at(10))
		_, _ = ant.Budget.RequestExtension(20, "the rest", at(11)) // fits: 180 total
		_, _ = ant.Budget.RequestExtension(1, "x", at(12))         // returns the open one
		ant.Budget.Policy.CeilingFactor = 2                        // the ceiling drops to 120
		if _, _, err := ant.DecideExtension(20, at(13)); err == nil {
			t.Error("granting above the ceiling succeeded")
		}
		if ant.Budget.Open == nil {
			t.Error("the refused grant closed the request")
		}
	})
}

func TestBudgetExhaustion(t *testing.T) {
	t.Run("time runs out: grace period, then time-out", func(t *testing.T) {
		ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
		if ant.Budget.Exhausted(at(59.9)) || !ant.Budget.Exhausted(at(60)) {
			t.Error("want the budget exhausted at 60 minutes and not before")
		}
		if ant.TimeOutDue(at(62.9)) || !ant.TimeOutDue(at(63)) {
			t.Error("want the time-out due when the 3-minute grace period ends")
		}
		if ant.Budget.Remaining(at(62)) != 0 {
			t.Errorf("Remaining() = %s during grace, want 0", ant.Budget.Remaining(at(62)))
		}
		mustTransition(t, ant, domain.AntTimedOut, at(63))
	})
	t.Run("budget runs out during finishing: no time-out", func(t *testing.T) {
		ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
		mustTransition(t, ant, domain.AntFinishing, at(59))
		if ant.TimeOutDue(at(70)) {
			t.Error("time-out due while finishing")
		}
		if err := ant.Transition(domain.AntTimedOut, at(70), "budget used up"); !errors.Is(err, domain.ErrTransition) {
			t.Errorf("finishing → timed-out = %v, want ErrTransition", err)
		}
		mustTransition(t, ant, domain.AntSucceeded, at(71))
	})
	t.Run("grace period keeps running while waiting for an extension", func(t *testing.T) {
		ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
		_, _ = ant.Budget.RequestExtension(30, "nearly there", at(61))
		mustTransition(t, ant, domain.AntAwaitingExtension, at(61))
		if ant.TimeOutDue(at(64)) {
			t.Error("time-out due while awaiting an extension")
		}
		mustTransition(t, ant, domain.AntRunning, at(66))
		if !ant.TimeOutDue(at(66)) {
			t.Error("want the time-out due once running again after the grace period")
		}
	})
	t.Run("an extension granted during grace undoes the exhaustion", func(t *testing.T) {
		ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
		_, _ = ant.Budget.RequestExtension(30, "nearly there", at(61))
		mustTransition(t, ant, domain.AntAwaitingExtension, at(61))
		if _, _, err := ant.DecideExtension(30, at(62)); err != nil {
			t.Fatal(err)
		}
		mustTransition(t, ant, domain.AntRunning, at(62))
		if ant.Budget.Exhausted(at(62)) || ant.TimeOutDue(at(70)) {
			t.Error("want the budget not exhausted after the extension")
		}
		if got := ant.Budget.Remaining(at(62)); got != 29*time.Minute {
			t.Errorf("Remaining() = %s, want 29m0s", got)
		}
	})
	t.Run("queen continues a timed-out ant with 30 minutes", func(t *testing.T) {
		ant := runningAnt(t, time.Hour, domain.DefaultBudgetPolicy())
		mustTransition(t, ant, domain.AntTimedOut, at(63))
		if _, err := ant.StartNewBudget(30 * time.Minute); err != nil {
			t.Fatal(err)
		}
		mustTransition(t, ant, domain.AntStarting, at(120))
		if got := ant.Budget.Remaining(at(120)); got != 30*time.Minute {
			t.Errorf("Remaining() = %s, want 30m0s", got)
		}
		if ant.Budget.Ceiling() != 90*time.Minute {
			t.Errorf("Ceiling() = %s, want 3 × 30 minutes", ant.Budget.Ceiling())
		}
	})
}

func TestFollowUpBudget(t *testing.T) {
	if got := domain.FollowUpBudget(time.Hour, 0); got != 30*time.Minute {
		t.Errorf("default follow-up budget = %s, want 30m0s", got)
	}
	if got := domain.FollowUpBudget(45*time.Minute, 50); got != 22*time.Minute+30*time.Second {
		t.Errorf("follow-up budget of 45 minutes = %s, want 22m30s", got)
	}

	ant := waitingAnt(t, 30)
	mustTransition(t, ant, domain.AntRunning, at(31))
	if _, err := ant.StartNewBudget(time.Hour); err == nil {
		t.Error("a new budget during a run succeeded")
	}
	mustTransition(t, ant, domain.AntFinishing, at(40))
	mustTransition(t, ant, domain.AntSucceeded, at(41))
	closed, err := ant.StartNewBudget(domain.FollowUpBudget(ant.Base, 50))
	if err != nil {
		t.Fatal(err)
	}
	if closed == nil || closed.Status != domain.ExtensionClosed {
		t.Errorf("closed request = %+v, want the open request closed", closed)
	}
	if ant.Budget.Total() != 30*time.Minute || ant.Budget.UsedAt(at(100)) != 0 || ant.Budget.Open != nil {
		t.Errorf("new budget: total %s, used %s, open %v", ant.Budget.Total(), ant.Budget.UsedAt(at(100)), ant.Budget.Open)
	}
}
