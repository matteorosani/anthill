package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// FindingsHandler takes a shepherd finding on an ant PR of this
// instance (spec change-reaction, "Shepherd findings on ant PRs"), the
// only Anthill-authored change that is not ignored.
type FindingsHandler interface {
	HandleFindings(ctx context.Context, in ChangeInput) error
}

// RelevanceClassifier classifies a human comment or review the rules
// could not decide, with the item for context (design D8).
type RelevanceClassifier interface {
	Classify(ctx context.Context, ant ports.AntRecord, item domain.WorkItem, e domain.ChangeEvent) (domain.Relevance, error)
}

// ChangeRouter is what the change reactor asks of ant supervision; the
// AntSupervisor implements it.
type ChangeRouter interface {
	RouteChange(ctx context.Context, id ports.AntID, c domain.AntChange) error
	Trigger(ctx context.Context, id ports.AntID) error
	Withdraw(ctx context.Context, id ports.AntID, reason string) error
}

var (
	_ ChangeRouter        = (*AntSupervisor)(nil)
	_ RelevanceClassifier = (*Classifier)(nil)
	_ Holds               = (*ItemHolds)(nil)
)

// HeldClaims lists the claims this instance holds; Claiming implements
// it.
type HeldClaims interface {
	Held(ctx context.Context) ([]ports.HeldClaim, error)
}

// ChangeReactorStore is the part of the local store the change reactor
// uses.
type ChangeReactorStore interface {
	ports.SessionIndex
	ports.StoredChanges
}

// ChangeReactorDeps are the ports and services the change reactor uses.
type ChangeReactorDeps struct {
	Store  ChangeReactorStore
	Claims HeldClaims
	Ants   ChangeRouter
	// Tracker and CodeHost read the item and check write access; Route
	// gives the item's settings, such as its labels.
	Tracker  TrackerRoute
	CodeHost CodeHostRoute
	Route    WorkspaceRoute
	// Classifier classifies the comments the rules cannot. Without it,
	// they are actionable, as when the classifier fails (design D8).
	Classifier RelevanceClassifier
	// Conversation handles commands and answers (agent-conversation),
	// and Findings the shepherd's findings on ant PRs (16.4). Both are
	// optional: without one, the changes it would take are dropped, and
	// without Conversation a comment is classified like any other.
	Conversation ConversationIntake
	Findings     FindingsHandler
}

// ChangeReactor is the one intake of change events for ants (spec
// change-reaction). For each event of the outbox, in this order:
//  1. it finds the ticket and this instance's ant for the event's item
//     or ant PR, and whether this instance holds the task claim;
//  2. Anthill-authored changes are ignored, except a shepherd finding on
//     an ant PR of this instance, which goes to Findings;
//  3. a human comment or review goes to the conversation's HandleCommands,
//     which ends the intake for one holding /anthill lines, unless it
//     asks for the change to be routed, as /anthill fix on an ant PR;
//  4. an instance that is not responsible does nothing more;
//  5. the hold label removed triggers the ant, so that its stored
//     changes are handled as if they had just arrived; the opt-in label
//     removed or the item closed withdraws the ant; other label and
//     state changes need nothing here;
//  6. an answer to a pending question is taken by the conversation's
//     HandleAnswer;
//  7. the rest is classified: rules first (domain.Classify), then the
//     classifier, with actionable when it fails;
//  8. an actionable change is routed by the ant's playbook.
//
// Routing only delivers or stores a change. The run or proposal due for
// stored changes comes when Flush triggers the ant, after the events of
// one poll are handled, so those changes go to one follow-up run.
type ChangeReactor struct {
	deps ChangeReactorDeps

	mu      sync.Mutex
	touched []ports.AntID
}

// NewChangeReactor returns the change reactor.
func NewChangeReactor(deps ChangeReactorDeps) *ChangeReactor {
	return &ChangeReactor{deps: deps}
}

