package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// activeRun is a run whose process is alive, or a slot reserved for a run
// being started. Capacity counts both.
type activeRun struct {
	item domain.Ref
	repo domain.Repository
	// The fields below are set once the run has started. Only the run's
	// event reader changes them after that, apart from outcome and
	// reason, which mu guards.
	ant   ports.AntID
	run   ports.RunRecord
	exec  ports.ExecID
	token string
	// resumed is the session the run resumed, or "".
	resumed string
	usage   ports.Usage
	// lastMessage is the agent's last message, as the executor reported
	// it, not yet made safe to post.
	lastMessage string
	outcome     ports.RunOutcome
	reason      string
	// clock is the timekeeper's record of the run, which mu guards.
	clock runClock
}

// reserve takes a slot for a run of the item, within the machine's
// capacity and the capacity of the item's repository, which is a limit
// within the machine's (spec ant-execution, "Capacity limits active
// runs"). It fails with errCapacityFull when either is reached.
func (s *AntSupervisor) reserve(item domain.Ref, ws ToolWorkspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[item]; ok {
		return fmt.Errorf("%s already has an active run", item)
	}
	repo := 0
	for _, r := range s.runs {
		if r.repo == ws.Repo {
			repo++
		}
	}
	if len(s.runs) >= s.machineCapacity() || repo >= max(ws.Settings.Capacity, 1) {
		return errCapacityFull
	}
	s.runs[item] = &activeRun{item: item, repo: ws.Repo}
	return nil
}

// unreserve gives back the slot of a run that did not start or has ended.
func (s *AntSupervisor) unreserve(item domain.Ref) {
	s.mu.Lock()
	delete(s.runs, item)
	s.mu.Unlock()
}

// freeSlot reports whether the machine has a free slot.
func (s *AntSupervisor) freeSlot() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs) < s.machineCapacity()
}

func (s *AntSupervisor) machineCapacity() int {
	if s.deps.Capacity == nil {
		return domain.DefaultCapacity
	}
	return max(s.deps.Capacity(), 1)
}

// queue makes an ant whose run is due wait for capacity. An ant already
// queued stays so.
func (s *AntSupervisor) queue(ctx context.Context, rec ports.AntRecord) error {
	if rec.State() == domain.AntQueued {
		return nil
	}
	_, err := s.transition(ctx, rec, domain.AntQueued, ReasonCapacityFull)
	return err
}

// startRun carries out a StartRun decision: within capacity, the run
// starts; otherwise the ant is queued and its run starts when a slot
// frees, if it is still due (spec ant-execution, "Capacity limits active
// runs"). An ant's first run, for an ant queued at acceptance, first
// does the rest of the acceptance: the re-check, the claim, the branch
// and the worktree. A later run renews the parked claim and reuses the
// worktree, recreating it when it is gone.
func (s *AntSupervisor) startRun(ctx context.Context, t *antTurn, d domain.StartRun) (bool, error) {
	rec := t.rec
	if !rec.State().BetweenRuns() {
		return false, fmt.Errorf("ant %d is %s: a run starts only between runs", rec.ID, rec.State())
	}
	// No run starts while the item is on hold: the ant stays as it is,
	// queued included, and its run starts once the label is removed, if
	// it is still due (spec change-reaction, "Hold freezes the ant").
	if held, err := s.onHold(ctx, rec.Item); err != nil || held {
		return false, err
	}
	if err := s.reserve(rec.Item, t.ws); err != nil {
		if errors.Is(err, errCapacityFull) {
			return false, s.queue(ctx, rec)
		}
		return false, err
	}
	var (
		ws   AntWorkspace
		undo func(context.Context) error
		err  error
	)
	if t.snap.Runs == 0 {
		rec, ws, undo, err = s.prepareFirst(ctx, rec, t.ws)
	} else {
		rec, ws, err = s.prepareNext(ctx, rec, t.ws)
	}
	if err != nil || rec.State().Final() {
		s.unreserve(rec.Item)
		return false, err
	}
	if err := s.launch(ctx, rec, t.ws, ws, d); err != nil {
		s.unreserve(rec.Item)
		return false, s.startFailed(ctx, rec, t.snap.Runs == 0, undo, err)
	}
	return true, nil
}

