package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// MaxPRDecisions is the most decisions the shepherd carries out for one
// trigger on a PR (design D22). Reaching it means a fault, and the
// shepherd stops with *PRDecisionLimitError instead of looping.
const MaxPRDecisions = 16

// ReviewClaims is the part of Claiming the shepherd uses for review
// claims.
type ReviewClaims interface {
	Acquire(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) (ports.HeldClaim, error)
	Release(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) error
}

// RoundEnds records the end of a round with its usage (spec
// ant-execution, "Usage is recorded"); the usage recorder implements it.
type RoundEnds interface {
	EndRound(ctx context.Context, r ports.RoundRecord, reported ports.Usage) error
}

// CommandAcks posts the acknowledgement of a command, the reply with
// marker ID cmd-<commentID> that makes it run once across
// instances (spec agent-conversation, "Commands run once"). The command
// service implements it; the acknowledgement is idempotent.
type CommandAcks interface {
	Acknowledge(ctx context.Context, on domain.Ref, commentID, text string) error
}

// WriteAccess reports whether login has write access to repo, so that a
// forged marker on a round's summary or a command's acknowledgement does
// not count (design, Risks: forged markers).
type WriteAccess func(ctx context.Context, repo domain.Repository, login string) (bool, error)

// The services that implement the shepherd's seams.
var (
	_ ReviewClaims = (*Claiming)(nil)
	_ RoundEnds    = (*UsageRecorder)(nil)
)

// ShepherdStore is the part of the local store the shepherd uses: its
// rounds and their requests, the pushes of its fix passes, and the
// session index to find the ant of an ant PR.
type ShepherdStore interface {
	ports.SessionIndex
	ports.RunLog
	ports.RoundRequests
	ports.FixPushes
}

// ShepherdDeps are the ports and services the shepherd uses.
type ShepherdDeps struct {
	Store  ShepherdStore
	Claims ReviewClaims
	// Route finds a PR's repository and its effective settings, and
	// CodeHost reaches the code host of the repository.
	Route    WorkspaceRoute
	CodeHost CodeHostRoute
	// WriteAccess checks the authors of round summaries and
	// acknowledgements.
	WriteAccess WriteAccess
	// Instance is this instance's id on the provider of a repository, as
	// its markers carry it.
	Instance func(repo domain.Repository) string
	Runners  *Runners
	// Rounds runs each round, and RoundEnds records its end.
	Rounds    RoundRunner
	RoundEnds RoundEnds
	// Fixes runs each fix pass (spec pr-shepherd, "Fix agent pass").
	Fixes FixPassRunner
	// Acks acknowledges the /anthill review that a requested round
	// serves.
	Acks CommandAcks
	// Ants takes the findings handed to the ant of an ant PR of this
	// instance (spec change-reaction, "Shepherd findings on ant PRs").
	Ants  FindingsRouter
	Clock ports.Clock
	// Errors receives the errors of work the shepherd does in the
	// background: a round and the decisions after it. Nil drops them.
	Errors func(error)
}

// Shepherd reviews PRs (spec pr-shepherd). What comes next for a PR is
// decided by the default playbook (design D22): on each trigger the
// shepherd builds the PR's snapshot, calls domain.DecidePR, checks the
// decision against the rules it keeps (participation, the review runner,
// the review claim), carries it out and records its effect, then asks
// again until the playbook returns Nothing.
//
// Each kind of decision has its own handler, as in AntSupervisor. A
// decision without one ends the trigger with *UnhandledPRDecisionError
// and has no effect.
//
// A round runs in the background, in a goroutine the shepherd owns, with
// the review claim held. When it ends, the shepherd asks the playbook
// again under the same claim, so that a fix pass decided after the round
// runs within the round's claim (spec pr-shepherd, "Fix agent pass"), and
// releases the claim once the playbook returns Nothing.
type Shepherd struct {
	deps     ShepherdDeps
	store    ShepherdStore
	clock    ports.Clock
	handlers map[reflect.Type]prHandler

	// mu guards locks and active.
	mu    sync.Mutex
	locks map[domain.Ref]*sync.Mutex
	// active holds the PRs with a round in progress on this instance.
	active map[domain.Ref]bool
	// handed holds the rounds this instance handed to its ants.
	handed handedRounds
	// cancels ends the work in progress on a PR when it is merged or
	// closed (StopPR).
	cancels map[domain.Ref]context.CancelFunc

	wg sync.WaitGroup
}

