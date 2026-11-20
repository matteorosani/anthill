package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// The services that implement the conversation's seams.
var (
	_ ConversationIntake = (*Conversation)(nil)
	_ CommandAcks        = (*Conversation)(nil)
	_ Answers            = conversationAnswers{}
	_ CommandClaims      = (*Claiming)(nil)
	_ AntCommands        = (*AntSupervisor)(nil)
	// The shepherd serves /anthill review, and acknowledges it through
	// the conversation.
	_ ReviewRequests = (*Shepherd)(nil)
)

// ConversationStore is the part of the local store the conversation
// uses.
type ConversationStore interface {
	ports.SessionIndex
	ports.Questions
}

// AntCommands is what the conversation asks of the ant supervisor: a
// trigger after an answer, which lets the playbook start the run that
// resumes the session (design D22), and the queen actions behind the
// /anthill stop and retry commands.
type AntCommands interface {
	Trigger(ctx context.Context, id ports.AntID) error
	Stop(ctx context.Context, id ports.AntID, reason string) error
	Retry(ctx context.Context, id ports.AntID) error
}

// CommandClaims is the part of Claiming the commands use: this
// instance's id for the markers of its replies, the holder of a task
// claim as of a command comment, and a review claim taken just to reply
// on a PR that no ant works on.
type CommandClaims interface {
	Instance(ctx context.Context, ref domain.Ref) (string, error)
	HolderAt(ctx context.Context, ref domain.Ref, kind domain.ClaimKind, comment domain.Comment) (holder, instance string, err error)
	Acquire(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) (ports.HeldClaim, error)
	Release(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) error
}

// ReviewRequests is the shepherd's part of /anthill review (spec
// pr-shepherd, "Developer-requested rounds"). CanReview fails when this
// instance does not review the PR: reviewing is off for this machine or
// repository, or the review runner is missing. RequestRound records a
// pending requested round, idempotent by the command comment's ID, and
// triggers the shepherd. The round, holding the PR's review claim, then
// acknowledges the command through CommandAcks: the acknowledgement is
// the record, across instances, that the request was served.
type ReviewRequests interface {
	CanReview(ctx context.Context, pr domain.Ref) error
	RequestRound(ctx context.Context, pr domain.Ref, commentID string) error
}

// FixRequests opts a human PR in to the shepherd's auto-fix for one
// thread on /anthill fix (spec pr-shepherd). The PR's review claim
// decides which instance takes it on: OptInFix returns false when
// another instance did. It must be idempotent by the command comment's
// ID.
type FixRequests interface {
	OptInFix(ctx context.Context, pr domain.Ref, thread, commandComment string) (bool, error)
}

// ConversationDeps are the ports and services the conversation uses.
type ConversationDeps struct {
	Store    ConversationStore
	Tracker  TrackerRoute
	CodeHost CodeHostRoute
	Claims   CommandClaims
	Ants     AntCommands
	// Reviews and Fixes are optional until the shepherd carries them
	// out: without them, /anthill review and /anthill fix on a human PR
	// are left to the instances that have them.
	Reviews ReviewRequests
	Fixes   FixRequests
	// Queen posts the queen's answers given in the console, on the
	// ticket or PR of the question. Optional: without it, answering from
	// the console fails.
	Queen    QueenCommentRoute
	Clock    ports.Clock
	Notifier ports.Notifier
	// Staleness returns the question-staleness setting of the global
	// configuration.
	Staleness func() time.Duration
}

// Conversation carries out the conversation between ants and developers
// on the provider (spec agent-conversation): it finds the answers to
// pending questions, reports stale questions to the queen, and executes
// /anthill commands once across instances. The ant tools post the
// questions themselves (ask_question).
type Conversation struct {
	deps ConversationDeps
}

// NewConversation returns the conversation service.
func NewConversation(deps ConversationDeps) *Conversation {
	return &Conversation{deps: deps}
}

// Answers is the conversation's source of answers for the supervisor
// (AntSupervisorDeps.Answers).
func (c *Conversation) Answers() Answers {
	return conversationAnswers{c}
}

// conversationAnswers gives the supervisor the answers the conversation
// found.
type conversationAnswers struct{ c *Conversation }

// Answer is the answer found to the ant's latest question, for the
// playbook's snapshot of an ant awaiting input (design D22). It is nil
// while that question is pending, and when the ant asked none.
func (a conversationAnswers) Answer(ctx context.Context, ant ports.AntID) (*domain.ChangeEvent, error) {
	qs, err := a.c.deps.Store.AntQuestions(ctx, ant)
	if err != nil || len(qs) == 0 {
		return nil, err
	}
	last := qs[len(qs)-1]
	if last.AnsweredAt.IsZero() {
		return nil, nil
	}
	return last.Answer, nil
}

