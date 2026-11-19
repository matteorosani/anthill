package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// commandTarget is what a command comment is about, as read fresh from
// the provider.
type commandTarget struct {
	// comment is the command comment as the provider has it now, and
	// comments every comment of the ticket or PR, review threads included,
	// to find the acknowledgement.
	comment  domain.Comment
	comments []domain.Comment
	place    domain.CommandPlace
	// prAuthor is the author of the PR the command is on, or "".
	prAuthor string
	// owned is set when some instance held the task claim of the
	// command's item when the command was posted: the command is on a
	// claimed ticket or its ant PR. On a PR, unset means a human PR.
	owned bool
	// released is set when this instance held that claim, for an
	// /anthill release that has released it since.
	released bool
}

// The ways the replies to a command comment get posted, from the least
// to the most direct. When a comment's commands give several, the most
// direct posts every reply.
type replier int

const (
	// replyNone: this instance posts no acknowledgement.
	replyNone replier = iota
	// replyUnderReviewClaim: a reply on a PR no ant works on, posted by
	// the instance that wins the PR's review claim for it.
	replyUnderReviewClaim
	// replyAsShepherd: this instance's shepherd took the command on and
	// holds the PR's review claim.
	replyAsShepherd
	// replyAsHolder: this instance is responsible for the item.
	replyAsHolder
)

// HandleCommands executes the /anthill commands of a human comment once across
// instances (spec agent-conversation, "Each command is executed once"):
//  1. a reply that acknowledges the comment, by a user with write
//     access, means it was handled, by this instance or another one;
//  2. each command is authorized (domain.AuthorizeCommand);
//  3. only the instance responsible for it acts: for a command on a
//     ticket or an ant PR, the holder of the item's task claim
//     (in.Responsible); for /anthill review, and /anthill fix on a human
//     PR, the shepherd of the instance that wins the PR's review claim;
//  4. the replies are posted in one acknowledgement: by the responsible
//     instance, by the shepherd that took the command on, or, for help,
//     refusals and wrong-place replies on a human PR, by the instance
//     that wins the PR's review claim to post them. A requested round is
//     acknowledged by the shepherd when it starts, so a comment holding
//     /anthill review gets no other acknowledgement.
//
// A crash between 3 and 4 executes the command again after the restart.
// That is safe: stop and retry check the ant's state first, and round and
// fix requests are idempotent by the command comment's ID.
func (c *Conversation) HandleCommands(ctx context.Context, in ChangeInput) (CommandResult, error) {
	e := in.Event
	switch {
	case e.Origin == domain.OriginAnthill, e.CommentID == "",
		e.Kind != domain.ChangeCommentAdded && e.Kind != domain.ChangeCommentEdited && e.Kind != domain.ChangeReviewSubmitted:
		return CommandResult{}, nil
	}
	names := domain.ParseCommands(e.Body)
	if len(names) == 0 {
		return CommandResult{}, nil
	}
	handled := CommandResult{Handled: true}
	t, err := c.commandTarget(ctx, in)
	if errors.Is(err, ports.ErrNotFound) {
		return handled, nil // the comment or its item is gone
	}
	if err != nil {
		return handled, err
	}
	authors := []string{e.Actor}
	for _, x := range t.comments {
		if m, ok := domain.FindMarker(x.Body); ok && m.Kind == domain.CommentReply && m.ID == domain.CommandAckID(e.CommentID) {
			authors = append(authors, x.Author)
		}
	}
	writers, err := c.writers(ctx, e.Ref, authors...)
	if err != nil {
		return handled, err
	}
	if _, acked := domain.FindCommandAck(t.comments, e.CommentID, writers); acked {
		return handled, nil
	}
	if err := c.resolveOwner(ctx, in, &t); err != nil {
		return handled, err
	}

	authz := domain.CommandContext{Commenter: e.Actor, Writers: writers, PRAuthor: t.prAuthor, Place: t.place}
	var (
		replies   []domain.CommandReply
		errs      []error
		how       = replyNone
		requested bool
	)
	for _, name := range names {
		r, err := c.execute(ctx, in, t, name, domain.AuthorizeCommand(name, authz))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s on %s: %w", name, e.Ref, err))
			continue
		}
		handled.Route = handled.Route || r.route
		requested = requested || r.requested
		if r.reply != "" && r.how != replyNone {
			replies = append(replies, domain.CommandReply{Name: name, Text: r.reply})
			how = max(how, r.how)
		}
	}
	if len(errs) > 0 || requested || len(replies) == 0 {
		return handled, errors.Join(errs...)
	}
	return handled, c.postReplies(ctx, e, how, replies)
}

// commandOutcome is what executing one command did.
type commandOutcome struct {
	reply string
	// how is who may post reply.
	how replier
	// route: the change goes to the item's ant as actionable.
	route bool
	// requested: a round was requested, which the shepherd acknowledges.
	requested bool
}