// prepareFirst does the acceptance steps for an ant queued at
// acceptance, once a slot is free: it is claimed and started only if it
// is still eligible (spec ant-execution, "Queued for capacity"). An ant
// that cannot start becomes abandoned with the reason, and the queen is
// told.
func (s *AntSupervisor) prepareFirst(ctx context.Context, rec ports.AntRecord, tw ToolWorkspace) (ports.AntRecord, AntWorkspace, func(context.Context) error, error) {
	p, err := s.acquire(ctx, rec, tw)
	if err != nil {
		if !refusesStart(err) {
			// The ant stays queued, and the next freed slot tries again.
			return rec, AntWorkspace{}, nil, err
		}
		reason := err.Error()
		rec, err = s.transition(ctx, rec, domain.AntAbandoned, reason)
		if err == nil {
			s.notifyAnt(ctx, domain.NotifyFailed, rec.Item, "Queued ant not started", reason)
		}
		return rec, AntWorkspace{}, nil, err
	}
	rec.Runner, rec.Branch, rec.Worktree, rec.BaseBranch = string(p.runner), p.ws.Branch, p.ws.Worktree, p.ws.Base
	if err := s.store.SaveAnt(ctx, rec); err != nil {
		return rec, AntWorkspace{}, nil, errors.Join(fmt.Errorf("storing ant %d: %w", rec.ID, err), p.undo(ctx))
	}
	return rec, p.ws, p.undo, nil
}

// prepareNext renews the parked task claim and reuses the ant's branch
// and worktree. A claim lost meanwhile has already ended the ant (spec
// work-claiming), so the run does not start.
func (s *AntSupervisor) prepareNext(ctx context.Context, rec ports.AntRecord, tw ToolWorkspace) (ports.AntRecord, AntWorkspace, error) {
	if err := s.deps.Claims.Unpark(ctx, rec.Item); err != nil {
		var taken *ClaimTakenError
		if errors.As(err, &taken) {
			fresh, rerr := s.store.Ant(ctx, rec.ID)
			return fresh, AntWorkspace{}, rerr
		}
		return rec, AntWorkspace{}, fmt.Errorf("renewing the claim on %s: %w", rec.Item, err)
	}
	instance, err := s.instance(ctx, rec.Item)
	if err != nil {
		return rec, AntWorkspace{}, err
	}
	settings := tw.Settings
	settings.BaseBranch = rec.BaseBranch
	ws, err := s.deps.Workspace.Prepare(ctx, PrepareRequest{
		Clone: tw.Clone, Repo: tw.Repo, Item: rec.Item, Settings: settings,
		Trailers: domain.NewTrailers(rec.Item, domain.Runner(rec.Runner), instance),
		Branch:   rec.Branch, Worktree: rec.Worktree,
	})
	if err != nil {
		return rec, AntWorkspace{}, fmt.Errorf("preparing the worktree of %s: %w", rec.Item, err)
	}
	rec.Worktree = ws.Worktree
	return rec, ws, nil
}

// instance is the instance id that holds the item's task claim.
func (s *AntSupervisor) instance(ctx context.Context, item domain.Ref) (string, error) {
	held, err := s.deps.Claims.Held(ctx)
	if err != nil {
		return "", fmt.Errorf("reading the held claims: %w", err)
	}
	for _, h := range held {
		if h.Ref == item && h.Kind == domain.ClaimTask {
			return h.Instance, nil
		}
	}
	return "", fmt.Errorf("%s", domain.MessageClaimNotHeld(item))
}