// Handle handles one change event; it is an OutboxHandler. It is
// idempotent, keyed by the event's outbox ID: a repeated event is stored
// once. Handle the events of a drain, then call Flush.
func (r *ChangeReactor) Handle(ctx context.Context, e ports.OutboxEvent) error {
	in, err := r.resolve(ctx, e)
	if err != nil {
		return err
	}
	ev := in.Event
	if ev.Origin == domain.OriginAnthill {
		if r.deps.Findings != nil && in.Responsible && shepherdFinding(ev) {
			return r.deps.Findings.HandleFindings(ctx, in)
		}
		return nil
	}
	route := false
	if r.deps.Conversation != nil && hasText(ev) {
		res, err := r.deps.Conversation.HandleCommands(ctx, in)
		if err != nil || (res.Handled && !res.Route) {
			return err
		}
		route = res.Route
	}
	if !in.Responsible {
		return nil
	}
	if !route {
		switch ev.Kind {
		case domain.ChangeLabelAdded, domain.ChangeLabelRemoved, domain.ChangeStateChanged:
			return r.labelOrState(ctx, in)
		}
		if r.deps.Conversation != nil {
			if answered, err := r.deps.Conversation.HandleAnswer(ctx, in); err != nil || answered {
				return err
			}
		}
		rel, err := r.classify(ctx, in)
		if err != nil || rel != domain.Actionable {
			return err
		}
	}
	if err := r.deps.Ants.RouteChange(ctx, in.Ant.ID, domain.AntChange{ID: int64(e.ID), Event: ev}); err != nil {
		return fmt.Errorf("routing a change of %s: %w", in.Item, err)
	}
	r.touch(in.Ant.ID)
	return nil
}