// execute carries out one command, if this instance is responsible for
// it, and returns its reply.
func (c *Conversation) execute(ctx context.Context, in ChangeInput, t commandTarget, name domain.CommandName, d domain.CommandDecision) (commandOutcome, error) {
	e := in.Event
	humanPR := e.Ref.Kind == domain.KindPR && !t.owned
	if d.Outcome != domain.CommandRun {
		switch {
		case in.Responsible:
			return commandOutcome{reply: d.Reply, how: replyAsHolder}, nil
		case humanPR:
			return commandOutcome{reply: d.Reply, how: replyUnderReviewClaim}, nil
		}
		// An unclaimed ticket, or an item another instance holds.
		return commandOutcome{}, nil
	}
	switch {
	case name == domain.CmdReview:
		return c.requestRound(ctx, e)
	case name == domain.CmdFix && humanPR:
		return c.optInFix(ctx, e)
	case name == domain.CmdRelease && t.released:
		// Claiming releases the claim on every instance on its own (spec
		// work-claiming, "Manual override"); the instance it released
		// acknowledges it, even once the claim is gone.
		return commandOutcome{reply: domain.ReplyReleased, how: replyAsHolder}, nil
	case !in.Responsible:
		return commandOutcome{}, nil
	}
	o := commandOutcome{how: replyAsHolder}
	if in.Ant == nil {
		o.reply = domain.ReplyNoAnt
		return o, nil
	}
	var err error
	switch name {
	case domain.CmdStop:
		o.reply, err = c.stop(ctx, e, in.Ant.ID)
	case domain.CmdRetry:
		o.reply, err = c.retry(ctx, in.Ant.ID)
	case domain.CmdRelease:
		o.reply = domain.ReplyReleased
	case domain.CmdFix:
		o.reply, o.route = domain.ReplyFixForwarded, true
	default:
		err = fmt.Errorf("command %s has no execution", name)
	}
	return o, err
}

// stop carries out /anthill stop through the supervisor's stop.
func (c *Conversation) stop(ctx context.Context, e domain.ChangeEvent, ant ports.AntID) (string, error) {
	err := c.deps.Ants.Stop(ctx, ant, fmt.Sprintf("%s by %s", domain.CmdStop, e.Actor))
	var refused *ActionRefusedError
	switch {
	case errors.As(err, &refused):
		return domain.ReplyNoActiveRun, nil
	case err != nil:
		return "", err
	}
	return domain.ReplyStopped, nil
}

// retry carries out /anthill retry through the supervisor's retry.
func (c *Conversation) retry(ctx context.Context, ant ports.AntID) (string, error) {
	err := c.deps.Ants.Retry(ctx, ant)
	var refused *ActionRefusedError
	switch {
	case errors.As(err, &refused):
		return fmt.Sprintf(domain.ReplyCannotRetry, refused.State), nil
	case err != nil:
		return "", err
	}
	return domain.ReplyRetried, nil
}

// requestRound hands /anthill review to the shepherd. An instance that
// does not review the PR leaves the request to one that does; the round,
// once started, acknowledges it.
func (c *Conversation) requestRound(ctx context.Context, e domain.ChangeEvent) (commandOutcome, error) {
	if c.deps.Reviews == nil || e.Ref.Kind != domain.KindPR {
		return commandOutcome{}, nil
	}
	if err := c.deps.Reviews.CanReview(ctx, e.Ref); err != nil {
		return commandOutcome{}, nil //nolint:nilerr // not this instance's to review
	}
	if err := c.deps.Reviews.RequestRound(ctx, e.Ref, e.CommentID); err != nil {
		return commandOutcome{}, err
	}
	return commandOutcome{requested: true}, nil
}

// optInFix hands /anthill fix on a human PR to the shepherd.
func (c *Conversation) optInFix(ctx context.Context, e domain.ChangeEvent) (commandOutcome, error) {
	if c.deps.Fixes == nil {
		return commandOutcome{}, nil
	}
	won, err := c.deps.Fixes.OptInFix(ctx, e.Ref, e.ThreadID, e.CommentID)
	if err != nil || !won {
		return commandOutcome{}, err
	}
	return commandOutcome{reply: domain.ReplyFixOptedIn, how: replyAsShepherd}, nil
}

