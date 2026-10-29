package domain

import (
	"errors"
	"fmt"
	"time"
)

// AntState is the state of an ant (spec ant-execution).
type AntState string

// The ant states.
const (
	// With an active run.
	AntStarting          AntState = "starting"
	AntRunning           AntState = "running"
	AntAwaitingExtension AntState = "awaiting-extension"
	AntFinishing         AntState = "finishing"
	// Between runs.
	AntQueued        AntState = "queued"
	AntAwaitingInput AntState = "awaiting-input"
	AntStopped       AntState = "stopped"
	AntFailed        AntState = "failed"
	AntTimedOut      AntState = "timed-out"
	AntSucceeded     AntState = "succeeded"
	// Final.
	AntDone      AntState = "done"
	AntAbandoned AntState = "abandoned"
)

// AntStates lists every ant state.
var AntStates = []AntState{
	AntStarting, AntRunning, AntAwaitingExtension, AntFinishing,
	AntQueued, AntAwaitingInput, AntStopped, AntFailed, AntTimedOut, AntSucceeded,
	AntDone, AntAbandoned,
}

// Valid reports whether s is one of the ant states.
func (s AntState) Valid() bool {
	_, ok := antTransitions[s]
	return ok
}

// HasActiveRun reports whether an ant in state s has a run whose process
// is alive.
func (s AntState) HasActiveRun() bool {
	switch s {
	case AntStarting, AntRunning, AntAwaitingExtension, AntFinishing:
		return true
	}
	return false
}

// Final reports whether s is done or abandoned. A final ant never changes
// state again.
func (s AntState) Final() bool {
	return s == AntDone || s == AntAbandoned
}

// BetweenRuns reports whether an ant in state s has no active run and is
// not final.
func (s AntState) BetweenRuns() bool {
	return s.Valid() && !s.HasActiveRun() && !s.Final()
}

// CountsBudget reports whether time in state s uses the budget (spec
// ant-timebox): only starting, running and finishing do.
func (s AntState) CountsBudget() bool {
	return s == AntStarting || s == AntRunning || s == AntFinishing
}

// antTransitions is the table of allowed transitions: for each state, the
// set of states it may move to. Every pair not in the table is refused.
var antTransitions = map[AntState]map[AntState]bool{
	AntQueued: set(AntStarting, AntDone, AntAbandoned),
	// failed: the runner did not start or the agent crashed. stopped: the
	// queen or /anthill stop ended the run.
	AntStarting: set(AntRunning, AntFailed, AntStopped, AntDone, AntAbandoned),
	AntRunning: set(AntAwaitingExtension, AntAwaitingInput, AntFinishing,
		AntFailed, AntStopped, AntTimedOut, AntDone, AntAbandoned),
	// Back to running when the decision arrives or the wait ends. The
	// budget does not count while waiting, so it cannot time out here.
	AntAwaitingExtension: set(AntRunning, AntFailed, AntStopped, AntDone, AntAbandoned),
	// Once finishing, the finish completes and the ant cannot time out.
	// succeeded: the PR is open (complete, partial, or a follow-up run
	// with no change). done: a task run with no change. failed: the
	// process ended before Anthill completed the finish.
	AntFinishing: set(AntSucceeded, AntDone, AntFailed, AntAbandoned),
	// Between runs, a due run starts at once or waits for capacity.
	AntAwaitingInput: set(AntStarting, AntQueued, AntDone, AntAbandoned),
	AntStopped:       set(AntStarting, AntQueued, AntDone, AntAbandoned),
	AntFailed:        set(AntStarting, AntQueued, AntDone, AntAbandoned),
	AntTimedOut:      set(AntStarting, AntQueued, AntDone, AntAbandoned),
	AntSucceeded:     set(AntStarting, AntQueued, AntDone, AntAbandoned),
	AntDone:          {},
	AntAbandoned:     {},
}

func set(states ...AntState) map[AntState]bool {
	m := make(map[AntState]bool, len(states))
	for _, s := range states {
		m[s] = true
	}
	return m
}

// CanTransition reports whether an ant may move from one state to
// another. A state never moves to itself.
func CanTransition(from, to AntState) bool {
	return antTransitions[from][to]
}

// ErrTransition is returned, wrapped, for every refused transition.
var ErrTransition = errors.New("transition not allowed")