// launch starts the run: it moves the ant to starting, writes the run's
// input, records the start, issues the tool token and starts the process
// through the executor (design D21). Recording the start in one store
// transaction marks the stored changes given to the run with it (design
// D22).
func (s *AntSupervisor) launch(ctx context.Context, rec ports.AntRecord, tw ToolWorkspace, ws AntWorkspace, d domain.StartRun) error {
	now := s.clock.Now()
	ant, err := domain.RestoreAnt(rec.Item, rec.Base, rec.Budget, rec.History)
	if err != nil {
		return err
	}
	if d.NewBudget > 0 {
		// An open extension request is closed with the old budget; the
		// timekeeper's records show the new budget.
		if _, err := ant.StartNewBudget(d.NewBudget); err != nil {
			return err
		}
	}
	if ant.State() != domain.AntStarting {
		if err := ant.Transition(domain.AntStarting, now, startReason(d)); err != nil {
			return err
		}
	}
	rec.History, rec.Budget = ant.History(), ant.Budget
	// The start closes the follow-up proposal in the same transaction.
	rec.FollowUpProposal = domain.ProposalNone

	input, err := s.input(ctx, rec, d)
	if err != nil {
		return err
	}
	session := ""
	if d.Brief == domain.BriefNone {
		session = rec.SessionID
	}
	run := ports.RunRecord{Ant: rec.ID, Kind: kindOf(d.Kind), SessionID: session, StartedAt: now, NewBudget: d.NewBudget}
	ids := make([]int64, len(d.Changes))
	for i, c := range d.Changes {
		ids[i] = c.ID
	}
	run.ID, err = s.store.BeginRun(ctx, ports.RunStart{Ant: rec, Run: run, Consumed: ids, At: now})
	if err != nil {
		return fmt.Errorf("recording the start of a run of ant %d: %w", rec.ID, err)
	}
	r := &activeRun{item: rec.Item, repo: tw.Repo, ant: rec.ID, run: run, resumed: session}

	token, err := s.deps.Tokens.Issue(ToolSession{Role: d.Kind, Ant: rec.ID, Run: run.ID})
	if err != nil {
		return s.endUnstarted(ctx, r, err)
	}
	r.token = token
	var mcp []string
	if s.opts.MCPCommand != nil {
		mcp = s.opts.MCPCommand(token)
	}
	spec := ports.ExecSpec{
		Runner:  domain.Runner(rec.Runner),
		Session: session,
		Run: ports.RunSpec{
			Worktree:   ws.Worktree,
			Brief:      input,
			Budget:     ant.Budget.Remaining(now),
			MCPCommand: mcp,
			ToolToken:  token,
			Env:        domain.AgentEnv(s.opts.Environ),
		},
		Deadline: deadline(now, ant.Budget.Remaining(now), tw.Settings.GracePeriod),
	}
	spec.Run.Telemetry = s.startRunTelemetry(ctx, rec, tw, run.ID, d.Kind)
	exec, err := s.deps.Executor.Start(ctx, spec)
	if err != nil {
		s.deps.Tokens.Revoke(token)
		return s.endUnstarted(ctx, r, err)
	}
	r.exec = exec.ID
	r.clock.deadline = spec.Deadline
	s.deps.Tokens.Attach(token, RunChannel{Capabilities: exec.Capabilities, Exec: exec.ID})
	s.mu.Lock()
	s.runs[rec.Item] = r
	s.mu.Unlock()
	s.wg.Add(1)
	go s.watch(context.WithoutCancel(ctx), r, exec.Events)
	if d.Kind == domain.RoleFollowUpRun && d.NewBudget > 0 {
		s.notifyAnt(ctx, domain.NotifyFollowUpStarted, rec.Item, "Follow-up run started", describeChanges(d.Changes))
	}
	return nil
}

// endUnstarted records the end of a run whose process never started,
// and returns why.
func (s *AntSupervisor) endUnstarted(ctx context.Context, r *activeRun, cause error) error {
	r.run.EndedAt, r.run.Outcome, r.run.Reason = s.clock.Now(), ports.OutcomeFailed, ReasonStartFailed+": "+cause.Error()
	return errors.Join(cause, s.deps.Runs.EndRun(ctx, r.run, ports.Usage{}, r.resumed))
}

