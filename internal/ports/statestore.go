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
	RunStarts
	RoundRequests
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
	// BaseBranch is the branch the ant branch was made from: its PR's
	// target and where new commits are counted from (spec ant-tools,
	// "Finish tool").
	BaseBranch string
	// PR is the ant PR, or nil before it is opened.
	PR *domain.Ref
	// Finish is the finish of the active run that Anthill has not carried
	// out yet: the finish tool stores it with the move to finishing, and
	// the decision that ends the finish clears it with the ant's next
	// state (design D22). It is nil otherwise.
	Finish *domain.AntFinish
	// FollowUpProposal is where the follow-up proposal for the ant's
	// stored changes stands (spec change-reaction, "Follow-ups start
	// automatically by default"). Saving it with the ant lets the start
	// of a run close it in the same transaction (design D22).
	FollowUpProposal domain.ProposalState
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
	// Usage is the run's own usage: when the runner reports cost as a
	// running total for its session, CostUSD is already the run's share
	// of it and CostIsSessionTotal is false (spec ant-execution, "Cost
	// reported for a whole session").
	Usage Usage
	// SessionCostUSD is the running total of its session that the runner
	// reported last during the run, when it reports cost that way, and
	// nil otherwise or when it reported none. The next run that resumes
	// the session takes its cost as the difference from it.
	SessionCostUSD *float64
	// NewBudget is the follow-up budget a follow-up run started for
	// changes got, and 0 for a run that kept the ant's budget. Only runs
	// with one count against the follow-up limit (spec change-reaction,
	// "Follow-up limits").
	NewBudget time.Duration
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
	// session, usage and session cost. An empty r.SessionID keeps the
	// stored one.
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

	// Text is the question as the ant wrote it, after redaction, for
	// answers that quote it, and Thread is the thread the question
	// comment opened on a threaded provider, or "". Answer is the change
	// that answered it, which the run resuming the session gets as input
	// (spec agent-conversation, "Answers resume the session").
	Text   string
	Thread string
	Answer *domain.ChangeEvent
}

// Questions keeps questions until they are answered.
type Questions interface {
	// SaveQuestion stores or updates a question by its comment ID.
	SaveQuestion(ctx context.Context, q QuestionRecord) error
	// PendingQuestions lists the questions without an answer, oldest
	// first.
	PendingQuestions(ctx context.Context) ([]QuestionRecord, error)

	// AntQuestions lists an ant's questions, answered or not, oldest first,
	// and QuestionsOn the questions posted on a ticket or PR.
	AntQuestions(ctx context.Context, ant AntID) ([]QuestionRecord, error)
	QuestionsOn(ctx context.Context, on domain.Ref) ([]QuestionRecord, error)
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

// RunStart is the start of a run with its effect on the ant (design D22):
// the ant's new state and budget, and the stored changes given to the
// run.
type RunStart struct {
	// Ant is the ant's record, moved to starting.
	Ant AntRecord
	// Run is the run; its ID is ignored.
	Run RunRecord
	// Consumed are the stored changes the run gets as input.
	Consumed []int64
	At       time.Time
}

// RunStarts records the start of a run in one transaction, so that a
// crash keeps the ant, the run and the consumed changes all or none
// (design D22: the effect of a decision is recorded with its action).
type RunStarts interface {
	// BeginRun saves s.Ant, stores s.Run as active and marks s.Consumed
	// as given to a run, and returns the run's ID.
	BeginRun(ctx context.Context, s RunStart) (RunID, error)
}

// RoundRequest is one /anthill review on a PR (spec pr-shepherd,
// "Developer-requested rounds"), recorded by the instance that carries
// out the command.
type RoundRequest struct {
	PR domain.Ref
	// CommentID is the comment holding the command. A PR has at most one
	// request per comment.
	CommentID string
	At        time.Time
	// Round is the round of this instance that served the request, or 0
	// while it is pending or when another instance's round served it.
	Round RoundID
}

// RoundRequests records the requested rounds of the shepherd, and starts
// a round with its effect on them in one transaction (design D22).
type RoundRequests interface {
	// RequestRound stores a pending request. A request for a PR and
	// comment already stored, pending or served, is ignored.
	RequestRound(ctx context.Context, r RoundRequest) error
	// RoundRequests lists a PR's pending requests, oldest first.
	RoundRequests(ctx context.Context, pr domain.Ref) ([]RoundRequest, error)
	// DropRoundRequest marks a pending request as served by another
	// instance, whose acknowledgement of the command is on the PR (spec
	// agent-conversation, "Commands run once"). A request that is not
	// pending is left as it is.
	DropRoundRequest(ctx context.Context, pr domain.Ref, commentID string) error
	// BeginRound stores r as an active round, as StartRound does, and for
	// a requested round marks every pending request of r.PR as served by
	// it, and returns the round's ID.
	BeginRound(ctx context.Context, r RoundRecord) (RoundID, error)
}
