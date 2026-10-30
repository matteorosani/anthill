package ports

import (
	"context"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

// StateStore is the local, machine-private store (design D2): the session
// index, runs and usage, poll state and hashes, the outbox, stored
// changes, questions, extension requests, hidden proposals and held
// claims. It is made of smaller interfaces so that each service depends
// only on the part it uses. Lookups of a missing record return ErrNotFound.
type StateStore interface {
	SessionIndex
	RunLog
	PollState
	Outbox
	StoredChanges
	Questions
	Extensions
	Proposals
	HeldClaims
}

// AntID identifies one ant. An item can have several ants over time, such
// as an abandoned one and a later one; the latest is its current ant.
type AntID int64

// AntRecord is an ant's entry in the session index (spec ant-execution,
// "Session index").
type AntRecord struct {
	ID   AntID
	Item domain.Ref
	// Base, Budget and History are what domain.RestoreAnt needs. History
	// only grows: saving a record appends its new transitions.
	Base    time.Duration
	Budget  domain.Budget
	History []domain.Transition
	Runner  string
	// SessionID is empty until the first run reports it, and after the
	// index is rebuilt (design D17).
	SessionID string
	Worktree  string
	Branch    string
	// PR is the ant PR, or nil before it is opened.
	PR *domain.Ref
}

// State is the ant's state: the target of its last transition.
func (r AntRecord) State() domain.AntState {
	if len(r.History) == 0 {
		return ""
	}
	return r.History[len(r.History)-1].To
}

// SessionIndex keeps one record per ant (spec ant-execution, "Session
// index").
type SessionIndex interface {
	// CreateAnt stores a new ant, which becomes its item's current ant,
	// and returns its ID. r.ID is ignored.
	CreateAnt(ctx context.Context, r AntRecord) (AntID, error)
	// SaveAnt updates the ant r.ID.
	SaveAnt(ctx context.Context, r AntRecord) error
	Ant(ctx context.Context, id AntID) (AntRecord, error)
	// CurrentAnt is the latest ant of an item. A change on a PR finds it
	// through the PR's link to its item.
	CurrentAnt(ctx context.Context, item domain.Ref) (AntRecord, error)
	// AntByBranch is the latest ant on an ant branch, for a PR whose
	// closing reference was removed ("PR body reference removed").
	AntByBranch(ctx context.Context, branch string) (AntRecord, error)
	// Ants lists the current ant of every item whose state is one of
	// states, or of every item when states is empty, oldest first (spec
	// queen-console, "Monitoring ants"; spec ant-execution, "Restart
	// resilience").
	Ants(ctx context.Context, states ...domain.AntState) ([]AntRecord, error)
}

// RunKind is the kind of a run (glossary).
type RunKind string

// The run kinds.
const (
	RunTask     RunKind = "task"
	RunFollowUp RunKind = "follow-up"
)

// RunOutcome is how a run ended (glossary): a finish status, failed,
// stopped or timed out.
type RunOutcome string

// The run outcomes.
const (
	OutcomeComplete RunOutcome = "complete"
	OutcomePartial  RunOutcome = "partial"
	OutcomeNoChange RunOutcome = "no-change"
	OutcomeFailed   RunOutcome = "failed"
	OutcomeStopped  RunOutcome = "stopped"
	OutcomeTimedOut RunOutcome = "timed-out"
)

// RunID identifies one run.
type RunID int64

// RunRecord is one run in an ant's history, with its usage (spec
// ant-execution, "Usage is recorded").
type RunRecord struct {
	ID        RunID
	Ant       AntID
	Kind      RunKind
	SessionID string
	StartedAt time.Time
	// EndedAt is zero while the run is active.
	EndedAt time.Time
	// Outcome is empty while the run is active.
	Outcome RunOutcome
	Reason  string
	Usage   Usage
}

// Duration is how long the run lasted, or 0 while it is active.
func (r RunRecord) Duration() time.Duration {
	if r.EndedAt.IsZero() {
		return 0
	}
	return r.EndedAt.Sub(r.StartedAt)
}

// RoundID identifies one shepherd round.
type RoundID int64

// RoundRecord is one shepherd round run by this instance, with its usage
// (spec pr-shepherd, "Round triggers", "Developer-requested rounds").
type RoundRecord struct {
	ID   RoundID
	PR   domain.Ref
	Head string
	// Requested is set for rounds started by /anthill review, which are
	// counted apart from automatic rounds.
	Requested bool
	StartedAt time.Time
	EndedAt   time.Time
	Usage     Usage
}

// RunLog records runs and shepherd rounds.
type RunLog interface {
	// StartRun stores an active run and returns its ID. r.ID is ignored.
	StartRun(ctx context.Context, r RunRecord) (RunID, error)
	// EndRun stores the end of run r.ID: its end time, outcome, reason,
	// session and usage.
	EndRun(ctx context.Context, r RunRecord) error
	// Runs lists an ant's runs, oldest first.
	Runs(ctx context.Context, ant AntID) ([]RunRecord, error)
	StartRound(ctx context.Context, r RoundRecord) (RoundID, error)
	EndRound(ctx context.Context, r RoundRecord) error
	// Rounds lists a PR's rounds, oldest first.
	Rounds(ctx context.Context, pr domain.Ref) ([]RoundRecord, error)
	// Usage lists the runs and rounds started in [from, to), for the
	// usage report (spec queen-console, "Usage reporting").
	Usage(ctx context.Context, from, to time.Time) ([]RunRecord, []RoundRecord, error)
}

// Hash is the hash of one field group of an item or PR (design D7).
type Hash struct {
	Ref   domain.Ref
	Group string
	Value string
	// Text is the group's text, kept for the groups whose old value a
	// change event carries, such as a title or body, and "" for the
	// others. It stays on this machine and is never logged.
	Text string
}

// PollResult is what one successful poll of a provider connection found.
type PollResult struct {
	// Connection names the provider connection.
	Connection string
	Cursor     string
	At         time.Time
	// Hashes are the new hashes; groups not listed keep their old hash.
	Hashes []Hash
	// Events are the changes found, which go to the outbox.
	Events []domain.ChangeEvent
}

// PollStatus is the state of one provider connection (spec queen-console,
// "Health and status").
type PollStatus struct {
	Connection  string
	Cursor      string
	LastSuccess time.Time
	// LastError is the last failure's message, empty after a success.
	LastError   string
	LastErrorAt time.Time
	// RateLimitedUntil is zero unless polling is paused for the rate
	// limit (spec work-item-sources, "Polling cadence").
	RateLimitedUntil time.Time
}

// PollState keeps poll cursors and hashes, and writes change events to
// the outbox.
type PollState interface {
	// RecordPoll stores the cursor, the hashes and the change events of
	// one poll in a single transaction, so a crash keeps all or none of
	// them (design D7).
	RecordPoll(ctx context.Context, p PollResult) error
	// RecordPollFailure stores a failed poll; the cursor is kept.
	RecordPollFailure(ctx context.Context, connection string, at time.Time, message string, rateLimitedUntil time.Time) error
	// PollStatus is a connection's state, with only Connection set when
	// it was never polled.
	PollStatus(ctx context.Context, connection string) (PollStatus, error)
	// PollStatuses lists every connection's state.
	PollStatuses(ctx context.Context) ([]PollStatus, error)
	// Hashes are the stored hashes of an item or PR by group.
	Hashes(ctx context.Context, ref domain.Ref) (map[string]string, error)
	// Texts are the stored texts of an item or PR by group, for the
	// groups whose text is not empty.
	Texts(ctx context.Context, ref domain.Ref) (map[string]string, error)
}

// OutboxID identifies one change event in the outbox.
type OutboxID int64

// OutboxEvent is a change event waiting to be handled.
type OutboxEvent struct {
	ID         OutboxID
	Event      domain.ChangeEvent
	DetectedAt time.Time
}

// Outbox holds the change events still to handle (design D7). Delivery is
// at least once: an event stays pending until Done, so after a crash it
// is handed out again. Handling is exactly once because handlers are
// idempotent, keyed by the outbox ID.
type Outbox interface {
	// Pending lists the events not yet done, oldest first.
	Pending(ctx context.Context) ([]OutboxEvent, error)
	// Done marks an event handled.
	Done(ctx context.Context, id OutboxID, at time.Time) error
}

// StoredChange is an actionable change kept for an ant's next run (spec
// change-reaction, "Changes while the ant is between runs").
type StoredChange struct {
	ID  int64
	Ant AntID
	// Source is the outbox event the change came from. Storing a change
	// with a Source already stored does nothing, which keeps the outbox
	// handler idempotent.
	Source   OutboxID
	Event    domain.ChangeEvent
	StoredAt time.Time
}

// StoredChanges keeps changes for ants between runs.
type StoredChanges interface {
	StoreChange(ctx context.Context, c StoredChange) error
	// StoredChanges lists an ant's changes not yet given to a run, oldest
	// first.
	StoredChanges(ctx context.Context, ant AntID) ([]StoredChange, error)
	// ConsumeChanges marks the listed changes as given to a run.
	ConsumeChanges(ctx context.Context, ids []int64, at time.Time) error
}

// QuestionRecord is a question an ant asked (spec agent-conversation).
type QuestionRecord struct {
	// CommentID is the question comment's ID on the provider.
	CommentID string
	Ant       AntID
	// On is the ticket or PR the question was posted on.
	On      domain.Ref
	AskedAt time.Time
	// AnsweredAt and AnswerID are set once a human answered.
	AnsweredAt time.Time
	AnswerID   string
	// StaleAt is set when the question was reported as stale.
	StaleAt time.Time
}

// Questions keeps questions until they are answered.
type Questions interface {
	// SaveQuestion stores or updates a question by its comment ID.
	SaveQuestion(ctx context.Context, q QuestionRecord) error
	// PendingQuestions lists the questions without an answer, oldest
	// first.
	PendingQuestions(ctx context.Context) ([]QuestionRecord, error)
}

// ExtensionID identifies one extension request.
type ExtensionID int64

// ExtensionRecord is one extension request of an ant (spec ant-timebox).
type ExtensionRecord struct {
	ID      ExtensionID
	Ant     AntID
	Request domain.ExtensionRequest
	// DecidedAt is set once the request is no longer pending.
	DecidedAt time.Time
}

// Extensions keeps extension requests for the console (spec
// queen-console, "Controlling ants").
type Extensions interface {
	// SaveExtension stores a new request when r.ID is 0, or updates r.ID,
	// and returns the ID.
	SaveExtension(ctx context.Context, r ExtensionRecord) (ExtensionID, error)
	// PendingExtensions lists the requests waiting for the queen.
	PendingExtensions(ctx context.Context) ([]ExtensionRecord, error)
	// Extensions lists an ant's requests, oldest first.
	Extensions(ctx context.Context, ant AntID) ([]ExtensionRecord, error)
}

// HiddenProposal is a proposal the queen declined or snoozed on this
// machine (spec queen-console, "Accepting and declining").
type HiddenProposal struct {
	Item     domain.Ref
	HiddenAt time.Time
	// Until is the end of a snooze, and zero for a decline.
	Until time.Time
	// ItemHash is the item's content hash when it was hidden; a declined
	// item is proposed again once it changes (spec task-eligibility,
	// "Proposal list").
	ItemHash string
}

// Proposals keeps hidden proposals.
type Proposals interface {
	// Hide stores or replaces the hidden entry of an item.
	Hide(ctx context.Context, h HiddenProposal) error
	// Unhide removes it. Unhiding an item that is not hidden does nothing.
	Unhide(ctx context.Context, item domain.Ref) error
	Hidden(ctx context.Context) ([]HiddenProposal, error)
}