// NewShepherd returns the shepherd.
func NewShepherd(deps ShepherdDeps) *Shepherd {
	s := &Shepherd{
		deps:     deps,
		store:    deps.Store,
		clock:    deps.Clock,
		handlers: map[reflect.Type]prHandler{},
		locks:    map[domain.Ref]*sync.Mutex{},
		active:   map[domain.Ref]bool{},
		cancels:  map[domain.Ref]context.CancelFunc{},
	}
	handlePR(s, true, s.startRound)
	handlePR(s, true, s.postRoundLimit)
	handlePR(s, false, s.handFindings)
	handlePR(s, true, s.postAutofixUnavailable)
	handlePR(s, true, s.startFixPass)
	return s
}

// prTurn is one decision being carried out: the PR as just read, the
// snapshot the decision came from, the repository's workspace and code
// host, and the review claim held.
type prTurn struct {
	pr   domain.PullRequest
	snap domain.PRSnapshot
	ws   ToolWorkspace
	host ports.CodeHostPort
	held ports.HeldClaim
	// handedOff is set by a handler that passed the review claim on to
	// work it started in the background, which releases it.
	handedOff bool
}

// prHandler carries out one decision and records its effect. It returns
// false when the trigger ends there, such as after starting a round.
type prHandler struct {
	// claim is set for the decisions that need the review claim: rounds,
	// the round limit comment and fix passes (spec pr-shepherd, "Review
	// rounds", "Fix agent pass").
	claim bool
	run   func(ctx context.Context, t *prTurn, d domain.PRDecision) (bool, error)
}

// handlePR registers h as the handler of decisions of type D.
func handlePR[D domain.PRDecision](s *Shepherd, claim bool, h func(ctx context.Context, t *prTurn, d D) (bool, error)) {
	s.handlers[reflect.TypeFor[D]()] = prHandler{claim: claim, run: func(ctx context.Context, t *prTurn, d domain.PRDecision) (bool, error) {
		return h(ctx, t, d.(D))
	}}
}

// UnhandledPRDecisionError is a decision the shepherd has no handler
// for. Nothing was done.
type UnhandledPRDecisionError struct {
	PR       domain.Ref
	Decision domain.PRDecision
}

func (e *UnhandledPRDecisionError) Error() string {
	return fmt.Sprintf("%s: no handler for decision %T", e.PR, e.Decision)
}

// PRDecisionLimitError is a trigger that reached MaxPRDecisions.
type PRDecisionLimitError struct {
	PR   domain.Ref
	Last domain.PRDecision
}

func (e *PRDecisionLimitError) Error() string {
	return fmt.Sprintf("%s: %d decisions for one trigger, the last %T: stopped", e.PR, MaxPRDecisions, e.Last)
}

// NotReviewingError reports that this instance takes no review claims in
// a repository (spec pr-shepherd, "Shepherd participation", "Review
// runner"): reviewing is off for the machine or the repository, or the
// review runner is not available here.
type NotReviewingError struct {
	Repo domain.Repository
	// Runner is set when the review runner is missing, with why.
	Runner domain.Runner
	Err    error
}

func (e *NotReviewingError) Error() string {
	if e.Runner != "" {
		return fmt.Sprintf("not reviewing in %s: review runner %s not available", e.Repo, e.Runner)
	}
	return fmt.Sprintf("not reviewing in %s: reviewing is off on this machine", e.Repo)
}

func (e *NotReviewingError) Unwrap() error {
	return e.Err
}

// CanReview returns nil when this instance takes review claims in the
// repository of pr, and *NotReviewingError when it does not. The command
// service calls it before it acknowledges /anthill review, so that an
// instance that cannot review leaves the command to one that can.
func (s *Shepherd) CanReview(ctx context.Context, pr domain.Ref) error {
	ws, err := s.deps.Route(ctx, pr)
	if err != nil {
		return fmt.Errorf("finding the repository of %s: %w", pr, err)
	}
	return s.participates(ctx, ws)
}

// participates checks the participation settings and the review runner.
func (s *Shepherd) participates(ctx context.Context, ws ToolWorkspace) error {
	if !ws.Settings.Reviewing {
		return &NotReviewingError{Repo: ws.Repo}
	}
	if name, _, err := s.deps.Runners.SelectReview(ctx, ws.Settings); err != nil {
		return &NotReviewingError{Repo: ws.Repo, Runner: name, Err: err}
	}
	return nil
}

// RequestRound records a /anthill review on pr, made in the comment
// commentID, and triggers the shepherd on the PR (spec pr-shepherd,
// "Developer-requested rounds"). A request already recorded for the
// comment is ignored, so a repeated call is harmless. When this instance
// cannot review the PR, it records nothing and returns *NotReviewingError.
// It returns without waiting for the round.
func (s *Shepherd) RequestRound(ctx context.Context, pr domain.Ref, commentID string) error {
	if err := s.CanReview(ctx, pr); err != nil {
		return err
	}
	if err := s.store.RequestRound(ctx, ports.RoundRequest{PR: pr, CommentID: commentID, At: s.clock.Now()}); err != nil {
		return fmt.Errorf("recording the round request on %s: %w", pr, err)
	}
	return s.Trigger(ctx, pr)
}

