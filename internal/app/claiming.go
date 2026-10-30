package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// ReasonClaimLost is the reason of an ant's move to abandoned when its
// task claim was lost or force-released (spec work-claiming, "Holder
// returns after expiry").
const ReasonClaimLost = "claim lost"

// ClaimProvider is what Claiming needs of one provider connection: its
// claims, and who has write access, because only the claims and release
// commands of users with write access count. The GitHub and Azure DevOps
// adapters and the fake provider's clients all implement it.
type ClaimProvider interface {
	ports.ClaimStore
	CanWrite(ctx context.Context, repo domain.Repository, login string) (bool, error)
}

// ClaimTarget is where and as whom an item or PR is claimed.
type ClaimTarget struct {
	Provider ClaimProvider
	// Instance is this instance's id on that provider, such as
	// mrosani@LAPTOP-42.
	Instance string
	// Label is the claim label of the item's repository.
	Label string
}

// ClaimRoute finds the target of an item or PR. The daemon builds it from
// the provider connections and the configuration.
type ClaimRoute func(ctx context.Context, ref domain.Ref) (ClaimTarget, error)

// ClaimStateStore is the part of the local store Claiming uses: the held
// claims, and the session index to make an ant abandoned.
type ClaimStateStore interface {
	ports.HeldClaims
	ports.SessionIndex
}

// ClaimingOptions are the timings of the claim protocol.
type ClaimingOptions struct {
	// Lease is the lease of new claims (default domain.DefaultLease).
	Lease time.Duration
	// RereadDelay is the wait between posting a claim comment and
	// re-reading the claims (default domain.DefaultClaimRereadDelay).
	RereadDelay time.Duration
}

// ClaimTakenError is a claim that could not be acquired because another
// instance holds it or won the race (spec work-claiming, "Two instances
// claim simultaneously"; spec ant-execution, "Claim lost at start").
type ClaimTakenError struct {
	Ref domain.Ref
	// Holder is the instance holding the claim, or "" when the race was
	// lost to a change that named no holder, such as an Azure DevOps
	// revision conflict.
	Holder string
}

func (e *ClaimTakenError) Error() string {
	if e.Holder == "" {
		return fmt.Sprintf("lost the claim race on %s", e.Ref)
	}
	return "claimed by " + e.Holder
}

// Claiming runs the claim protocol of work-claiming (design D3) for this
// instance: acquisition, renewal, parking, release, and the detection of
// lost and force-released claims. It depends only on ports.
type Claiming struct {
	route    ClaimRoute
	store    ClaimStateStore
	clock    ports.Clock
	notifier ports.Notifier
	loss     ports.ClaimLossHandler
	opts     ClaimingOptions

	mu    sync.Mutex
	locks map[heldKey]*sync.Mutex
}

type heldKey struct {
	ref  domain.Ref
	kind domain.ClaimKind
}

// NewClaiming returns the claim service. Zero options take their
// defaults.
func NewClaiming(route ClaimRoute, store ClaimStateStore, clock ports.Clock, notifier ports.Notifier, loss ports.ClaimLossHandler, opts ClaimingOptions) *Claiming {
	if opts.Lease <= 0 {
		opts.Lease = domain.DefaultLease
	}
	if opts.RereadDelay <= 0 {
		opts.RereadDelay = domain.DefaultClaimRereadDelay
	}
	return &Claiming{route: route, store: store, clock: clock, notifier: notifier, loss: loss, opts: opts, locks: map[heldKey]*sync.Mutex{}}
}

