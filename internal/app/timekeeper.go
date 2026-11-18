package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// The timekeeper (spec ant-timebox; design D21) is the part of the
// supervisor that times active runs from their ant's budget:
//   - the deadline: each run starts with the time at which the executor
//     ends it, the budget left plus the grace period. BudgetChanged moves
//     it whenever the budget stops counting, counts again or grows: it is
//     cleared while request_extension waits, and set again from the
//     budget afterwards. A finishing ant keeps it: the finish handling
//     ends the run as soon as finish returns. The executor
//     ends the process tree at the deadline on its own timer, so the hard
//     limit never depends on the agent or on this service;
//   - the warning: at the warning threshold, Tick delivers the warning to
//     the run (design D5) and notifies the queen;
//   - the notice to stop: when the budget runs out, Tick tells the run to
//     stop and commit within the grace period.
//
// A run ended at its deadline makes its ant timed-out (see endRun).

// TimekeeperInterval is how often RunTimekeeper checks the active runs
// for a warning or a notice to stop that is due. The deadline does not
// depend on it.
const TimekeeperInterval = 5 * time.Second

// RunMessages delivers a message to the agent of an ant's active run by
// the first means its runner supports (spec ant-tools, "Messages during
// a run"). The ant tools implement it.
type RunMessages interface {
	Deliver(ctx context.Context, ant ports.AntID, text string) (domain.DeliveryMeans, error)
}

// BudgetWatcher is told when an ant's budget stops counting, counts
// again or grows outside the supervisor: when request_extension starts
// or stops waiting, and when an extension is granted. The supervisor
// implements it.
type BudgetWatcher interface {
	BudgetChanged(ctx context.Context, ant ports.AntID) error
}

var (
	_ BudgetWatcher = (*AntSupervisor)(nil)
	_ RunMessages   = (*AntTools)(nil)
)

// runClock is the timekeeper's record of one active run.
type runClock struct {
	// deadline is the last deadline given to the executor; zero is none.
	deadline time.Time
	// stopNotice is the moment the budget ran out for which the notice
	// to stop was given, so it is given once, and again only after an
	// extension moved that moment.
	stopNotice time.Time
}

// BudgetChanged moves the deadline of the ant's active run to match its
// budget (design D21): none while request_extension waits, and otherwise
// the moment the budget runs out plus the grace period. A deadline that
// has passed ends the run at once. It does nothing for an ant without an
// active run, and for a finishing ant, whose run the finish handling
// ends.
func (s *AntSupervisor) BudgetChanged(ctx context.Context, id ports.AntID) error {
	defer s.lockAnt(id)()
	r := s.runOf(id)
	if r == nil {
		return nil
	}
	rec, err := s.store.Ant(ctx, id)
	if err != nil {
		return fmt.Errorf("reading ant %d: %w", id, err)
	}
	var want time.Time
	switch rec.State() {
	case domain.AntStarting, domain.AntRunning:
		want = rec.Budget.Deadline()
	case domain.AntAwaitingExtension:
	default:
		return nil
	}
	s.mu.Lock()
	same := r.clock.deadline.Equal(want)
	r.clock.deadline = want
	s.mu.Unlock()
	if same {
		return nil
	}
	err = s.deps.Executor.SetDeadline(r.exec, want)
	if errors.Is(err, ports.ErrUnknownExecution) {
		// The run has just ended; its end is handled by its events.
		return nil
	}
	if err != nil {
		return fmt.Errorf("moving the deadline of the run of ant %d: %w", id, err)
	}
	return nil
}

// Tick delivers the warnings and the notices to stop that are due at the
// clock's time to the active runs (spec ant-timebox, "Warning threshold"
// and "Budget exhaustion").
func (s *AntSupervisor) Tick(ctx context.Context) error {
	s.mu.Lock()
	var ants []ports.AntID
	for _, r := range s.runs {
		if r.exec != "" {
			ants = append(ants, r.ant)
		}
	}
	s.mu.Unlock()
	var errs []error
	for _, id := range ants {
		errs = append(errs, s.tickRun(ctx, id))
	}
	return errors.Join(errs...)
}

// RunTimekeeper calls Tick every interval, TimekeeperInterval when it is
// 0 or less, until ctx ends. Errors go to the supervisor's Errors.
func (s *AntSupervisor) RunTimekeeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = TimekeeperInterval
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.clock.After(interval):
			s.report(s.Tick(ctx))
		}
	}
}

// tickRun gives one run its due warning or notice to stop. Only a
// starting or running ant is timed: the budget does not count while the
// ant waits for an extension, and a finishing ant completes its finish.
func (s *AntSupervisor) tickRun(ctx context.Context, id ports.AntID) error {
	defer s.lockAnt(id)()
	r := s.runOf(id)
	if r == nil {
		return nil
	}
	rec, err := s.store.Ant(ctx, id)
	if err != nil {
		return fmt.Errorf("reading ant %d: %w", id, err)
	}
	if st := rec.State(); st != domain.AntStarting && st != domain.AntRunning {
		return nil
	}
	now := s.clock.Now()
	b := &rec.Budget
	if out, ok := b.RunsOutAt(); ok && !now.Before(out) {
		s.mu.Lock()
		given := r.clock.stopNotice.Equal(out)
		r.clock.stopNotice = out
		s.mu.Unlock()
		var errs []error
		if !given {
			errs = append(errs, s.tell(ctx, id, domain.StopNotice(max(0, out.Add(b.Policy.Grace).Sub(now)))))
		}
		if !b.Warned {
			// The notice to stop replaces a warning not given yet.
			b.MarkWarned()
			errs = append(errs, s.saveBudget(ctx, rec))
		}
		return errors.Join(errs...)
	}
	at, ok := b.WarningAt()
	if !ok || now.Before(at) {
		return nil
	}
	text := b.WarningMessage(now)
	err = s.tell(ctx, id, text)
	b.MarkWarned()
	if serr := s.saveBudget(ctx, rec); serr != nil {
		return errors.Join(err, serr)
	}
	s.notifyAnt(ctx, ports.NotifyWarning, rec.Item, "Ant near its limit", text)
	return err
}

// tell delivers a message to the ant's active run. A run that ended
// meanwhile needs none.
func (s *AntSupervisor) tell(ctx context.Context, id ports.AntID, text string) error {
	if s.deps.Messages == nil {
		return nil
	}
	if _, err := s.deps.Messages.Deliver(ctx, id, text); err != nil && !errors.Is(err, ErrNoActiveRun) {
		return fmt.Errorf("delivering a message to ant %d: %w", id, err)
	}
	return nil
}

// saveBudget stores the budget of rec on the ant as stored now, so that
// nothing else of the ant is written back.
func (s *AntSupervisor) saveBudget(ctx context.Context, rec ports.AntRecord) error {
	fresh, err := s.store.Ant(ctx, rec.ID)
	if err != nil {
		return fmt.Errorf("reading ant %d: %w", rec.ID, err)
	}
	fresh.Budget.Warned = rec.Budget.Warned
	if err := s.store.SaveAnt(ctx, fresh); err != nil {
		return fmt.Errorf("storing ant %d: %w", rec.ID, err)
	}
	return nil
}

// runOf is the ant's active run once its process has started, or nil.
func (s *AntSupervisor) runOf(id ports.AntID) *activeRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		if r.ant == id && r.exec != "" {
			return r
		}
	}
	return nil
}
