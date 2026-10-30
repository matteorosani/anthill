package app

import (
	"context"
	"fmt"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// redactor is the redactor of the configuration in use.
func (t *AntTools) redactor() *domain.Redactor {
	if t.opts.Redactor != nil {
		if r := t.opts.Redactor(); r != nil {
			return r
		}
	}
	return t.builtin
}

// safe makes text fit to post on behalf of an agent: absolute local paths
// rewritten and secrets redacted (spec agent-conversation, "Comment
// content safety").
func (t *AntTools) safe(c *toolCall, text string) string {
	return t.redactor().Redact(domain.RewritePaths(text, c.worktree(), t.opts.Home))
}

// summary is safe text cut to the summary limit, for summaries and
// progress reports.
func (t *AntTools) summary(c *toolCall, text string) string {
	return domain.Truncate(t.safe(c, text), t.opts.SummaryLimit)
}

// worktree is the call's worktree: the ant's, or the review or fix
// agent's.
func (c *toolCall) worktree() string {
	if c.session.Role.IsRun() {
		return c.ant.Worktree
	}
	return c.session.Worktree
}

// MessageNoPR is the refusal of a call that needs the ant PR before it is
// open.
const MessageNoPR = "you have no pull request yet: push and open one with open_pull_request first"

// target is where a run's where argument points: the item, or the ant PR.
func (c *toolCall) target(where string) (domain.Ref, error) {
	if where == wherePR {
		if c.ant.PR == nil {
			return domain.Ref{}, domain.RefuseTool("%s", MessageNoPR)
		}
		return *c.ant.PR, nil
	}
	return c.ant.Item, nil
}

// antComment is an Anthill comment of kind on ref, signed with the
// instance of the call's claim.
func (c *toolCall) antComment(kind domain.CommentKind, ref domain.Ref, id, body string) domain.AnthillComment {
	return domain.AnthillComment{
		Role:   domain.RoleAnt,
		Marker: domain.Marker{Version: domain.MarkerVersion, ID: id, Instance: c.claim.Instance, Kind: kind, Ref: ref.String()},
		Body:   body,
	}
}

// post posts an Anthill comment on an item, through its tracker, or on a
// PR, through its code host.
func (t *AntTools) post(ctx context.Context, ref domain.Ref, ac domain.AnthillComment) (domain.Comment, error) {
	if ref.Kind == domain.KindPR {
		host, err := t.codeHost(ctx, ref.Repo)
		if err != nil {
			return domain.Comment{}, err
		}
		return host.AddComment(ctx, ref, ac)
	}
	tracker, err := t.tracker(ctx, ref)
	if err != nil {
		return domain.Comment{}, err
	}
	return tracker.AddComment(ctx, ref, ac)
}

// postedResult is the result of a tool that posts a comment.
type postedResult struct {
	URL     string `json:"url"`
	Message string `json:"message,omitempty"`
}

// questionArgs are the arguments of ask_question.
type questionArgs struct {
	Text  string `json:"text"`
	Where string `json:"where"`
}

// MessageQuestionPosted is what ask_question returns.
const MessageQuestionPosted = "question posted: your run ends now, with your session saved; a later run resumes it with the answer"

// askQuestion posts a question comment, on the item or, by default in a
// follow-up run, on the ant PR. The ant becomes awaiting-input, the queen
// is notified, and Anthill ends the run with its session saved (spec
// agent-conversation, "Ants ask questions through the ticket").
func (t *AntTools) askQuestion(ctx context.Context, c *toolCall) (any, error) {
	var args questionArgs
	if err := decodeArgs(c.args, &args); err != nil {
		return nil, err
	}
	if args.Where == "" {
		args.Where = whereItem
		if c.session.Role == domain.RoleFollowUpRun {
			args.Where = wherePR
		}
	}
	ref, err := c.target(args.Where)
	if err != nil {
		return nil, err
	}
	text := t.safe(c, args.Text)
	posted, err := t.post(ctx, ref, c.antComment(domain.CommentQuestion, ref, "", text))
	if err != nil {
		return nil, fmt.Errorf("posting the question: %w", err)
	}
	now := t.clock.Now()
	if err := t.store.SaveQuestion(ctx, ports.QuestionRecord{CommentID: posted.ID, Ant: c.ant.ID, On: ref, AskedAt: now}); err != nil {
		return nil, err
	}
	rec := c.ant
	ant, err := restoreAnt(rec)
	if err != nil {
		return nil, err
	}
	if err := toState(ant, domain.AntAwaitingInput, now, "asked a question"); err != nil {
		return nil, err
	}
	if err := t.store.SaveAnt(ctx, withAnt(rec, ant)); err != nil {
		return nil, err
	}
	if t.deps.Notifier != nil {
		item := rec.Item
		n := ports.Notification{Kind: ports.NotifyQuestion, Title: "The ant for " + item.String() + " asks a question", Body: domain.Truncate(text, 200), Ref: &item}
		_ = t.deps.Notifier.Notify(ctx, n) // a notification is best effort
	}
	if err := t.deps.Runs.EndRun(context.WithoutCancel(ctx), c.session.Ant, c.session.Run, "asked a question"); err != nil {
		return nil, fmt.Errorf("the question was posted, but ending the run failed: %w", err)
	}
	return postedResult{URL: posted.URL, Message: MessageQuestionPosted}, nil
}

// toState moves a run's ant to state. An ant still starting is running
// first: its agent is calling tools.
func toState(ant *domain.Ant, state domain.AntState, at time.Time, reason string) error {
	if ant.State() == domain.AntStarting {
		if err := ant.Transition(domain.AntRunning, at, "the agent called an ant tool"); err != nil {
			return err
		}
	}
	return ant.Transition(state, at, reason)
}

// progressArgs are the arguments of report_progress.
type progressArgs struct {
	Text string `json:"text"`
}

// reportProgress writes the run's one status comment on the item: the
// first call posts it and the later ones edit it (design, Risks: comment
// noise).
func (t *AntTools) reportProgress(ctx context.Context, c *toolCall) (any, error) {
	var args progressArgs
	if err := decodeArgs(c.args, &args); err != nil {
		return nil, err
	}
	item := c.ant.Item
	id := fmt.Sprintf("status-%d-%d", c.session.Ant, c.session.Run)
	ac := c.antComment(domain.CommentStatus, item, id, t.summary(c, args.Text))
	tracker, err := t.tracker(ctx, item)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	commentID := t.statuses[c.key]
	t.mu.Unlock()
	var posted domain.Comment
	if commentID == "" {
		posted, err = tracker.AddComment(ctx, item, ac)
	} else {
		posted, err = tracker.EditComment(ctx, item, commentID, ac)
	}
	if err != nil {
		return nil, fmt.Errorf("writing the status comment: %w", err)
	}
	t.mu.Lock()
	if _, ok := t.sessions[c.key]; ok {
		t.statuses[c.key] = posted.ID
	}
	t.mu.Unlock()
	return postedResult{URL: posted.URL}, nil
}

// commentArgs are the arguments of comment.
type commentArgs struct {
	Where string `json:"where"`
	Body  string `json:"body"`
}

// comment posts an agent-written comment on the item or the ant PR.
func (t *AntTools) comment(ctx context.Context, c *toolCall) (any, error) {
	var args commentArgs
	if err := decodeArgs(c.args, &args); err != nil {
		return nil, err
	}
	ref, err := c.target(args.Where)
	if err != nil {
		return nil, err
	}
	posted, err := t.post(ctx, ref, c.antComment(domain.CommentReply, ref, "", t.safe(c, args.Body)))
	if err != nil {
		return nil, fmt.Errorf("posting the comment: %w", err)
	}
	return postedResult{URL: posted.URL}, nil
}

// replyArgs are the arguments of reply_to_thread.
type replyArgs struct {
	Thread  string `json:"thread"`
	Body    string `json:"body"`
	Resolve bool   `json:"resolve"`
}

// replyResult is the result of reply_to_thread.
type replyResult struct {
	URL      string `json:"url"`
	Resolved bool   `json:"resolved"`
}

// replyToThread answers in a review thread of the ant PR, and resolves it
// when asked (spec ant-tools, "Agent replies to a review thread").
func (t *AntTools) replyToThread(ctx context.Context, c *toolCall) (any, error) {
	var args replyArgs
	if err := decodeArgs(c.args, &args); err != nil {
		return nil, err
	}
	pr, err := c.target(wherePR)
	if err != nil {
		return nil, err
	}
	host, err := t.codeHost(ctx, pr.Repo)
	if err != nil {
		return nil, err
	}
	posted, err := host.ReplyToThread(ctx, pr, args.Thread, c.antComment(domain.CommentReply, pr, "", t.safe(c, args.Body)))
	if err != nil {
		return nil, fmt.Errorf("replying in thread %s: %w", args.Thread, err)
	}
	if args.Resolve {
		if err := host.ResolveThread(ctx, pr, args.Thread); err != nil {
			return nil, fmt.Errorf("the reply was posted, but resolving thread %s failed: %w", args.Thread, err)
		}
	}
	return replyResult{URL: posted.URL, Resolved: args.Resolve}, nil
}
