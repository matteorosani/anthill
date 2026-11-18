package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// MaxAntDecisions is the most decisions the supervisor carries out for
// one trigger (design D22). The playbook returns Nothing well before it;
// reaching it means a fault, and the supervisor stops with
// *DecisionLimitError instead of looping.
const MaxAntDecisions = 16

// Briefs writes the first input of a run, with the answer and the stored
// changes the run is for (spec ant-execution, "Task brief", "Sessions are
// persisted for resumption"): the task brief of an ant's first run, the
// summary brief of a run that cannot resume its session, and the input
// of a run that resumes it.
type Briefs interface {
	Task(ctx context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error)
	Summary(ctx context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error)
	// Resume is the input of a run that resumes the session (BriefNone):
	// the answer and the stored changes.
	Resume(ctx context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error)
}

// The services that implement the supervisor's seams.
var (
	_ Briefs     = (*BriefBuilder)(nil)
	_ RunEnds    = (*UsageRecorder)(nil)
	_ TaskClaims = (*Claiming)(nil)
	_ RunTokens  = (*AntTools)(nil)

	_ FinishHandler = (*AntSupervisor)(nil)
	_ RunEnder      = (*AntSupervisor)(nil)
)

// TaskClaims is the part of Claiming the supervisor uses for task claims.
type TaskClaims interface {
	Acquire(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) (ports.HeldClaim, error)
	Unpark(ctx context.Context, item domain.Ref) error
	Park(ctx context.Context, item domain.Ref) error
	Release(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) error
	Held(ctx context.Context) ([]ports.HeldClaim, error)
}

// RunTokens issues and revokes the tool token of each run; AntTools
// implements it. Attach gives the token's run its process in the
// executor once it has started, for live input.
type RunTokens interface {
	Issue(s ToolSession) (string, error)
	Attach(token string, ch RunChannel)
	Revoke(token string)
	// Progress is the last progress report of the token's run, or "",
	// for the summary of a run that ended without finishing.
	Progress(token string) string
}

// EligibilityChecker re-evaluates an item's eligibility on fresh provider
// data (spec task-eligibility, "Re-check at acceptance"). own is the ant
// being started, which does not count as work on the item, or 0.
type EligibilityChecker interface {
	Recheck(ctx context.Context, item domain.Ref, own ports.AntID) (domain.WorkItem, domain.EligibilityResult, error)
}

// Answers finds the answer to an ant's pending question, for the
// playbook's snapshot. The conversation service implements it.
type Answers interface {
	Answer(ctx context.Context, ant ports.AntID) (*domain.ChangeEvent, error)
}

// RunActivity receives each line of an active run's activity, such as
// for the queen console to follow an ant. It must not block.
type RunActivity interface {
	Activity(ant ports.AntID, run ports.RunID, line string)
}

// RunEnds records the end of a run with its usage (spec ant-execution,
// "Usage is recorded"): the supervisor sets the end time, outcome,
// reason and session, and it fills in the usage from what the runner
// reported. resumedSession is the session the run resumed, or "" for a
// new session. The usage recorder implements it.
type RunEnds interface {
	EndRun(ctx context.Context, r ports.RunRecord, reported ports.Usage, resumedSession string) error
}

// DueRuns says which run is due for a queued ant that the playbook leaves
// to the services: one queued from failed, stopped or timed-out by a
// retry or a continue (domain.DecideAnt). The queen actions implement it;
// nil means no run.
type DueRuns interface {
	Due(ctx context.Context, ant ports.AntRecord) (*domain.StartRun, error)
}

// AntSupervisorStore is the part of the local store the supervisor uses.
type AntSupervisorStore interface {
	ports.SessionIndex
	ports.RunLog
	ports.StoredChanges
	ports.RunStarts
	ports.Extensions
}

