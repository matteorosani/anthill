package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

// allowedTransitions is the transition table of spec ant-execution,
// written out again here so that a change to the table in the code must
// also change this test.
var allowedTransitions = []struct {
	from, to domain.AntState
	example  string
}{
	{domain.AntQueued, domain.AntStarting, "a slot freed and the run is still due"},
	{domain.AntQueued, domain.AntDone, "PR merged while queued for a follow-up"},
	{domain.AntQueued, domain.AntAbandoned, "queen abandons"},

	{domain.AntStarting, domain.AntRunning, "session started"},
	{domain.AntStarting, domain.AntFailed, "runner did not start"},
	{domain.AntStarting, domain.AntStopped, "queen stops"},
	{domain.AntStarting, domain.AntDone, "PR merged"},
	{domain.AntStarting, domain.AntAbandoned, "claim lost at start"},

	{domain.AntRunning, domain.AntAwaitingExtension, "request_extension waits for the queen"},
	{domain.AntRunning, domain.AntAwaitingInput, "ask_question"},
	{domain.AntRunning, domain.AntFinishing, "finish called"},
	{domain.AntRunning, domain.AntFailed, "exited without finish"},
	{domain.AntRunning, domain.AntStopped, "/anthill stop"},
	{domain.AntRunning, domain.AntTimedOut, "budget and grace period used up"},
	{domain.AntRunning, domain.AntDone, "PR merged during a follow-up run"},
	{domain.AntRunning, domain.AntAbandoned, "opt-in withdrawn"},

	{domain.AntAwaitingExtension, domain.AntRunning, "decision or end of the wait"},
	{domain.AntAwaitingExtension, domain.AntFailed, "crashed while waiting"},
	{domain.AntAwaitingExtension, domain.AntStopped, "queen stops"},
	{domain.AntAwaitingExtension, domain.AntDone, "PR merged"},
	{domain.AntAwaitingExtension, domain.AntAbandoned, "queen abandons"},

	{domain.AntFinishing, domain.AntSucceeded, "PR open"},
	{domain.AntFinishing, domain.AntDone, "no change needed"},
	{domain.AntFinishing, domain.AntFailed, "process ended before the finish completed"},
	{domain.AntFinishing, domain.AntAbandoned, "queen abandons"},

	{domain.AntAwaitingInput, domain.AntStarting, "answer arrived"},
	{domain.AntAwaitingInput, domain.AntQueued, "answer arrived while capacity is full"},
	{domain.AntAwaitingInput, domain.AntDone, "PR closed"},
	{domain.AntAwaitingInput, domain.AntAbandoned, "queen abandons"},

	{domain.AntStopped, domain.AntStarting, "retry"},
	{domain.AntStopped, domain.AntQueued, "retry while capacity is full"},
	{domain.AntStopped, domain.AntDone, "PR merged"},
	{domain.AntStopped, domain.AntAbandoned, "queen abandons"},

	{domain.AntFailed, domain.AntStarting, "retry after crash"},
	{domain.AntFailed, domain.AntQueued, "retry while capacity is full"},
	{domain.AntFailed, domain.AntDone, "PR merged"},
	{domain.AntFailed, domain.AntAbandoned, "queen abandons"},

	{domain.AntTimedOut, domain.AntStarting, "continue"},
	{domain.AntTimedOut, domain.AntQueued, "continue while capacity is full"},
	{domain.AntTimedOut, domain.AntDone, "draft PR merged"},
	{domain.AntTimedOut, domain.AntAbandoned, "queen abandons"},

	{domain.AntSucceeded, domain.AntStarting, "follow-up run"},
	{domain.AntSucceeded, domain.AntQueued, "follow-up run while capacity is full"},
	{domain.AntSucceeded, domain.AntDone, "PR merged by developer"},
	{domain.AntSucceeded, domain.AntAbandoned, "queen abandons"},
}

func isAllowed(from, to domain.AntState) bool {
	for _, tr := range allowedTransitions {
		if tr.from == from && tr.to == to {
			return true
		}
	}
	return false
}

// antIn returns an ant restored in the given state at t0.
func antIn(t *testing.T, state domain.AntState) *domain.Ant {
	t.Helper()
	ant, err := domain.RestoreAnt(ghItem(12), time.Hour, domain.NewBudget(time.Hour, domain.DefaultBudgetPolicy()),
		[]domain.Transition{{To: state, At: t0, Reason: "restored"}})
	if err != nil {
		t.Fatal(err)
	}
	return ant
}

func TestAllowedTransitions(t *testing.T) {
	for _, tr := range allowedTransitions {
		t.Run(string(tr.from)+"→"+string(tr.to)+": "+tr.example, func(t *testing.T) {
			ant := antIn(t, tr.from)
			at := t0.Add(time.Minute)
			if err := ant.Transition(tr.to, at, tr.example); err != nil {
				t.Fatalf("Transition() = %v", err)
			}
			if ant.State() != tr.to {
				t.Errorf("State() = %s, want %s", ant.State(), tr.to)
			}
			h := ant.History()
			want := domain.Transition{From: tr.from, To: tr.to, At: at, Reason: tr.example}
			if got := h[len(h)-1]; got != want {
				t.Errorf("last transition = %+v, want %+v", got, want)
			}
		})
	}
}

