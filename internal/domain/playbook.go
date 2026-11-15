package domain

import "time"

// AntChange is one actionable change for an ant. The service classifies
// changes before the playbook sees them (spec change-reaction, "Relevance
// classification"), so informational changes and commands never reach it.
type AntChange struct {
	// ID is the service's key for the change, such as its outbox event or
	// stored change, so that it can record the decision's effect.
	ID    int64
	Event ChangeEvent
	// Findings are set when the change is the findings of a shepherd round
	// handed to the owning ant (spec change-reaction, "Shepherd findings
	// on ant PRs"). Event is then the round's summary comment.
	Findings []Finding
}

// AntPR is the ant PR as the playbook sees it.
type AntPR struct {
	Ref   Ref
	Draft bool
	// DraftForced reports that an earlier run ended partial or timed out
	// and no later run finished complete, so the PR must stay a draft
	// (spec ant-execution, "Draft PRs for incomplete work").
	DraftForced bool
}

// AntFinish is the finish of the active run, which Anthill has not
// carried out yet.
type AntFinish struct {
	Status     FinishStatus
	Summary    string
	OpenPoints []string
	// NewCommits is the number of commits on the branch after the base.
	NewCommits int
}

// ProposalState is where the follow-up proposal for an ant's stored
// changes stands (spec change-reaction, "Follow-ups start automatically by
// default").
type ProposalState string

// The proposal states.
const (
	// ProposalNone: no follow-up proposal is open.
	ProposalNone ProposalState = ""
	// ProposalPending: the proposal waits for the queen.
	ProposalPending ProposalState = "pending"
	// ProposalAccepted: the queen accepted it, and the run is due.
	ProposalAccepted ProposalState = "accepted"
	// ProposalDeclined: the queen declined it. The stored changes are
	// kept, and the next actionable change brings a new proposal for all
	// of them: storing that change clears this state.
	ProposalDeclined ProposalState = "declined"
)

// AntSnapshot is the state the default playbook decides from (design
// D22). The service builds it from the session index, the local store and
// the configuration. Hold, capacity, claims and write access are left out
// on purpose: the services apply them to the decision, whatever it is.
type AntSnapshot struct {
	State AntState
	// QueuedFrom is the between-runs state a queued ant was in before it
	// was queued, taken from its history, or "" for an ant queued at
	// acceptance. A queued ant is decided as in that state, so a run that
	// is still due comes out again when a slot frees.
	QueuedFrom AntState
	// Runs is the number of runs the ant had so far. 0 means its next run
	// is its first and gets the task brief.
	Runs int
	// LastRun is the role of the ant's last or active run:
	// RoleTaskRun or RoleFollowUpRun.
	LastRun AgentRole
	// FollowUpRuns is the number of follow-up runs on the ant PR so far,
	// for the follow-up limit. Only follow-up runs started for changes,
	// which get a new follow-up budget, count: a run that resumes a
	// follow-up after an answer, and a retry or continue of one, do not.
	FollowUpRuns int
	// Resumable reports whether the next run can resume the session: a
	// session id is known and the runner can resume. Otherwise the run
	// gets a summary brief (spec ant-execution, "Sessions are persisted
	// for resumption").
	Resumable bool
	// Base is the ant's base budget, of which the follow-up budget is a
	// share.
	Base time.Duration
	// PR is the ant PR, or nil before it is opened.
	PR *AntPR
	// Finish is the finish of the active run that Anthill has not carried
	// out yet. It is set only while the ant is finishing.
	Finish *AntFinish
	// Answer is the answer found to the ant's pending question, while the
	// ant is awaiting input.
	Answer *ChangeEvent
	// Unrouted are the actionable changes neither delivered nor stored
	// yet, oldest first. Holding all of them lets the changes found in one
	// poll go to one follow-up run.
	Unrouted []AntChange
	// Stored are the stored changes not yet given to a run, oldest first.
	Stored []AntChange
	// Proposal is where the follow-up proposal for Stored stands.
	Proposal ProposalState
	// AutoFollowUp, FollowUpLimit and FollowUpPercent are the
	// repository's resolved auto-follow-up, follow-up-limit and
	// follow-up-budget settings (Effective). A FollowUpLimit of 0 allows
	// no follow-up run.
	AutoFollowUp    bool
	FollowUpLimit   int
	FollowUpPercent int
}