// HandleAnswer checks whether the change answers a pending question of in.Ant
// (domain.IsAnswer). The first comment that does is the answer: it is
// stored with the question, which is then answered, and the ant is
// triggered, so that the playbook starts the run that resumes its
// session with the answer, or queues it while capacity is full (spec
// agent-conversation, "Answers resume the session"). A change that
// already answered a question is handled again the same way, so a
// redelivered outbox event starts no second run and is not routed as a
// change.
func (c *Conversation) HandleAnswer(ctx context.Context, in ChangeInput) (bool, error) {
	e := in.Event
	if in.Ant == nil || e.Origin == domain.OriginAnthill || e.CommentID == "" {
		return false, nil
	}
	ant := in.Ant.ID
	qs, err := c.deps.Store.AntQuestions(ctx, ant)
	if err != nil {
		return false, fmt.Errorf("reading the questions of ant %d: %w", ant, err)
	}
	for _, q := range qs {
		if q.AnswerID == e.CommentID {
			return true, c.trigger(ctx, ant)
		}
	}
	var writers *domain.UserSet
	for _, q := range qs {
		if !q.AnsweredAt.IsZero() || q.On != e.Ref {
			continue
		}
		if writers == nil {
			set, err := c.writers(ctx, e.Ref, e.Actor)
			if err != nil {
				return false, err
			}
			writers = &set
		}
		pending := domain.PendingQuestion{On: q.On, CommentID: q.CommentID, Thread: q.Thread, Text: q.Text, AskedAt: q.AskedAt}
		if !domain.IsAnswer(pending, e, *writers) {
			continue
		}
		return true, c.recordAnswer(ctx, q, e)
	}
	return false, nil
}

// recordAnswer stores e as the answer to q and triggers the question's
// ant, so that the playbook starts the run that resumes its session.
func (c *Conversation) recordAnswer(ctx context.Context, q ports.QuestionRecord, e domain.ChangeEvent) error {
	answer := e
	q.AnsweredAt, q.AnswerID, q.Answer = c.deps.Clock.Now(), e.CommentID, &answer
	if err := c.deps.Store.SaveQuestion(ctx, q); err != nil {
		return fmt.Errorf("storing the answer to question %s: %w", q.CommentID, err)
	}
	return c.trigger(ctx, q.Ant)
}

// trigger asks the playbook what comes next for the ant.
func (c *Conversation) trigger(ctx context.Context, ant ports.AntID) error {
	if err := c.deps.Ants.Trigger(ctx, ant); err != nil {
		return fmt.Errorf("starting the run of ant %d after its answer: %w", ant, err)
	}
	return nil
}

// writers is the set of the given logins that have write access to the
// repository of ref: the tracker's for a ticket, the code host's for a
// PR.
func (c *Conversation) writers(ctx context.Context, ref domain.Ref, logins ...string) (domain.UserSet, error) {
	var can func(ctx context.Context, repo domain.Repository, login string) (bool, error)
	if ref.Kind == domain.KindPR {
		host, err := c.deps.CodeHost(ctx, ref.Repo)
		if err != nil {
			return domain.UserSet{}, fmt.Errorf("finding the code host of %s: %w", ref, err)
		}
		can = host.CanWrite
	} else {
		tracker, err := c.deps.Tracker(ctx, ref)
		if err != nil {
			return domain.UserSet{}, fmt.Errorf("finding the tracker of %s: %w", ref, err)
		}
		can = tracker.CanWrite
	}
	var ok []string
	asked := map[string]bool{}
	for _, login := range logins {
		if login == "" || asked[login] {
			continue
		}
		asked[login] = true
		w, err := can(ctx, ref.Repo, login)
		if err != nil {
			return domain.UserSet{}, fmt.Errorf("checking the write access of %s on %s: %w", login, ref.Repo, err)
		}
		if w {
			ok = append(ok, login)
		}
	}
	return domain.NewUserSet(ok...), nil
}

// CheckStale reports to the queen each question pending for longer than
// the question-staleness setting, once (spec agent-conversation,
// "Question timeout"). The question is marked stale, which the console
// shows; no run starts. Questions of ants that are no longer awaiting
// input are left alone.
func (c *Conversation) CheckStale(ctx context.Context) error {
	staleness := domain.DefaultQuestionStaleness
	if c.deps.Staleness != nil {
		staleness = c.deps.Staleness()
	}
	pending, err := c.deps.Store.PendingQuestions(ctx)
	if err != nil {
		return fmt.Errorf("listing the pending questions: %w", err)
	}
	now := c.deps.Clock.Now()
	var errs []error
	for _, q := range pending {
		if !q.StaleAt.IsZero() || now.Sub(q.AskedAt) < staleness {
			continue
		}
		rec, err := c.deps.Store.Ant(ctx, q.Ant)
		if err != nil {
			errs = append(errs, fmt.Errorf("reading ant %d: %w", q.Ant, err))
			continue
		}
		if s := rec.State(); s != domain.AntAwaitingInput {
			continue
		}
		q.StaleAt = now
		if err := c.deps.Store.SaveQuestion(ctx, q); err != nil {
			errs = append(errs, fmt.Errorf("marking question %s stale: %w", q.CommentID, err))
			continue
		}
		c.notify(ctx, ports.Notification{
			Kind:  ports.NotifyStaleQuestion,
			Title: "Question of the ant for " + rec.Item.String() + " is stale",
			Body:  fmt.Sprintf("%s: no answer for %s. Answer it, or abandon the ant.", q.On, now.Sub(q.AskedAt).Round(time.Minute)),
			Ref:   &rec.Item,
		})
	}
	return errors.Join(errs...)
}

// notify tells the queen; a notification is best effort.
func (c *Conversation) notify(ctx context.Context, n ports.Notification) {
	if c.deps.Notifier != nil {
		_ = c.deps.Notifier.Notify(ctx, n)
	}
}
