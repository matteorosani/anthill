package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// ConsoleAnts is the part of the supervisor the queen console uses: the
// acceptance and the queen actions on ants (spec queen-console,
// "Accepting and declining" and "Controlling ants"). AntSupervisor
// implements it.
type ConsoleAnts interface {
	Accept(ctx context.Context, req AcceptRequest) (ports.AntID, error)
	Stop(ctx context.Context, id ports.AntID, reason string) error
	Abandon(ctx context.Context, id ports.AntID, reason string) error
	Retry(ctx context.Context, id ports.AntID) error
	Continue(ctx context.Context, id ports.AntID, budget time.Duration) error
	DecideExtension(ctx context.Context, id ports.AntID, minutes int) (domain.ExtensionRequest, error)
	AcceptFollowUp(ctx context.Context, id ports.AntID) error
	DeclineFollowUp(ctx context.Context, id ports.AntID) error
}

// ConsoleAnswers posts the queen's answers given in the console. The
// conversation service implements it.
type ConsoleAnswers interface {
	AnswerFromConsole(ctx context.Context, ant ports.AntID, text string) (domain.Comment, error)
}

var (
	_ ConsoleAnts    = (*AntSupervisor)(nil)
	_ ConsoleAnswers = (*Conversation)(nil)
)

// ConsoleStore is the part of the local store the console reads.
type ConsoleStore interface {
	ports.SessionIndex
	ports.RunLog
	ports.Questions
	ports.Extensions
	ports.Proposals
	ports.StoredChanges
}

// ConsoleDeps are the services and ports behind the console.
type ConsoleDeps struct {
	Store       ConsoleStore
	Ants        ConsoleAnts
	Eligibility *Eligibility
	// Candidates lists the items the proposal list evaluates, and Tracker
	// reads one item, for a decline.
	Candidates CandidateSource
	Tracker    TrackerRoute
	Answers    ConsoleAnswers
	Activity   *ActivityHub
	// Workspace and Route clean up an ant's worktree and branch.
	Workspace *Workspace
	Route     WorkspaceRoute
	// Repositories lists the repositories of this instance's enabled
	// entries, against which the queen's item references are resolved.
	Repositories func() []domain.Repository
	Clock        ports.Clock
}

// Console carries out the queen console's operations on the daemon's
// services (spec queen-console). The control interface calls it for the
// CLI and any later GUI; it holds no state of its own.
type Console struct {
	deps ConsoleDeps
}

// NewConsole returns the console over its dependencies.
func NewConsole(deps ConsoleDeps) *Console {
	return &Console{deps: deps}
}

// NoAntError is a console action on an item that has no ant on this
// machine.
type NoAntError struct {
	Item domain.Ref
}

func (e *NoAntError) Error() string {
	return "no ant for " + ItemName(e.Item)
}

// ItemName is how the console names an item: #12 on GitHub and AB#12 on
// Azure DevOps.
func ItemName(ref domain.Ref) string {
	return ref.Display(ref.Repo)
}

// Resolve reads an item reference the queen typed, such as #12, AB#12 or
// matteorosani/anthill#12 (domain.ResolveItemRef).
func (c *Console) Resolve(text string) (domain.Ref, error) {
	var repos []domain.Repository
	if c.deps.Repositories != nil {
		repos = c.deps.Repositories()
	}
	return domain.ResolveItemRef(text, repos)
}

// Now is the daemon's time, from which the console computes ages and
// remaining times.
func (c *Console) Now() time.Time {
	return c.deps.Clock.Now()
}

// currentAnt is the item's current ant on this machine.
func (c *Console) currentAnt(ctx context.Context, item domain.Ref) (ports.AntRecord, error) {
	rec, err := c.deps.Store.CurrentAnt(ctx, item)
	if errors.Is(err, ports.ErrNotFound) {
		return rec, &NoAntError{Item: item}
	}
	if err != nil {
		return rec, fmt.Errorf("reading the ant of %s: %w", ItemName(item), err)
	}
	return rec, nil
}

// ItemActionError is a queen action the ant's state does not allow,
// named by the item rather than the ant's ID.
type ItemActionError struct {
	Item    domain.Ref
	Refused *ActionRefusedError
}

func (e *ItemActionError) Error() string {
	why := "it is " + string(e.Refused.State)
	if e.Refused.Reason != "" {
		why = e.Refused.Reason
	}
	return fmt.Sprintf("cannot %s the ant for %s: %s", e.Refused.Action, ItemName(e.Item), why)
}

func (e *ItemActionError) Unwrap() error {
	return e.Refused
}

// onItem names the item in a refused queen action.
func onItem(item domain.Ref, err error) error {
	var refused *ActionRefusedError
	if errors.As(err, &refused) {
		return &ItemActionError{Item: item, Refused: refused}
	}
	return err
}

// AntView is an ant as the console lists it (spec queen-console,
// "Monitoring ants").
type AntView struct {
	ID     ports.AntID
	Item   domain.Ref
	State  domain.AntState
	Runner string
	Branch string
	PR     *domain.Ref
	// Used and Remaining are the budget's time used and left at the
	// console's now, and Total its length with extensions.
	Used, Remaining, Total time.Duration
	// NearLimit is set once the ant was warned at its warning threshold
	// (spec ant-timebox, "Warning before time-out").
	NearLimit bool
	// Since is the time of the ant's last transition, and Reason its
	// reason.
	Since  time.Time
	Reason string
	// FollowUp is where its follow-up proposal stands.
	FollowUp domain.ProposalState
}

// view makes the console's view of an ant record at now.
func view(rec ports.AntRecord, now time.Time) AntView {
	b := rec.Budget
	v := AntView{
		ID: rec.ID, Item: rec.Item, State: rec.State(), Runner: rec.Runner, Branch: rec.Branch, PR: rec.PR,
		Used: b.UsedAt(now), Remaining: b.Remaining(now), Total: b.Total(), NearLimit: b.Warned,
		FollowUp: rec.FollowUpProposal,
	}
	if n := len(rec.History); n > 0 {
		v.Since, v.Reason = rec.History[n-1].At, rec.History[n-1].Reason
	}
	return v
}