// AntDecision is the one next action the default playbook returns for an
// ant. It is one of StartRun, DeliverChange, StoreChange,
// ProposeFollowUp, OpenPR, FinishWithNoChange, CompleteFinish and Nothing. The unexported
// method seals the interface: no other package can add a decision.
type AntDecision interface {
	antDecision()
}

// BriefKind is the brief a run starts with.
type BriefKind string

// The brief kinds.
const (
	// BriefNone: the run resumes the session, so it needs no brief.
	BriefNone BriefKind = ""
	// BriefTask: the ant's first run gets the task brief.
	BriefTask BriefKind = "task"
	// BriefSummary: a run that cannot resume the session gets a brief
	// summarizing the earlier work.
	BriefSummary BriefKind = "summary"
)

// StartRun starts a run of the ant. Its effect, recorded with the start:
// the ant is starting, the changes are marked as given to the run, the
// answer as given, and the follow-up proposal is closed.
type StartRun struct {
	Kind  AgentRole
	Brief BriefKind
	// Answer is the answer to the ant's question, if the run resumes
	// after one.
	Answer *ChangeEvent
	// Changes are the stored changes the run gets as input.
	Changes []AntChange
	// NewBudget is the follow-up budget of a follow-up run started for
	// changes. 0 keeps the ant's budget.
	NewBudget time.Duration
}

// DeliverChange gives a change to the active run. Its effect: the change
// is no longer unrouted.
type DeliverChange struct {
	Change AntChange
}

// StoreChange keeps a change for the ant's next run, and the service
// notifies the queen. Its effect: the change moves from unrouted to
// stored, and a declined follow-up proposal is cleared, so that the
// stored changes can be proposed again.
type StoreChange struct {
	Change AntChange
	// FollowUpLimitReached tells the service to tell the queen that the
	// follow-up limit is reached, so the change starts no run.
	FollowUpLimitReached bool
}

// ProposeFollowUp offers the queen a follow-up run for the stored
// changes. Its effect: the proposal is pending.
type ProposeFollowUp struct {
	Changes []AntChange
}

// OpenPR pushes the branch and opens the ant PR for a finish whose agent
// did not open it. The title follows the ticket and the body is the finish
// summary, with the open points. Its effect: the ant has a PR.
type OpenPR struct {
	// Draft is set after a partial finish.
	Draft      bool
	Summary    string
	OpenPoints []string
}

// FinishWithNoChange carries out a task run's no-change finish: the
// explanation is posted on the ticket, the hold label is added and the
// task claim released. Its effect: the ant is done.
type FinishWithNoChange struct {
	Explanation string
}

// CompleteFinish completes a finish once the ant PR is open, or a
// follow-up run's no-change finish, which leaves the PR as it is (spec
// ant-execution, "Finishing with a pull request", "Draft PRs for
// incomplete work", "No change needed"): the outcome is recorded and the
// task claim parked. Its effect: the ant is succeeded, and its PR is a
// draft or ready as the decision says.
type CompleteFinish struct {
	Status FinishStatus
	// MarkDraft is set after partial: the PR becomes a draft, whatever the
	// agent chose, and its body lists OpenPoints.
	MarkDraft  bool
	OpenPoints []string
	// MarkReady is set after complete when the PR is a draft that an
	// earlier partial or timed-out run forced.
	MarkReady bool
}

// Nothing means nothing is due for the ant.
type Nothing struct{}

func (StartRun) antDecision()           {}
func (DeliverChange) antDecision()      {}
func (StoreChange) antDecision()        {}
func (ProposeFollowUp) antDecision()    {}
func (OpenPR) antDecision()             {}
func (FinishWithNoChange) antDecision() {}
func (CompleteFinish) antDecision()     {}
func (Nothing) antDecision()            {}