// AntSupervisorDeps are the ports and services the supervisor uses.
type AntSupervisorDeps struct {
	Store       AntSupervisorStore
	Claims      TaskClaims
	Eligibility EligibilityChecker
	// Workspace makes and reuses ant branches and worktrees, and Route
	// finds an item's clone, code repository and settings.
	Workspace *Workspace
	Route     WorkspaceRoute
	Runners   *Runners
	Executor  ports.Executor
	Tokens    RunTokens
	Briefs    Briefs
	// Answers is optional until the conversation service exists: without
	// it no ant has an answer.
	Answers  Answers
	Clock    ports.Clock
	Notifier ports.Notifier
	// Capacity returns the machine's capacity: the global configuration's
	// capacity setting.
	Capacity func() int
	// Runs records the end of each run.
	Runs RunEnds
	// Tracker and CodeHost reach the item's tracker and the code host of
	// its repository, and Git counts an ant branch's new commits, for the
	// finish and the wrap-up of a run that ended without finishing.
	Tracker  TrackerRoute
	CodeHost CodeHostRoute
	Git      ports.SourceControl
	// Activity and Due are optional. Without Activity, activity lines are
	// dropped; without Due, ants queued for a retry or continue stay
	// queued.
	Activity RunActivity
	Due      DueRuns
	// Messages delivers the timekeeper's warning and notice to stop to
	// the agent of an active run, and Extensions applies the queen's
	// extension decisions; the ant tools implement both. Both are
	// optional: without Messages the timekeeper only moves deadlines, and
	// without Extensions DecideExtension fails.
	Messages   RunMessages
	Extensions ExtensionDecider
}

// AntSupervisorOptions are the settings of the supervisor.
type AntSupervisorOptions struct {
	// MCPCommand returns the command line of the ant tools server for a
	// run's tool token, such as anthill mcp --ant <token> (design D5).
	MCPCommand func(token string) []string
	// Environ is Anthill's own environment, from which each agent's is
	// made (domain.AgentEnv).
	Environ []string
	// Errors receives the errors of work the supervisor does in the
	// background, such as handling a run's events. Nil drops them.
	Errors func(error)
}

// AntSupervisor carries out the life of ants (spec ant-execution):
// acceptance, the start of each run through the executor (design D21),
// capacity and queued ants, and the events of active runs. What comes
// next for an ant is decided by the default playbook (design D22): after
// each trigger the supervisor builds the ant's snapshot, calls
// domain.DecideAnt, checks the decision against the rules it keeps
// (capacity and queued, claims), carries it out and records its effect,
// then asks again until the playbook returns Nothing.
//
// Each kind of decision has its own handler. A decision without one ends
// the trigger with *UnhandledDecisionError and has no effect.
type AntSupervisor struct {
	deps  AntSupervisorDeps
	store AntSupervisorStore
	clock ports.Clock
	opts  AntSupervisorOptions

	handlers map[reflect.Type]antHandler

	// mu guards runs, locks and items, and the outcome of each run.
	mu sync.Mutex
	// runs holds the active runs and the slots reserved for runs being
	// started, by item: an item has at most one ant that is not final.
	// It is what capacity counts.
	runs  map[domain.Ref]*activeRun
	locks map[ports.AntID]*sync.Mutex
	// items serializes the acceptances of one item.
	items map[domain.Ref]*sync.Mutex

	wg sync.WaitGroup
}

// NewAntSupervisor returns the supervisor.
func NewAntSupervisor(deps AntSupervisorDeps, opts AntSupervisorOptions) *AntSupervisor {
	s := &AntSupervisor{
		deps:     deps,
		store:    deps.Store,
		clock:    deps.Clock,
		opts:     opts,
		handlers: map[reflect.Type]antHandler{},
		runs:     map[domain.Ref]*activeRun{},
		locks:    map[ports.AntID]*sync.Mutex{},
		items:    map[domain.Ref]*sync.Mutex{},
	}
	handle(s, s.startRun)
	handle(s, s.openPR)
	handle(s, s.completeFinish)
	handle(s, s.finishWithNoChange)
	if s.deps.Due == nil {
		// The queen actions say which run is due after a retry or a
		// continue.
		s.deps.Due = s
	}
	return s
}

// antTurn is one decision being carried out: the ant's record and the
// snapshot the decision came from, read under the ant's lock.
type antTurn struct {
	rec  ports.AntRecord
	snap domain.AntSnapshot
	ws   ToolWorkspace
}

// antHandler carries out one decision and records its effect. It returns
// false when the trigger ends there without the decision's effect, such
// as a run that waits for capacity.
type antHandler func(ctx context.Context, t *antTurn, d domain.AntDecision) (bool, error)

// handle registers h as the handler of decisions of type D.
func handle[D domain.AntDecision](s *AntSupervisor, h func(ctx context.Context, t *antTurn, d D) (bool, error)) {
	s.handlers[reflect.TypeFor[D]()] = func(ctx context.Context, t *antTurn, d domain.AntDecision) (bool, error) {
		return h(ctx, t, d.(D))
	}
}

// UnhandledDecisionError is a decision the supervisor has no handler
// for. Nothing was done.
type UnhandledDecisionError struct {
	Ant      ports.AntID
	Decision domain.AntDecision
}