// Transition records one change of an ant's state.
type Transition struct {
	From   AntState
	To     AntState
	At     time.Time
	Reason string
}

// Ant is the worker for one accepted item (spec ant-execution). It owns
// its budget: every transition starts or pauses the budget's counting.
// Change its state only through Transition.
type Ant struct {
	Item Ref
	// Base is the base budget at acceptance. Follow-up budgets are a share
	// of it.
	Base   time.Duration
	Budget Budget

	state   AntState
	history []Transition
}

// NewAnt creates the ant for an accepted item, in state starting or, when
// capacity is full, queued. The first entry of its history has an empty
// From.
func NewAnt(item Ref, state AntState, base time.Duration, policy BudgetPolicy, at time.Time, reason string) (*Ant, error) {
	if state != AntStarting && state != AntQueued {
		return nil, fmt.Errorf("%w: a new ant starts as %s or %s, not %q", ErrTransition, AntStarting, AntQueued, state)
	}
	if reason == "" {
		return nil, fmt.Errorf("%w: a transition needs a reason", ErrTransition)
	}
	a := &Ant{
		Item:    item,
		Base:    base,
		Budget:  NewBudget(base, policy),
		state:   state,
		history: []Transition{{To: state, At: at, Reason: reason}},
	}
	if state.CountsBudget() {
		a.Budget.resume(at)
	}
	return a, nil
}

// RestoreAnt rebuilds an ant from stored or recovered data without
// checking the transition table, such as an ant found as succeeded when
// the session index is rebuilt. Its state is the last entry of history.
func RestoreAnt(item Ref, base time.Duration, budget Budget, history []Transition) (*Ant, error) {
	if len(history) == 0 {
		return nil, errors.New("an ant needs at least one transition")
	}
	state := history[len(history)-1].To
	if !state.Valid() {
		return nil, fmt.Errorf("ant state %q is not valid", state)
	}
	return &Ant{Item: item, Base: base, Budget: budget, state: state, history: append([]Transition(nil), history...)}, nil
}

// State returns the current state.
func (a *Ant) State() AntState {
	return a.state
}

// History returns a copy of every transition so far, oldest first.
func (a *Ant) History() []Transition {
	return append([]Transition(nil), a.history...)
}

// Transition moves the ant to a new state at the given time, for the
// given reason. It refuses a pair missing from the table, an empty reason
// and a time before the previous transition. Moving into or out of a
// state that counts the budget starts or pauses the budget.
func (a *Ant) Transition(to AntState, at time.Time, reason string) error {
	from := a.state
	switch {
	case !CanTransition(from, to):
		return fmt.Errorf("%w: %s → %s", ErrTransition, from, to)
	case reason == "":
		return fmt.Errorf("%w: %s → %s needs a reason", ErrTransition, from, to)
	case at.Before(a.history[len(a.history)-1].At):
		return fmt.Errorf("%w: %s → %s at %s is before the previous transition", ErrTransition, from, to, at.Format(time.RFC3339))
	}
	switch {
	case from.CountsBudget() && !to.CountsBudget():
		a.Budget.pause(at)
	case !from.CountsBudget() && to.CountsBudget():
		a.Budget.resume(at)
	}
	a.state = to
	a.history = append(a.history, Transition{From: from, To: to, At: at, Reason: reason})
	return nil
}

// TimeOutDue reports whether Anthill must end the active run and make the
// ant timed-out: the ant is running, its budget is used up, and the grace
// period after that has ended. A finishing ant completes its finish
// instead, and an ant waiting for an extension decision is not counting.
func (a *Ant) TimeOutDue(now time.Time) bool {
	return a.state == AntRunning && a.Budget.GraceOver(now)
}

// StartNewBudget gives the ant a new budget of the given length, for a
// follow-up run or a continue. The ant must be between runs. An open
// extension request is closed and returned, so it can be stored.
func (a *Ant) StartNewBudget(initial time.Duration) (*ExtensionRequest, error) {
	if !a.state.BetweenRuns() {
		return nil, fmt.Errorf("a new budget needs an ant between runs, not %s", a.state)
	}
	closed := a.Budget.Open
	if closed != nil {
		closed.Status = ExtensionClosed
		closed.Message = "closed: the ant got a new budget"
	}
	a.Budget = NewBudget(initial, a.Budget.Policy)
	return closed, nil
}