func TestEveryOtherTransitionIsRefused(t *testing.T) {
	for _, from := range domain.AntStates {
		for _, to := range domain.AntStates {
			if isAllowed(from, to) {
				continue
			}
			t.Run(string(from)+"→"+string(to), func(t *testing.T) {
				if domain.CanTransition(from, to) {
					t.Fatal("CanTransition() = true, want false")
				}
				ant := antIn(t, from)
				err := ant.Transition(to, t0.Add(time.Minute), "not allowed")
				if !errors.Is(err, domain.ErrTransition) {
					t.Fatalf("Transition() = %v, want ErrTransition", err)
				}
				if ant.State() != from || len(ant.History()) != 1 {
					t.Errorf("refused transition changed the ant: %s, %d transitions", ant.State(), len(ant.History()))
				}
			})
		}
	}
}

func TestTransitionNeedsReasonAndOrderedTime(t *testing.T) {
	ant := antIn(t, domain.AntRunning)
	if err := ant.Transition(domain.AntFinishing, t0.Add(time.Minute), ""); !errors.Is(err, domain.ErrTransition) {
		t.Errorf("empty reason: %v, want ErrTransition", err)
	}
	if err := ant.Transition(domain.AntFinishing, t0.Add(-time.Minute), "finish"); !errors.Is(err, domain.ErrTransition) {
		t.Errorf("time before the last transition: %v, want ErrTransition", err)
	}
	if ant.State() != domain.AntRunning {
		t.Errorf("State() = %s after refused transitions, want running", ant.State())
	}
}

func TestNewAnt(t *testing.T) {
	for _, state := range domain.AntStates {
		t.Run(string(state), func(t *testing.T) {
			ant, err := domain.NewAnt(ghItem(12), state, time.Hour, domain.DefaultBudgetPolicy(), t0, "accepted")
			wantOK := state == domain.AntStarting || state == domain.AntQueued
			if (err == nil) != wantOK {
				t.Fatalf("NewAnt(%s) error = %v, want ok %v", state, err, wantOK)
			}
			if wantOK {
				h := ant.History()
				if len(h) != 1 || h[0].From != "" || h[0].To != state || !h[0].At.Equal(t0) || h[0].Reason != "accepted" {
					t.Errorf("history = %+v", h)
				}
			}
		})
	}
	if _, err := domain.NewAnt(ghItem(12), domain.AntStarting, time.Hour, domain.DefaultBudgetPolicy(), t0, ""); err == nil {
		t.Error("NewAnt with no reason succeeded")
	}
}

func TestRestoreAnt(t *testing.T) {
	if _, err := domain.RestoreAnt(ghItem(12), time.Hour, domain.Budget{}, nil); err == nil {
		t.Error("RestoreAnt with no history succeeded")
	}
	if _, err := domain.RestoreAnt(ghItem(12), time.Hour, domain.Budget{}, []domain.Transition{{To: "sleeping", At: t0, Reason: "x"}}); err == nil {
		t.Error("RestoreAnt with an invalid state succeeded")
	}
	// Session index recovery finds an ant with an open PR.
	ant := antIn(t, domain.AntSucceeded)
	if ant.State() != domain.AntSucceeded {
		t.Errorf("State() = %s, want succeeded", ant.State())
	}
}

func TestAntStateGroups(t *testing.T) {
	tests := []struct {
		state                          domain.AntState
		active, between, final, counts bool
	}{
		{domain.AntStarting, true, false, false, true},
		{domain.AntRunning, true, false, false, true},
		{domain.AntAwaitingExtension, true, false, false, false},
		{domain.AntFinishing, true, false, false, true},
		{domain.AntQueued, false, true, false, false},
		{domain.AntAwaitingInput, false, true, false, false},
		{domain.AntStopped, false, true, false, false},
		{domain.AntFailed, false, true, false, false},
		{domain.AntTimedOut, false, true, false, false},
		{domain.AntSucceeded, false, true, false, false},
		{domain.AntDone, false, false, true, false},
		{domain.AntAbandoned, false, false, true, false},
	}
	if len(tests) != len(domain.AntStates) {
		t.Fatalf("test covers %d states, want %d", len(tests), len(domain.AntStates))
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			if got := tt.state.HasActiveRun(); got != tt.active {
				t.Errorf("HasActiveRun() = %v", got)
			}
			if got := tt.state.BetweenRuns(); got != tt.between {
				t.Errorf("BetweenRuns() = %v", got)
			}
			if got := tt.state.Final(); got != tt.final {
				t.Errorf("Final() = %v", got)
			}
			if got := tt.state.CountsBudget(); got != tt.counts {
				t.Errorf("CountsBudget() = %v", got)
			}
		})
	}
}

func TestAntAsksAQuestion(t *testing.T) {
	ant, err := domain.NewAnt(ghItem(12), domain.AntStarting, time.Hour, domain.DefaultBudgetPolicy(), t0, "accepted")
	if err != nil {
		t.Fatal(err)
	}
	mustTransition(t, ant, domain.AntRunning, t0.Add(time.Minute))
	mustTransition(t, ant, domain.AntAwaitingInput, t0.Add(10*time.Minute))
	if ant.Budget.Counting() {
		t.Error("budget still counting while awaiting input")
	}
	if got := ant.Budget.UsedAt(t0.Add(5 * time.Hour)); got != 10*time.Minute {
		t.Errorf("used = %s, want 10m0s", got)
	}
}

func mustTransition(t *testing.T, ant *domain.Ant, to domain.AntState, at time.Time) {
	t.Helper()
	if err := ant.Transition(to, at, "test"); err != nil {
		t.Fatalf("Transition(%s) = %v", to, err)
	}
}