func (e *UnhandledDecisionError) Error() string {
	return fmt.Sprintf("ant %d: no handler for decision %T", e.Ant, e.Decision)
}

// DecisionLimitError is a trigger that reached MaxAntDecisions.
type DecisionLimitError struct {
	Ant  ports.AntID
	Last domain.AntDecision
}

func (e *DecisionLimitError) Error() string {
	return fmt.Sprintf("ant %d: %d decisions for one trigger, the last %T: stopped", e.Ant, MaxAntDecisions, e.Last)
}

// Trigger asks the playbook what comes next for the ant and carries it
// out, until the playbook returns Nothing (design D22). Services call it
// after anything that changes the ant's snapshot, such as an answer, a
// change or a run that ended.
func (s *AntSupervisor) Trigger(ctx context.Context, id ports.AntID) error {
	defer s.lockAnt(id)()
	return s.decide(ctx, id)
}

// decide is Trigger with the ant's lock held.
func (s *AntSupervisor) decide(ctx context.Context, id ports.AntID) error {
	var last domain.AntDecision
	for range MaxAntDecisions {
		rec, err := s.store.Ant(ctx, id)
		if err != nil {
			return fmt.Errorf("reading ant %d: %w", id, err)
		}
		if rec.State().Final() {
			return nil
		}
		ws, err := s.deps.Route(ctx, rec.Item)
		if err != nil {
			return fmt.Errorf("finding the workspace of %s: %w", rec.Item, err)
		}
		snap, err := s.snapshot(ctx, rec, ws.Settings)
		if err != nil {
			return err
		}
		d, err := s.decision(ctx, rec, snap)
		if err != nil {
			return err
		}
		if _, ok := d.(domain.Nothing); ok {
			return nil
		}
		h, ok := s.handlers[reflect.TypeOf(d)]
		if !ok {
			return &UnhandledDecisionError{Ant: id, Decision: d}
		}
		done, err := h(ctx, &antTurn{rec: rec, snap: snap, ws: ws}, d)
		if err != nil || !done {
			return err
		}
		last = d
	}
	return &DecisionLimitError{Ant: id, Last: last}
}

// decision asks the playbook. For an ant queued for a retry or a
// continue, which the playbook leaves to the queen actions, it asks Due.
func (s *AntSupervisor) decision(ctx context.Context, rec ports.AntRecord, snap domain.AntSnapshot) (domain.AntDecision, error) {
	d := domain.DecideAnt(snap)
	if _, ok := d.(domain.Nothing); !ok || s.deps.Due == nil || snap.State != domain.AntQueued {
		return d, nil
	}
	switch snap.QueuedFrom {
	case domain.AntFailed, domain.AntStopped, domain.AntTimedOut:
		due, err := s.deps.Due.Due(ctx, rec)
		if err != nil || due == nil {
			return d, err
		}
		return *due, nil
	}
	return d, nil
}

// snapshot builds what the playbook decides from (design D22). The
// answer to a pending question and the changes not yet routed are filled
// in by the services that own them.
func (s *AntSupervisor) snapshot(ctx context.Context, rec ports.AntRecord, settings domain.Effective) (domain.AntSnapshot, error) {
	snap := domain.AntSnapshot{
		State:           rec.State(),
		LastRun:         domain.RoleTaskRun,
		Base:            rec.Base,
		AutoFollowUp:    settings.AutoFollowUp,
		FollowUpLimit:   settings.FollowUpLimit,
		FollowUpPercent: settings.FollowUpBudget,
	}
	if snap.State == domain.AntQueued {
		snap.QueuedFrom = queuedFrom(rec.History)
	}
	runs, err := s.store.Runs(ctx, rec.ID)
	if err != nil {
		return domain.AntSnapshot{}, fmt.Errorf("reading the runs of ant %d: %w", rec.ID, err)
	}
	snap.Runs = len(runs)
	for _, r := range runs {
		if r.Kind == ports.RunFollowUp {
			snap.FollowUpRuns++
		}
	}
	if len(runs) > 0 {
		snap.LastRun = roleOf(runs[len(runs)-1].Kind)
	}
	if caps, ok := s.deps.Runners.Capabilities(domain.Runner(rec.Runner)); ok {
		snap.Resumable = rec.SessionID != "" && caps.Resume
	}
	if rec.PR != nil {
		snap.PR = &domain.AntPR{Ref: *rec.PR}
	}
	if snap.State == domain.AntFinishing && rec.Finish != nil {
		if err := s.finishSnapshot(ctx, rec, runs, &snap); err != nil {
			return domain.AntSnapshot{}, err
		}
	}
	if s.deps.Answers != nil && (snap.State == domain.AntAwaitingInput || snap.QueuedFrom == domain.AntAwaitingInput) {
		if snap.Answer, err = s.deps.Answers.Answer(ctx, rec.ID); err != nil {
			return domain.AntSnapshot{}, fmt.Errorf("reading the answer of ant %d: %w", rec.ID, err)
		}
	}
	stored, err := s.store.StoredChanges(ctx, rec.ID)
	if err != nil {
		return domain.AntSnapshot{}, fmt.Errorf("reading the stored changes of ant %d: %w", rec.ID, err)
	}
	for _, c := range stored {
		snap.Stored = append(snap.Stored, domain.AntChange{ID: c.ID, Event: c.Event})
	}
	return snap, nil
}