// postReplies posts the acknowledgement of a command comment. A reply
// under the review claim first wins the PR's review claim, and releases
// it afterwards; when another instance wins it, that instance replies.
func (c *Conversation) postReplies(ctx context.Context, e domain.ChangeEvent, how replier, replies []domain.CommandReply) error {
	role := domain.RoleAnt
	if how != replyAsHolder {
		role = domain.RoleShepherd
	}
	if how == replyUnderReviewClaim {
		_, err := c.deps.Claims.Acquire(ctx, e.Ref, domain.ClaimReview)
		var taken *ClaimTakenError
		if errors.As(err, &taken) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claiming %s to reply: %w", e.Ref, err)
		}
		posted := c.acknowledge(ctx, e.Ref, e.CommentID, e.ThreadID, role, domain.RenderCommandAck(replies))
		return errors.Join(posted, c.deps.Claims.Release(ctx, e.Ref, domain.ClaimReview))
	}
	return c.acknowledge(ctx, e.Ref, e.CommentID, e.ThreadID, role, domain.RenderCommandAck(replies))
}

// Acknowledge posts the shepherd's acknowledgement of the command comment
// commentID on a ticket or PR (CommandAcks).
func (c *Conversation) Acknowledge(ctx context.Context, on domain.Ref, commentID, text string) error {
	return c.acknowledge(ctx, on, commentID, "", domain.RoleShepherd, text)
}

// acknowledge posts the one reply that acknowledges the command comment,
// with the marker ID that makes every instance skip it from then on. The
// provider posts it once however often it is called (design D7). In a
// review thread, the reply goes into the thread.
func (c *Conversation) acknowledge(ctx context.Context, on domain.Ref, commentID, thread string, role domain.Role, text string) error {
	instance, err := c.deps.Claims.Instance(ctx, on)
	if err != nil {
		return err
	}
	ac := domain.AnthillComment{
		Role:   role,
		Body:   text,
		Marker: domain.Marker{Version: domain.MarkerVersion, ID: domain.CommandAckID(commentID), Instance: instance, Kind: domain.CommentReply, Ref: on.String()},
	}
	if on.Kind == domain.KindPR {
		host, herr := c.deps.CodeHost(ctx, on.Repo)
		if herr != nil {
			return herr
		}
		if thread != "" {
			_, err = host.ReplyToThread(ctx, on, thread, ac)
		} else {
			_, err = host.AddComment(ctx, on, ac)
		}
	} else {
		tracker, terr := c.deps.Tracker(ctx, on)
		if terr != nil {
			return terr
		}
		_, err = tracker.AddComment(ctx, on, ac)
	}
	if err != nil {
		return fmt.Errorf("acknowledging the commands of comment %s on %s: %w", commentID, on, err)
	}
	return nil
}

// commandTarget reads the ticket or PR of a command comment fresh.
func (c *Conversation) commandTarget(ctx context.Context, in ChangeInput) (commandTarget, error) {
	e := in.Event
	var t commandTarget
	if e.Ref.Kind != domain.KindPR {
		tracker, err := c.deps.Tracker(ctx, e.Ref)
		if err != nil {
			return t, fmt.Errorf("finding the tracker of %s: %w", e.Ref, err)
		}
		item, _, err := tracker.Item(ctx, e.Ref)
		if err != nil {
			return t, fmt.Errorf("reading %s: %w", e.Ref, err)
		}
		t.place, t.comments = domain.PlaceTicket, item.Comments
	} else {
		host, err := c.deps.CodeHost(ctx, e.Ref.Repo)
		if err != nil {
			return t, fmt.Errorf("finding the code host of %s: %w", e.Ref, err)
		}
		pr, _, err := host.PullRequest(ctx, e.Ref)
		if err != nil {
			return t, fmt.Errorf("reading %s: %w", e.Ref, err)
		}
		t.place, t.prAuthor, t.comments = domain.PlacePR, pr.Author, slices.Clone(pr.Comments)
		for _, th := range pr.Threads {
			t.comments = append(t.comments, th.Comments...)
			if th.ID == e.ThreadID {
				t.place = domain.PlaceThread
			}
		}
	}
	i := slices.IndexFunc(t.comments, func(x domain.Comment) bool { return x.ID == e.CommentID })
	if i < 0 {
		return t, fmt.Errorf("comment %s on %s: %w", e.CommentID, e.Ref, ports.ErrNotFound)
	}
	t.comment = t.comments[i]
	return t, nil
}

// resolveOwner finds who held the task claim of the command's item when
// the command was posted. Every instance finds the same answer whenever
// it reads, which tells an ant PR from a human PR, and lets the instance
// an /anthill release released acknowledge it after its claim is gone.
func (c *Conversation) resolveOwner(ctx context.Context, in ChangeInput, t *commandTarget) error {
	if in.Item.Kind != domain.KindItem {
		return nil // a human PR: no ticket, no task claim
	}
	holder, instance, err := c.deps.Claims.HolderAt(ctx, in.Item, domain.ClaimTask, t.comment)
	if err != nil {
		return fmt.Errorf("finding the holder of %s: %w", in.Item, err)
	}
	t.owned = holder != ""
	t.released = t.owned && holder == instance && !in.Responsible
	return nil
}