// DecideAnt is the default playbook for ants (design D22): it returns the
// one next action for the ant in s. It decides from the state alone, not
// from the order of events, so a missed or repeated event or a restart
// gives the same decision. The service carries out the decision, records
// its effect, and calls DecideAnt again until it returns Nothing; after
// an effect is recorded, the same decision never comes back.
//
// The decisions come in this order:
//  1. Each unrouted change is delivered to an active run that has not
//     finished (starting, running or awaiting-extension), dropped for a
//     final ant, and stored otherwise. A finishing ant stores it, as a
//     change detected after finish is handled as for an ant without an
//     active run (spec change-reaction, "Delivery to an active run").
//  2. A finishing ant's finish is carried out.
//  3. An ant awaiting input with an answer resumes with the answer and the
//     stored changes, in a run of the same kind as the one that asked,
//     with the budget kept.
//  4. A succeeded ant with stored changes gets a follow-up run, started
//     at once with auto-follow-up or once the queen accepts its proposal,
//     while the follow-up limit allows.
//  5. An ant queued at acceptance starts its first run with the task
//     brief.
//
// A queued ant is decided as in the state it was queued from. Retry and
// continue are queen actions that the services carry out, capacity
// included, so an ant queued from failed, stopped or timed-out gets
// Nothing: the service that queued it starts its run. An accepted item
// whose ant can start at once is created starting by NewAnt, outside the
// playbook.
func DecideAnt(s AntSnapshot) AntDecision {
	state := s.State
	if state == AntQueued && s.QueuedFrom != "" {
		state = s.QueuedFrom
	}
	if len(s.Unrouted) > 0 {
		return route(s, state, s.Unrouted[0])
	}
	switch state {
	case AntFinishing:
		return decideFinish(s)
	case AntAwaitingInput:
		if s.Answer != nil {
			return StartRun{Kind: s.LastRun, Brief: s.resumeBrief(), Answer: s.Answer, Changes: s.Stored}
		}
	case AntSucceeded:
		return decideFollowUp(s)
	case AntQueued:
		if s.Runs == 0 {
			return StartRun{Kind: RoleTaskRun, Brief: BriefTask, Changes: s.Stored}
		}
	}
	return Nothing{}
}

// route decides where one unrouted change goes.
func route(s AntSnapshot, state AntState, c AntChange) AntDecision {
	switch {
	case state.Final():
		return Nothing{}
	case state.HasActiveRun() && state != AntFinishing:
		return DeliverChange{Change: c}
	}
	return StoreChange{Change: c, FollowUpLimitReached: state == AntSucceeded && s.followUpLimitReached()}
}

// decideFinish carries out a finishing ant's finish.
func decideFinish(s AntSnapshot) AntDecision {
	f := s.Finish
	if f == nil {
		return Nothing{}
	}
	if s.LastRun == RoleTaskRun {
		switch {
		case f.Status == FinishNoChange:
			return FinishWithNoChange{Explanation: f.Summary}
		case s.PR == nil:
			return OpenPR{Draft: f.Status == FinishPartial, Summary: f.Summary, OpenPoints: f.OpenPoints}
		}
	}
	d := CompleteFinish{Status: f.Status}
	switch f.Status {
	case FinishPartial:
		d.MarkDraft, d.OpenPoints = true, f.OpenPoints
	case FinishComplete:
		d.MarkReady = s.PR != nil && s.PR.Draft && s.PR.DraftForced
	}
	return d
}

// decideFollowUp decides the follow-up run of a succeeded ant.
func decideFollowUp(s AntSnapshot) AntDecision {
	if len(s.Stored) == 0 || s.followUpLimitReached() {
		return Nothing{}
	}
	if s.AutoFollowUp || s.Proposal == ProposalAccepted {
		return StartRun{
			Kind:      RoleFollowUpRun,
			Brief:     s.resumeBrief(),
			Changes:   s.Stored,
			NewBudget: FollowUpBudget(s.Base, s.FollowUpPercent),
		}
	}
	if s.Proposal == ProposalNone {
		return ProposeFollowUp{Changes: s.Stored}
	}
	return Nothing{}
}

// resumeBrief is the brief of a run that continues the ant's work.
func (s AntSnapshot) resumeBrief() BriefKind {
	if s.Resumable {
		return BriefNone
	}
	return BriefSummary
}

// followUpLimitReached reports whether the ant PR has had as many
// follow-up runs as the limit allows. A limit of 0 allows none.
func (s AntSnapshot) followUpLimitReached() bool {
	return s.FollowUpRuns >= s.FollowUpLimit
}