// ReasonStartFailed starts the reason of an ant whose run could not be
// started.
const ReasonStartFailed = "start failed"

// startFailed undoes a run that could not start. A first run undoes the
// whole acceptance: the worktree and the branch it made, and the claim;
// the ant becomes abandoned. A later run makes the ant failed, keeping
// its claim and worktree, so the queen can retry.
func (s *AntSupervisor) startFailed(ctx context.Context, rec ports.AntRecord, first bool, undo func(context.Context) error, cause error) error {
	fresh, err := s.store.Ant(ctx, rec.ID)
	if err != nil {
		return errors.Join(cause, err)
	}
	reason := ReasonStartFailed + ": " + cause.Error()
	to := domain.AntFailed
	if first {
		to = domain.AntAbandoned
	}
	var errs []error
	if fresh.State() != to && domain.CanTransition(fresh.State(), to) {
		_, err = s.transition(ctx, fresh, to, reason)
		errs = append(errs, err)
	}
	if first && undo != nil {
		errs = append(errs, undo(ctx))
	}
	s.notifyAnt(ctx, domain.NotifyFailed, rec.Item, "Run not started", reason)
	return errors.Join(append([]error{cause}, errs...)...)
}

// startReason is the reason of the move to starting.
func startReason(d domain.StartRun) string {
	switch {
	case d.Answer != nil:
		return "run due: answer"
	case d.Kind == domain.RoleFollowUpRun:
		return "run due: follow-up"
	case d.Brief == domain.BriefTask:
		return "run due: accepted"
	}
	return "run due"
}

// input is the first input of the run, with the answer and the stored
// changes: the task brief, the summary brief, or the input of a run that
// resumes its session.
func (s *AntSupervisor) input(ctx context.Context, rec ports.AntRecord, d domain.StartRun) (string, error) {
	var (
		in  string
		err error
	)
	switch d.Brief {
	case domain.BriefTask:
		in, err = s.deps.Briefs.Task(ctx, rec, d.Answer, d.Changes)
	case domain.BriefSummary:
		in, err = s.deps.Briefs.Summary(ctx, rec, d.Answer, d.Changes)
	default:
		in, err = s.deps.Briefs.Resume(ctx, rec, d.Answer, d.Changes)
	}
	if err != nil {
		return "", fmt.Errorf("writing the input of a run of %s: %w", rec.Item, err)
	}
	if d.Note != "" {
		in = d.Note + "\n\n" + in
	}
	return in, nil
}

// slotFreed starts the runs of queued ants while slots are free, in the
// order they were queued, if their run is still due (spec ant-execution,
// "Capacity limits active runs"). An ant whose run is no longer due stays
// queued, as the transition table has no way back to the state it was
// queued from. In practice that is an ant on hold, which hold freezes:
// once the hold label is removed, its run starts if it is still due. An
// ant queued at acceptance that is no longer eligible becomes abandoned
// with the unmet prerequisites (see prepareFirst).
func (s *AntSupervisor) slotFreed(ctx context.Context) {
	if s.isDetached() {
		// The daemon is stopping: the queued ants start after the
		// restart (Reconcile).
		return
	}
	queued, err := s.store.Ants(ctx, domain.AntQueued)
	if err != nil {
		s.reportUnlessDone(ctx, fmt.Errorf("listing the queued ants: %w", err))
		return
	}
	slices.SortStableFunc(queued, func(a, b ports.AntRecord) int {
		return a.History[len(a.History)-1].At.Compare(b.History[len(b.History)-1].At)
	})
	for _, rec := range queued {
		if !s.freeSlot() {
			return
		}
		s.reportUnlessDone(ctx, s.Trigger(ctx, rec.ID))
	}
}