// queuedFrom is the between-runs state a queued ant was queued from, or
// "" for an ant queued at acceptance.
func queuedFrom(history []domain.Transition) domain.AntState {
	if len(history) == 0 {
		return ""
	}
	return history[len(history)-1].From
}

func roleOf(k ports.RunKind) domain.AgentRole {
	if k == ports.RunFollowUp {
		return domain.RoleFollowUpRun
	}
	return domain.RoleTaskRun
}

func kindOf(r domain.AgentRole) ports.RunKind {
	if r == domain.RoleFollowUpRun {
		return ports.RunFollowUp
	}
	return ports.RunTask
}

// lockAnt serializes the work on one ant: its triggers and the events of
// its runs.
func (s *AntSupervisor) lockAnt(id ports.AntID) func() {
	s.mu.Lock()
	l, ok := s.locks[id]
	if !ok {
		l = &sync.Mutex{}
		s.locks[id] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// lockItem serializes the acceptances of one item.
func (s *AntSupervisor) lockItem(item domain.Ref) func() {
	s.mu.Lock()
	l, ok := s.items[item]
	if !ok {
		l = &sync.Mutex{}
		s.items[item] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// transition moves the ant to a new state and stores it.
func (s *AntSupervisor) transition(ctx context.Context, rec ports.AntRecord, to domain.AntState, reason string) (ports.AntRecord, error) {
	ant, err := domain.RestoreAnt(rec.Item, rec.Base, rec.Budget, rec.History)
	if err != nil {
		return rec, err
	}
	if err := ant.Transition(to, s.clock.Now(), reason); err != nil {
		return rec, err
	}
	rec.History, rec.Budget = ant.History(), ant.Budget
	if err := s.store.SaveAnt(ctx, rec); err != nil {
		return rec, fmt.Errorf("storing ant %d: %w", rec.ID, err)
	}
	return rec, nil
}

// report hands an error of background work to Errors.
func (s *AntSupervisor) report(err error) {
	if err != nil && s.opts.Errors != nil {
		s.opts.Errors(err)
	}
}

// notifyAnt tells the queen about an item's ant. A notifier that fails is
// only reported: the state is already recorded, and the console shows
// it.
func (s *AntSupervisor) notifyAnt(ctx context.Context, kind domain.NotificationKind, item domain.Ref, title, body string) {
	if s.deps.Notifier == nil {
		return
	}
	s.report(s.deps.Notifier.Notify(ctx, ports.Notification{Kind: kind, Title: title, Body: item.String() + ": " + body, Ref: &item}))
}

// Wait returns once the events of every run started so far are handled,
// such as when the daemon shuts down after ending its runs.
func (s *AntSupervisor) Wait() {
	s.wg.Wait()
}

// ActiveRuns returns the items whose ant has an active run or a run being
// started, which is what capacity counts.
func (s *AntSupervisor) ActiveRuns() []domain.Ref {
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := make([]domain.Ref, 0, len(s.runs))
	for ref := range s.runs {
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(a, b domain.Ref) int { return strings.Compare(a.String(), b.String()) })
	return refs
}

// errCapacityFull is returned by reserve when no slot is free.
var errCapacityFull = errors.New("capacity full")

// ReasonCapacityFull is the reason of a move to queued.
const ReasonCapacityFull = "capacity full"

// deadline is when the executor ends a run: the budget left plus the
// grace period (spec ant-timebox). The timekeeper moves it later.
func deadline(now time.Time, remaining, grace time.Duration) time.Time {
	return now.Add(remaining + grace)
}