// Flush triggers each ant that got a change since the last Flush, so
// that a run or a proposal due for its stored changes comes now. Call it
// after the events of a drain are handled.
func (r *ChangeReactor) Flush(ctx context.Context) error {
	r.mu.Lock()
	touched := r.touched
	r.touched = nil
	r.mu.Unlock()
	var errs []error
	for _, id := range touched {
		if err := r.deps.Ants.Trigger(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("triggering ant %d after its changes: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// Reconcile triggers, once at daemon start, the succeeded ants with
// stored changes, which a restart between storing a change and Flush
// left without their follow-up run or proposal (spec change-reaction,
// "Changes are not lost across restarts").
func (r *ChangeReactor) Reconcile(ctx context.Context) error {
	ants, err := r.deps.Store.Ants(ctx, domain.AntSucceeded)
	if err != nil {
		return fmt.Errorf("listing the succeeded ants: %w", err)
	}
	var errs []error
	for _, rec := range ants {
		stored, err := r.deps.Store.StoredChanges(ctx, rec.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("reading the stored changes of ant %d: %w", rec.ID, err))
			continue
		}
		if len(stored) > 0 {
			r.touch(rec.ID)
		}
	}
	return errors.Join(append(errs, r.Flush(ctx))...)
}

func (r *ChangeReactor) touch(id ports.AntID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(r.touched, id) {
		r.touched = append(r.touched, id)
	}
}

// resolve finds the event's ticket, this instance's ant for it and
// whether this instance is responsible for it.
func (r *ChangeReactor) resolve(ctx context.Context, e ports.OutboxEvent) (ChangeInput, error) {
	in := ChangeInput{Outbox: e.ID, Event: e.Event}
	ant, err := r.antOf(ctx, e.Event.Ref)
	if err != nil || ant == nil {
		if e.Event.Ref.Kind == domain.KindItem {
			in.Item = e.Event.Ref
		}
		return in, err
	}
	in.Item = ant.Item
	if ant.State().Final() {
		return in, nil
	}
	in.Ant = ant
	held, err := r.deps.Claims.Held(ctx)
	if err != nil {
		return in, fmt.Errorf("reading the held claims: %w", err)
	}
	in.Responsible = queuedAtAcceptance(*ant) || slices.ContainsFunc(held, func(h ports.HeldClaim) bool {
		return h.Ref == ant.Item && h.Kind == domain.ClaimTask
	})
	return in, nil
}

// queuedAtAcceptance reports whether the ant was queued at acceptance
// and has not started yet: it claims its item when its first run starts,
// so until then it is this instance's without a claim.
func queuedAtAcceptance(rec ports.AntRecord) bool {
	return len(rec.History) == 1 && rec.State() == domain.AntQueued
}

// antOf is this instance's current ant for an item, or for the item of
// an ant PR, or nil when it has none.
func (r *ChangeReactor) antOf(ctx context.Context, ref domain.Ref) (*ports.AntRecord, error) {
	if ref.Kind == domain.KindItem {
		rec, err := r.deps.Store.CurrentAnt(ctx, ref)
		if errors.Is(err, ports.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading the ant of %s: %w", ref, err)
		}
		return &rec, nil
	}
	ants, err := r.deps.Store.Ants(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the ants: %w", err)
	}
	for i := range ants {
		if ants[i].PR != nil && *ants[i].PR == ref {
			return &ants[i], nil
		}
	}
	return nil, nil
}

// shepherdFinding reports whether an Anthill-authored change is a
// shepherd finding on a PR. The findings handler (16.4) triggers the
// shepherd, which alone hands findings to the ant: they are never routed
// as ordinary changes. The round's summary comment, which holds the
// findings without a location, passes too: its marker carries the
// round's fields.
func shepherdFinding(e domain.ChangeEvent) bool {
	if e.Ref.Kind != domain.KindPR || !hasText(e) {
		return false
	}
	m, ok := domain.FindMarker(e.Body)
	if !ok {
		return false
	}
	// A round's summary holds the findings without a location; it is
	// told apart from an ant's run summary by its round fields.
	return m.Kind == domain.CommentFinding || (m.Kind == domain.CommentSummary && m.RoundFields != nil)
}

// hasText reports whether the change is a comment or a review, which
// carry text.
func hasText(e domain.ChangeEvent) bool {
	switch e.Kind {
	case domain.ChangeCommentAdded, domain.ChangeCommentEdited, domain.ChangeReviewSubmitted:
		return true
	}
	return false
}

// labelOrState handles a label or state change on the item of an ant of
// this instance (spec change-reaction, "Hold freezes the ant",
// "Withdrawal of opt-in"). Adding the hold label needs nothing: the
// supervisor checks the label before it starts a run.
func (r *ChangeReactor) labelOrState(ctx context.Context, in ChangeInput) error {
	ws, err := r.deps.Route(ctx, in.Item)
	if err != nil {
		return fmt.Errorf("finding the settings of %s: %w", in.Item, err)
	}
	if reason, ok := domain.Withdrawal(in.Event, ws.Settings.OptInLabel); ok {
		return r.deps.Ants.Withdraw(ctx, in.Ant.ID, reason)
	}
	if _, removed := domain.HoldChange(in.Event, ws.Settings.HoldLabel); removed {
		r.touch(in.Ant.ID)
	}
	return nil
}

// classify applies the classification rules, then the classifier to a
// comment they cannot decide. A classifier that fails, or none, makes the
// change actionable (design D8).
func (r *ChangeReactor) classify(ctx context.Context, in ChangeInput) (domain.Relevance, error) {
	ev := in.Event
	tracker, err := r.deps.Tracker(ctx, in.Item)
	if err != nil {
		return "", fmt.Errorf("reaching the tracker of %s: %w", in.Item, err)
	}
	item, _, err := tracker.Item(ctx, in.Item)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", in.Item, err)
	}
	writers, err := r.writers(ctx, tracker, ev)
	if err != nil {
		return "", err
	}
	rel := domain.Classify(ev, writers, item.Author)
	if rel != domain.NeedsClassifier {
		return rel, nil
	}
	if r.deps.Classifier == nil {
		return domain.Actionable, nil
	}
	rel, err = r.deps.Classifier.Classify(ctx, *in.Ant, item, ev)
	if err != nil || (rel != domain.Actionable && rel != domain.Informational) {
		return domain.Actionable, nil
	}
	return rel, nil
}

// writers is the set of the change's author when they have write access
// to the repository the change is in, or the empty set.
func (r *ChangeReactor) writers(ctx context.Context, tracker ports.TrackerPort, ev domain.ChangeEvent) (domain.UserSet, error) {
	if ev.Actor == "" {
		return domain.NewUserSet(), nil
	}
	var (
		ok  bool
		err error
	)
	if ev.Ref.Kind == domain.KindPR {
		var host ports.CodeHostPort
		if host, err = r.deps.CodeHost(ctx, ev.Ref.Repo); err != nil {
			return domain.UserSet{}, fmt.Errorf("reaching the code host of %s: %w", ev.Ref.Repo, err)
		}
		ok, err = host.CanWrite(ctx, ev.Ref.Repo, ev.Actor)
	} else {
		ok, err = tracker.CanWrite(ctx, ev.Ref.Repo, ev.Actor)
	}
	if err != nil {
		return domain.UserSet{}, fmt.Errorf("checking the write access of %s on %s: %w", ev.Actor, ev.Ref.Repo, err)
	}
	if ok {
		return domain.NewUserSet(ev.Actor), nil
	}
	return domain.NewUserSet(), nil
}