// Trigger asks the playbook what comes next for pr and carries it out,
// until the playbook returns Nothing or a round starts (design D22). The
// daemon calls it for each open PR of a configured repository after a
// poll, and RequestRound after a request. It never waits for a round: a
// round runs in the background, and while one runs on the PR, Trigger
// does nothing, because the playbook is asked again when it ends.
//
// The review claim is taken before the first decision that needs it.
// After the claim is taken, the PR is read and decided again, so a round
// another instance just finished is seen. A claim held by another
// instance ends the trigger without error: that instance reviews. So
// does an instance that does not take part in reviews.
func (s *Shepherd) Trigger(ctx context.Context, pr domain.Ref) error {
	defer s.lockPR(pr)()
	if s.roundActive(pr) {
		return nil
	}
	return s.decide(ctx, pr, nil)
}

// decide runs the playbook loop on pr with the PR's lock held. held is
// the review claim already held, or nil. The claim is released when the
// loop ends, unless a handler handed it to a round.
func (s *Shepherd) decide(ctx context.Context, pr domain.Ref, held *ports.HeldClaim) (err error) {
	defer func() {
		if held != nil {
			if rerr := s.deps.Claims.Release(context.WithoutCancel(ctx), pr, domain.ClaimReview); rerr != nil {
				err = errors.Join(err, fmt.Errorf("releasing the review claim on %s: %w", pr, rerr))
			}
		}
	}()
	ws, err := s.deps.Route(ctx, pr)
	if err != nil {
		return fmt.Errorf("finding the repository of %s: %w", pr, err)
	}
	host, err := s.deps.CodeHost(ctx, pr.Repo)
	if err != nil {
		return err
	}
	var last domain.PRDecision
	for range MaxPRDecisions {
		p, _, err := host.PullRequest(ctx, pr)
		if err != nil {
			return fmt.Errorf("reading %s: %w", pr, err)
		}
		if p.State.Ended() {
			// Nothing more on a merged or closed PR (spec pr-shepherd, "PR
			// closure is human").
			return nil
		}
		snap, err := s.snapshot(ctx, p, ws)
		if err != nil {
			return err
		}
		d := domain.DecidePR(snap)
		if _, ok := d.(domain.Nothing); ok {
			return nil
		}
		h, ok := s.handlers[reflect.TypeOf(d)]
		if !ok {
			return &UnhandledPRDecisionError{PR: pr, Decision: d}
		}
		if h.claim && held == nil {
			c, err := s.claim(ctx, ws, pr)
			if err != nil || c == nil {
				return err
			}
			held = c
			// Decide again under the claim.
			continue
		}
		t := &prTurn{pr: p, snap: snap, ws: ws, host: host}
		if held != nil {
			t.held = *held
		}
		done, err := h.run(ctx, t, d)
		if t.handedOff {
			held = nil
		}
		if err != nil || !done {
			return err
		}
		last = d
	}
	return &PRDecisionLimitError{PR: pr, Last: last}
}

// claim takes the review claim on pr, or returns nil when this instance
// does not take part in reviews or another instance holds the claim.
func (s *Shepherd) claim(ctx context.Context, ws ToolWorkspace, pr domain.Ref) (*ports.HeldClaim, error) {
	if err := s.participates(ctx, ws); err != nil {
		return nil, nil //nolint:nilerr // Not reviewing here is not a fault: another instance reviews.
	}
	h, err := s.deps.Claims.Acquire(ctx, pr, domain.ClaimReview)
	var taken *ClaimTakenError
	switch {
	case errors.As(err, &taken):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("claiming %s for review: %w", pr, err)
	}
	return &h, nil
}

// roundActive reports whether a round runs on pr on this instance.
func (s *Shepherd) roundActive(pr domain.Ref) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[pr]
}

func (s *Shepherd) setActive(pr domain.Ref, active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if active {
		s.active[pr] = true
	} else {
		delete(s.active, pr)
		if cancel, ok := s.cancels[pr]; ok {
			cancel()
			delete(s.cancels, pr)
		}
	}
}

// report hands an error of background work to Errors.
func (s *Shepherd) report(err error) {
	if err != nil && s.deps.Errors != nil {
		s.deps.Errors(err)
	}
}

// Wait returns once every round started so far, and the decisions after
// it, are done, such as when the daemon shuts down.
func (s *Shepherd) Wait() {
	s.wg.Wait()
}

// lockPR serializes the work on one PR: its triggers and the decisions
// after each of its rounds.
func (s *Shepherd) lockPR(pr domain.Ref) func() {
	s.mu.Lock()
	l, ok := s.locks[pr]
	if !ok {
		l = &sync.Mutex{}
		s.locks[pr] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}