// lock serializes the claim operations on one claim, so that a renewal
// never runs during the acquisition or release of the same claim.
func (c *Claiming) lock(ref domain.Ref, kind domain.ClaimKind) func() {
	c.mu.Lock()
	l, ok := c.locks[heldKey{ref, kind}]
	if !ok {
		l = &sync.Mutex{}
		c.locks[heldKey{ref, kind}] = l
	}
	c.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Acquire claims an item (a task claim) or a PR (a review claim): check
// that no claim holds, post the claim comment, wait the re-read delay,
// re-read, and only if this instance's claim is the earliest live one,
// add the claim label and, for a task claim, the assignee (spec
// work-claiming, "Claim acquisition resolves races deterministically").
// A lost race retracts the comment, touches nothing else, and fails with
// *ClaimTakenError. A claim this instance already holds, such as one left
// by a crash before the mark, is taken up again instead of posted twice.
func (c *Claiming) Acquire(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) (ports.HeldClaim, error) {
	defer c.lock(ref, kind)()
	t, err := c.route(ctx, ref)
	if err != nil {
		return ports.HeldClaim{}, err
	}
	_, v, err := c.read(ctx, t, ref)
	if err != nil {
		return ports.HeldClaim{}, err
	}
	var mine string
	if winner, found := domain.ResolveClaim(v, ref, kind); found {
		if winner.Instance != t.Instance {
			return ports.HeldClaim{}, &ClaimTakenError{Ref: ref, Holder: winner.Instance}
		}
		mine = winner.CommentID
	} else {
		claim := domain.Claim{Kind: kind, Instance: t.Instance, Ref: ref.String(), Lease: c.opts.Lease}
		posted, err := t.Provider.Post(ctx, ref, claim.Comment(v.Now.Add(c.opts.Lease)))
		if err != nil {
			return ports.HeldClaim{}, fmt.Errorf("post the claim on %s: %w", ref, err)
		}
		mine = posted.ID
		select {
		case <-ctx.Done():
			return ports.HeldClaim{}, errors.Join(ctx.Err(), c.retract(context.WithoutCancel(ctx), t, ref, mine))
		case <-c.clock.After(c.opts.RereadDelay):
		}
	}

	page, v, err := c.read(ctx, t, ref)
	if err != nil {
		return ports.HeldClaim{}, errors.Join(err, c.retract(ctx, t, ref, mine))
	}
	winner, found := domain.ResolveClaim(v, ref, kind)
	if !found || winner.CommentID != mine {
		taken := &ClaimTakenError{Ref: ref}
		if found {
			taken.Holder = winner.Instance
		}
		return ports.HeldClaim{}, errors.Join(taken, c.retract(ctx, t, ref, mine))
	}

	mark := ports.ClaimMark{Label: t.Label, Assign: kind == domain.ClaimTask && ref.Kind == domain.KindItem, Revision: page.Revision}
	assigned, err := t.Provider.Mark(ctx, ref, mark)
	if err != nil {
		undo := c.retract(ctx, t, ref, mine)
		if errors.Is(err, ports.ErrConflict) {
			// An Azure DevOps revision conflict is a lost race (spec
			// work-claiming, "Azure DevOps optimistic concurrency"); the
			// patch failed as a whole, so nothing else was written.
			return ports.HeldClaim{}, errors.Join(&ClaimTakenError{Ref: ref}, undo)
		}
		// The label may have been added before the failure.
		undo = errors.Join(undo, t.Provider.Unmark(ctx, ref, t.Label, false))
		return ports.HeldClaim{}, errors.Join(fmt.Errorf("mark the claim on %s: %w", ref, err), undo)
	}
	held := ports.HeldClaim{
		Ref: ref, Kind: kind, Instance: t.Instance, CommentID: mine, Label: t.Label,
		Assigned: assigned, Parked: winner.Parked, Lease: winner.Lease, UpdatedAt: winner.UpdatedAt,
	}
	if err := c.store.SaveClaim(ctx, held); err != nil {
		undo := errors.Join(c.retract(ctx, t, ref, mine), t.Provider.Unmark(ctx, ref, t.Label, assigned))
		return ports.HeldClaim{}, errors.Join(fmt.Errorf("store the claim on %s: %w", ref, err), undo)
	}
	return held, nil
}

func (c *Claiming) retract(ctx context.Context, t ClaimTarget, ref domain.Ref, commentID string) error {
	if err := t.Provider.Retract(ctx, ref, commentID); err != nil && !errors.Is(err, ports.ErrNotFound) {
		return fmt.Errorf("retract the claim comment %s on %s: %w", commentID, ref, err)
	}
	return nil
}

// read re-reads the claims of ref, asks who of their authors has write
// access, and returns the page and its view.
func (c *Claiming) read(ctx context.Context, t ClaimTarget, ref domain.Ref) (ports.ClaimPage, domain.ClaimView, error) {
	page, err := t.Provider.Claims(ctx, ref)
	if err != nil {
		return ports.ClaimPage{}, domain.ClaimView{}, fmt.Errorf("read the claims of %s: %w", ref, err)
	}
	asked := map[string]bool{}
	var writers []string
	for _, comment := range page.Comments {
		login := strings.ToLower(comment.Author)
		if login == "" || asked[login] {
			continue
		}
		asked[login] = true
		ok, err := t.Provider.CanWrite(ctx, ref.Repo, comment.Author)
		if err != nil {
			return ports.ClaimPage{}, domain.ClaimView{}, fmt.Errorf("check the write access of %s on %s: %w", comment.Author, ref.Repo, err)
		}
		if ok {
			writers = append(writers, comment.Author)
		}
	}
	v := domain.ClaimView{
		Comments:   page.Comments,
		Labels:     page.Labels,
		ClaimLabel: t.Label,
		Writers:    domain.NewUserSet(writers...),
		Now:        page.ServerTime,
	}
	return page, v, nil
}

// Held lists the claims this instance holds, with their lease expiry
// (spec queen-console, "Health and status").
func (c *Claiming) Held(ctx context.Context) ([]ports.HeldClaim, error) {
	return c.store.HeldClaims(ctx)
}

// Run renews the held claims every third of the lease until ctx is
// cancelled (spec work-claiming, "Leases and renewal"). The local clock
// only schedules the renewals; leases are judged by the provider's time.
func (c *Claiming) Run(ctx context.Context) {
	for {
		// A failed renewal is tried again at the next tick; the work goes
		// on meanwhile, and the next successful read decides whether the
		// claim still holds.
		_ = c.Renew(ctx)
		select {
		case <-ctx.Done():
			return
		case <-c.clock.After(c.opts.Lease / 3):
		}
	}
}

// Renew checks every held claim once: a claim still held is renewed by
// editing its comment, unless it is parked, and a lost or force-released
// one ends its work (see HandleChange). It goes through every claim and
// returns their errors joined.
func (c *Claiming) Renew(ctx context.Context) error {
	held, err := c.store.HeldClaims(ctx)
	if err != nil {
		return fmt.Errorf("read the held claims: %w", err)
	}
	var errs []error
	for _, h := range held {
		errs = append(errs, c.checkHeld(ctx, h.Ref, h.Kind, !h.Parked))
	}
	return errors.Join(errs...)
}

// HandleChange reacts to a change event on an item or PR this instance
// holds a claim on: a removed claim label or an /anthill release comment
// re-checks the claim on the provider at once, rather than at the next
// renewal (spec work-claiming, "Manual override"). The check, not the
// event, decides, so a stale or repeated event does no harm; other
// events, and changes Anthill made, are ignored.
func (c *Claiming) HandleChange(ctx context.Context, e domain.ChangeEvent) error {
	if e.Origin == domain.OriginAnthill {
		return nil
	}
	switch e.Kind {
	case domain.ChangeLabelRemoved:
	case domain.ChangeCommentAdded:
		if !domain.IsReleaseCommand(domain.Comment{Body: e.Body}) {
			return nil
		}
	default:
		return nil
	}
	held, err := c.store.HeldClaims(ctx)
	if err != nil {
		return fmt.Errorf("read the held claims: %w", err)
	}
	var errs []error
	for _, h := range held {
		if h.Ref != e.Ref || (e.Kind == domain.ChangeLabelRemoved && !strings.EqualFold(e.Label, h.Label)) {
			continue
		}
		errs = append(errs, c.checkHeld(ctx, h.Ref, h.Kind, false))
	}
	return errors.Join(errs...)
}

// Park keeps the task claim on an item without renewal while its ant is
// between runs, and Unpark renews it again when a run starts (spec
// work-claiming, "Long-lived task claims"). Both first check that the
// claim still holds, so an expired claim is never revived; a lost claim
// fails with *ClaimTakenError after its work was ended.
func (c *Claiming) Park(ctx context.Context, item domain.Ref) error {
	return c.setParked(ctx, item, true)
}

// Unpark renews a parked task claim. See Park.
func (c *Claiming) Unpark(ctx context.Context, item domain.Ref) error {
	return c.setParked(ctx, item, false)
}

func (c *Claiming) setParked(ctx context.Context, item domain.Ref, parked bool) error {
	defer c.lock(item, domain.ClaimTask)()
	s, err := c.check(ctx, item, domain.ClaimTask)
	if err != nil {
		return err
	}
	if !s.check.Held() {
		if err := c.lose(ctx, s); err != nil {
			return err
		}
		return &ClaimTakenError{Ref: item, Holder: s.check.Winner.Instance}
	}
	s.held.Parked = parked
	return c.renew(ctx, s)
}

// checkHeld checks one held claim and ends its work when it was lost.
// When renew is set, a claim still held is renewed.
func (c *Claiming) checkHeld(ctx context.Context, ref domain.Ref, kind domain.ClaimKind, renew bool) error {
	defer c.lock(ref, kind)()
	s, err := c.check(ctx, ref, kind)
	switch {
	case errors.Is(err, errNotHeld):
		return nil // released meanwhile
	case err != nil:
		return err
	case !s.check.Held():
		return c.lose(ctx, s)
	case renew && !s.held.Parked:
		return c.renew(ctx, s)
	}
	return nil
}

// errNotHeld is a claim this instance does not hold.
var errNotHeld = errors.New("claim not held")

// claimState is a held claim as just read on the provider.
type claimState struct {
	held   ports.HeldClaim
	target ClaimTarget
	check  domain.ClaimCheck
	// now is the provider's time of the read.
	now time.Time
}

// check reads a held claim on the provider. It must be called with the
// claim's lock held. A claim not held locally fails with errNotHeld.
func (c *Claiming) check(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) (claimState, error) {
	held, err := c.store.HeldClaims(ctx)
	if err != nil {
		return claimState{}, fmt.Errorf("read the held claims: %w", err)
	}
	var s claimState
	i := slices.IndexFunc(held, func(h ports.HeldClaim) bool { return h.Ref == ref && h.Kind == kind })
	if i < 0 {
		return claimState{}, fmt.Errorf("%s claim on %s: %w", kind, ref, errNotHeld)
	}
	s.held = held[i]
	if s.target, err = c.route(ctx, ref); err != nil {
		return claimState{}, err
	}
	_, v, err := c.read(ctx, s.target, ref)
	if err != nil {
		return claimState{}, err
	}
	v.ClaimLabel = s.held.Label
	s.check, s.now = domain.CheckClaim(v, ref, kind, s.held.Instance, s.held.CommentID), v.Now
	return s, nil
}

// renew edits the claim comment, which moves its provider timestamp and
// so renews the lease, and stores the new timestamp.
func (c *Claiming) renew(ctx context.Context, s claimState) error {
	h, claim := s.held, s.check.Own
	claim.Parked = h.Parked
	edited, err := s.target.Provider.Edit(ctx, h.Ref, h.CommentID, claim.Comment(s.now.Add(claim.Lease)))
	if err != nil {
		return fmt.Errorf("renew the claim on %s: %w", h.Ref, err)
	}
	h.UpdatedAt, h.Lease = edited.UpdatedAt, claim.Lease
	if err := c.store.SaveClaim(ctx, h); err != nil {
		return fmt.Errorf("store the claim on %s: %w", h.Ref, err)
	}
	return nil
}

// lose ends the work behind a lost or force-released claim (spec
// work-claiming, "Holder returns after expiry" and "Manual override"):
// the loss handler ends any active run, a task claim's ant becomes
// abandoned with reason "claim lost" (its worktree is kept), the claim
// comment is marked released and, unless another instance's claim holds
// by now, the claim label and Anthill's assignment are removed; then the
// queen is told.
// Until the handler and the ant succeed, the claim stays held, so the
// next renewal tries again.
func (c *Claiming) lose(ctx context.Context, s claimState) error {
	h, t, check := s.held, s.target, s.check
	if err := c.loss.ClaimLost(ctx, h.Ref, h.Kind, ReasonClaimLost); err != nil {
		return fmt.Errorf("end the work on %s after its claim was lost: %w", h.Ref, err)
	}
	if h.Kind == domain.ClaimTask && h.Ref.Kind == domain.KindItem {
		if err := c.abandon(ctx, h.Ref); err != nil {
			return err
		}
	}
	var cleanup error
	if check.Lost != domain.LossCommentGone {
		cleanup = c.markReleased(ctx, h, t, check.Own)
	}
	if !check.OtherHolds() {
		// No other claim holds, so the label and the assignment are
		// this instance's to remove.
		cleanup = errors.Join(cleanup, t.Provider.Unmark(ctx, h.Ref, h.Label, h.Assigned))
	}
	if err := c.store.DeleteClaim(ctx, h.Ref, h.Kind); err != nil {
		return errors.Join(cleanup, fmt.Errorf("forget the claim on %s: %w", h.Ref, err))
	}
	// A notification that cannot be shown is not a reason to fail.
	_ = c.notifier.Notify(ctx, ports.Notification{
		Kind:  ports.NotifyClaimLost,
		Title: "Claim lost on " + h.Ref.String(),
		Body:  check.Reason(),
		Ref:   &h.Ref,
	})
	if cleanup != nil {
		return fmt.Errorf("clean up the lost claim on %s: %w", h.Ref, cleanup)
	}
	return nil
}

// abandon moves the item's current ant to abandoned with reason "claim
// lost", unless it is already final or there is none.
func (c *Claiming) abandon(ctx context.Context, item domain.Ref) error {
	r, err := c.store.CurrentAnt(ctx, item)
	if errors.Is(err, ports.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the ant of %s: %w", item, err)
	}
	ant, err := domain.RestoreAnt(r.Item, r.Base, r.Budget, r.History)
	if err != nil {
		return fmt.Errorf("restore the ant of %s: %w", item, err)
	}
	if ant.State().Final() {
		return nil
	}
	if err := ant.Transition(domain.AntAbandoned, c.clock.Now(), ReasonClaimLost); err != nil {
		return err
	}
	r.History = ant.History()
	if err := c.store.SaveAnt(ctx, r); err != nil {
		return fmt.Errorf("store the ant of %s: %w", item, err)
	}
	return nil
}

// markReleased edits the claim comment to say it is released. A comment
// that is gone needs no edit.
func (c *Claiming) markReleased(ctx context.Context, h ports.HeldClaim, t ClaimTarget, own domain.Claim) error {
	if own.CommentID == "" {
		own = domain.Claim{Kind: h.Kind, Instance: h.Instance, Ref: h.Ref.String(), Lease: h.Lease}
	}
	own.Released, own.Parked = true, false
	if _, err := t.Provider.Edit(ctx, h.Ref, h.CommentID, own.Comment(time.Time{})); err != nil && !errors.Is(err, ports.ErrNotFound) {
		return fmt.Errorf("mark the claim on %s released: %w", h.Ref, err)
	}
	return nil
}

// Release gives up a claim because its work ended: the ticket is closed,
// the PR is merged or closed, the queen abandons the ant, the ant
// finishes with no-change, or a review activity finishes (spec
// work-claiming, "Release"). It marks the claim comment as released,
// removes the claim label, and unassigns the developer only if Anthill
// made the assignment. When another instance's claim holds by now, only
// the comment is marked, and the label and assignee are left to it.
// Releasing a claim not held does nothing.
func (c *Claiming) Release(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) error {
	defer c.lock(ref, kind)()
	s, err := c.check(ctx, ref, kind)
	switch {
	case errors.Is(err, errNotHeld):
		return nil
	case err != nil:
		return err
	}
	h, t, check := s.held, s.target, s.check
	if check.Lost != domain.LossCommentGone {
		if err := c.markReleased(ctx, h, t, check.Own); err != nil {
			return err
		}
	}
	if !check.OtherHolds() {
		if err := t.Provider.Unmark(ctx, ref, h.Label, h.Assigned); err != nil {
			return fmt.Errorf("release the claim on %s: %w", ref, err)
		}
	}
	if err := c.store.DeleteClaim(ctx, ref, kind); err != nil {
		return fmt.Errorf("forget the claim on %s: %w", ref, err)
	}
	return nil
}
