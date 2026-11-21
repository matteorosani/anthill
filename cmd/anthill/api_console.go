package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/control"
	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
)

// The control methods of the queen console (spec queen-console). Each
// console command calls one of them.
const (
	methodProposals       = "proposals.list"
	methodExplain         = "proposals.explain"
	methodAccept          = "proposals.accept"
	methodDecline         = "proposals.decline"
	methodSnooze          = "proposals.snooze"
	methodUnhide          = "proposals.unhide"
	methodAnts            = "ants.list"
	methodAnt             = "ants.show"
	methodFollow          = "ants.follow"
	methodStop            = "ants.stop"
	methodAbandon         = "ants.abandon"
	methodRetry           = "ants.retry"
	methodContinue        = "ants.continue"
	methodCleanup         = "ants.cleanup"
	methodQuestions       = "questions.list"
	methodAnswer          = "questions.answer"
	methodExtensions      = "extensions.list"
	methodDecideExtension = "extensions.decide"
	methodFollowUps       = "followups.list"
	methodAcceptFollowUp  = "followups.accept"
	methodDeclineFollowUp = "followups.decline"
)

// The JSON-RPC error codes of the console methods, in the range JSON-RPC
// leaves to servers.
const (
	// codeConsoleUnavailable: the daemon runs no ants yet, so it has no
	// console to serve.
	codeConsoleUnavailable = -32002
	// codeNeedsConfirmation: a cleanup would delete unpushed work.
	codeNeedsConfirmation = -32003
)

// itemParams are the params of the methods on one item. Item is the
// reference as the queen typed it, such as #12 or AB#12.
type itemParams struct {
	Item string `json:"item"`
}

type acceptParams struct {
	Item          string `json:"item"`
	Runner        string `json:"runner,omitempty"`
	BudgetMinutes int    `json:"budget_minutes,omitempty"`
	BaseBranch    string `json:"base_branch,omitempty"`
}

type snoozeParams struct {
	Item string `json:"item"`
	// Seconds is the snooze's length.
	Seconds int64 `json:"seconds"`
}

type antsParams struct {
	All bool `json:"all,omitempty"`
}

type continueParams struct {
	Item string `json:"item"`
	// BudgetMinutes is the new budget; 0 is the ant's base budget.
	BudgetMinutes int `json:"budget_minutes,omitempty"`
}

type cleanupParams struct {
	Item    string `json:"item"`
	Confirm bool   `json:"confirm,omitempty"`
}

type answerParams struct {
	Item string `json:"item"`
	Text string `json:"text"`
}

type decideExtensionParams struct {
	Item     string `json:"item"`
	Decision string `json:"decision"`
	Minutes  int    `json:"minutes,omitempty"`
}

// itemJSON names an item: Name as the console prints it, such as #12,
// and its repository.
type itemJSON struct {
	Name       string `json:"name"`
	Repository string `json:"repository"`
}

func itemOf(ref domain.Ref) itemJSON {
	return itemJSON{Name: app.ItemName(ref), Repository: ref.Repo.String()}
}

type proposalJSON struct {
	Item       itemJSON  `json:"item"`
	Title      string    `json:"title"`
	Repository string    `json:"repository"`
	Type       string    `json:"type"`
	Priority   string    `json:"priority"`
	CreatedAt  time.Time `json:"created_at"`
	Runner     string    `json:"runner"`
}

type proposalsResult struct {
	Now       time.Time      `json:"now"`
	Proposals []proposalJSON `json:"proposals"`
	Warnings  []string       `json:"warnings,omitempty"`
}

type checkJSON struct {
	Prerequisite string `json:"prerequisite"`
	Met          bool   `json:"met"`
	Reason       string `json:"reason,omitempty"`
}

type explainResult struct {
	Item   itemJSON    `json:"item"`
	Title  string      `json:"title"`
	Checks []checkJSON `json:"checks"`
	Hidden bool        `json:"hidden,omitempty"`
	// Until is the end of a snooze that hides the item.
	Until time.Time `json:"until,omitzero"`
}

type snoozeResult struct {
	Until time.Time `json:"until"`
}

type unhideResult struct {
	WasHidden bool `json:"was_hidden"`
}

// antJSON is an ant as the console lists it. The durations are in whole
// seconds.
type antJSON struct {
	ID        int64     `json:"id"`
	Item      itemJSON  `json:"item"`
	State     string    `json:"state"`
	Runner    string    `json:"runner"`
	Branch    string    `json:"branch,omitempty"`
	PR        *itemJSON `json:"pr,omitempty"`
	Used      int64     `json:"used"`
	Remaining int64     `json:"remaining"`
	Total     int64     `json:"total"`
	NearLimit bool      `json:"near_limit,omitempty"`
	Since     time.Time `json:"since"`
	Reason    string    `json:"reason,omitempty"`
	FollowUp  string    `json:"follow_up,omitempty"`
}

func antOf(v app.AntView) antJSON {
	out := antJSON{
		ID: int64(v.ID), Item: itemOf(v.Item), State: string(v.State), Runner: v.Runner, Branch: v.Branch,
		Used: seconds(v.Used), Remaining: seconds(v.Remaining), Total: seconds(v.Total), NearLimit: v.NearLimit,
		Since: v.Since, Reason: v.Reason, FollowUp: string(v.FollowUp),
	}
	if v.PR != nil {
		pr := itemOf(*v.PR)
		out.PR = &pr
	}
	return out
}

func seconds(d time.Duration) int64 {
	return int64(d / time.Second)
}

type antResult struct {
	Ant antJSON `json:"ant"`
}

type antsResult struct {
	Ants []antJSON `json:"ants"`
}

type transitionJSON struct {
	From   string    `json:"from,omitempty"`
	To     string    `json:"to"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
}

type runJSON struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	Outcome   string    `json:"outcome,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

type antDetailResult struct {
	Ant     antJSON          `json:"ant"`
	History []transitionJSON `json:"history"`
	Runs    []runJSON        `json:"runs"`
}

// followItem is one streamed line of a followed run.
type followItem struct {
	Line    string `json:"line"`
	Dropped int    `json:"dropped,omitempty"`
}

// followResult ends a follow: the run ended with its outcome, or the
// daemon stopped.
type followResult struct {
	Ended   bool   `json:"ended"`
	Outcome string `json:"outcome,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Dropped int    `json:"dropped,omitempty"`
}

type cleanupResult struct {
	RemoveWorktree     bool `json:"remove_worktree"`
	DeleteLocalBranch  bool `json:"delete_local_branch"`
	DeleteRemoteBranch bool `json:"delete_remote_branch"`
}

type questionJSON struct {
	Item    itemJSON  `json:"item"`
	On      itemJSON  `json:"on"`
	Text    string    `json:"text"`
	AskedAt time.Time `json:"asked_at"`
	Stale   bool      `json:"stale,omitempty"`
}

type questionsResult struct {
	Now       time.Time      `json:"now"`
	Questions []questionJSON `json:"questions"`
}

type answerResult struct {
	URL string `json:"url,omitempty"`
}

type extensionJSON struct {
	Item      itemJSON  `json:"item"`
	Minutes   int       `json:"minutes"`
	Reason    string    `json:"reason"`
	At        time.Time `json:"at"`
	Remaining int64     `json:"remaining"`
}

type extensionsResult struct {
	Extensions []extensionJSON `json:"extensions"`
}

type decisionResult struct {
	Status  string `json:"status"`
	Granted int    `json:"granted"`
	Message string `json:"message,omitempty"`
}

type followUpJSON struct {
	Item    itemJSON  `json:"item"`
	PR      *itemJSON `json:"pr,omitempty"`
	Changes []string  `json:"changes"`
}

type followUpsResult struct {
	FollowUps []followUpJSON `json:"follow_ups"`
}

// consoleHandler is a console method's handler once the console is known
// to be there.
type consoleHandler func(ctx context.Context, c *app.Console, raw json.RawMessage, stream control.Stream) (any, error)

// registerConsoleAPI registers the console methods. A nil console, in a
// daemon that runs no ants yet, answers each of them with
// codeConsoleUnavailable.
func registerConsoleAPI(srv *control.Server, c *app.Console) {
	handle := func(method string, h consoleHandler) {
		srv.Handle(method, func(ctx context.Context, raw json.RawMessage, stream control.Stream) (any, error) {
			if c == nil {
				return nil, &control.Error{Code: codeConsoleUnavailable, Message: "the daemon does not run ants yet"}
			}
			out, err := h(ctx, c, raw, stream)
			return out, codedError(err)
		})
	}
	handle(methodProposals, handleProposals)
	handle(methodUsage, handleUsage)
	handle(methodExplain, withItem(func(ctx context.Context, c *app.Console, item domain.Ref) (any, error) {
		x, err := c.Explain(ctx, item)
		if err != nil {
			return nil, err
		}
		out := explainResult{Item: itemOf(x.Item), Title: x.Title, Checks: []checkJSON{}, Hidden: x.Hidden, Until: x.Until}
		for _, r := range x.Results {
			out.Checks = append(out.Checks, checkJSON{Prerequisite: string(r.Prerequisite), Met: r.Met, Reason: r.Reason})
		}
		return out, nil
	}))
	handle(methodAccept, handleAccept)
	handle(methodDecline, withItem(func(ctx context.Context, c *app.Console, item domain.Ref) (any, error) {
		return struct{}{}, c.Decline(ctx, item)
	}))
	handle(methodSnooze, handleSnooze)
	handle(methodUnhide, withItem(func(ctx context.Context, c *app.Console, item domain.Ref) (any, error) {
		was, err := c.Unhide(ctx, item)
		return unhideResult{WasHidden: was}, err
	}))
	handle(methodAnts, handleAnts)
	handle(methodAnt, withItem(handleAnt))
	handle(methodFollow, handleFollow)
	handle(methodStop, withItem(antAction((*app.Console).Stop)))
	handle(methodAbandon, withItem(antAction((*app.Console).Abandon)))
	handle(methodRetry, withItem(antAction((*app.Console).Retry)))
	handle(methodContinue, handleContinue)
	handle(methodCleanup, handleCleanup)
	handle(methodQuestions, handleQuestions)
	handle(methodAnswer, handleAnswer)
	handle(methodExtensions, handleExtensions)
	handle(methodDecideExtension, handleDecideExtension)
	handle(methodFollowUps, handleFollowUps)
	handle(methodAcceptFollowUp, withItem(antAction((*app.Console).AcceptFollowUp)))
	handle(methodDeclineFollowUp, withItem(antAction((*app.Console).DeclineFollowUp)))
}

// codedError gives the errors the CLI treats apart their codes.
func codedError(err error) error {
	var confirm *app.NeedsConfirmationError
	if errors.As(err, &confirm) {
		return &control.Error{Code: codeNeedsConfirmation, Message: err.Error()}
	}
	return err
}

// resolve reads the item a method is about.
func resolve(c *app.Console, text string) (domain.Ref, error) {
	ref, err := c.Resolve(text)
	if err != nil {
		return ref, &control.Error{Code: control.CodeInvalidParams, Message: err.Error()}
	}
	return ref, nil
}

// withItem is a handler whose params are itemParams.
func withItem(h func(ctx context.Context, c *app.Console, item domain.Ref) (any, error)) consoleHandler {
	return func(ctx context.Context, c *app.Console, raw json.RawMessage, _ control.Stream) (any, error) {
		var p itemParams
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		item, err := resolve(c, p.Item)
		if err != nil {
			return nil, err
		}
		return h(ctx, c, item)
	}
}

// antAction is a handler for a queen action that returns the ant after
// it.
func antAction(action func(*app.Console, context.Context, domain.Ref) (app.AntView, error)) func(context.Context, *app.Console, domain.Ref) (any, error) {
	return func(ctx context.Context, c *app.Console, item domain.Ref) (any, error) {
		v, err := action(c, ctx, item)
		if err != nil {
			return nil, err
		}
		return antResult{Ant: antOf(v)}, nil
	}
}

func handleProposals(ctx context.Context, c *app.Console, _ json.RawMessage, _ control.Stream) (any, error) {
	list, err := c.Proposals(ctx)
	if err != nil {
		return nil, err
	}
	out := proposalsResult{Now: list.Now, Proposals: []proposalJSON{}, Warnings: list.Warnings}
	for _, p := range list.Proposals {
		out.Proposals = append(out.Proposals, proposalJSON{
			Item: itemOf(p.Item), Title: p.Title, Repository: p.Repository.String(), Type: string(p.Type),
			Priority: p.Priority, CreatedAt: p.CreatedAt, Runner: string(p.Runner),
		})
	}
	return out, nil
}

func handleAccept(ctx context.Context, c *app.Console, raw json.RawMessage, _ control.Stream) (any, error) {
	var p acceptParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	item, err := resolve(c, p.Item)
	if err != nil {
		return nil, err
	}
	v, err := c.Accept(ctx, app.AcceptRequest{Item: item, Runner: domain.Runner(p.Runner), BudgetMinutes: p.BudgetMinutes, BaseBranch: p.BaseBranch})
	if err != nil {
		return nil, err
	}
	return antResult{Ant: antOf(v)}, nil
}

func handleSnooze(ctx context.Context, c *app.Console, raw json.RawMessage, _ control.Stream) (any, error) {
	var p snoozeParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	item, err := resolve(c, p.Item)
	if err != nil {
		return nil, err
	}
	until, err := c.Snooze(ctx, item, time.Duration(p.Seconds)*time.Second)
	if err != nil {
		return nil, err
	}
	return snoozeResult{Until: until}, nil
}

func handleAnts(ctx context.Context, c *app.Console, raw json.RawMessage, _ control.Stream) (any, error) {
	var p antsParams
	if len(raw) > 0 {
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
	}
	ants, err := c.Ants(ctx, p.All)
	if err != nil {
		return nil, err
	}
	out := antsResult{Ants: []antJSON{}}
	for _, v := range ants {
		out.Ants = append(out.Ants, antOf(v))
	}
	return out, nil
}

func handleAnt(ctx context.Context, c *app.Console, item domain.Ref) (any, error) {
	d, err := c.Ant(ctx, item)
	if err != nil {
		return nil, err
	}
	out := antDetailResult{Ant: antOf(d.AntView), History: []transitionJSON{}, Runs: []runJSON{}}
	for _, t := range d.History {
		out.History = append(out.History, transitionJSON{From: string(t.From), To: string(t.To), At: t.At, Reason: t.Reason})
	}
	for _, r := range d.Runs {
		out.Runs = append(out.Runs, runJSON{
			ID: int64(r.ID), Kind: string(r.Kind), StartedAt: r.StartedAt, EndedAt: r.EndedAt,
			Outcome: string(r.Outcome), Reason: r.Reason,
		})
	}
	return out, nil
}

// handleFollow streams the activity of the item's active run until the
// run ends, the client detaches or the daemon stops. Detaching only ends
// the follow (spec queen-console, "Follow an ant").
func handleFollow(ctx context.Context, c *app.Console, raw json.RawMessage, stream control.Stream) (any, error) {
	var p itemParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	item, err := resolve(c, p.Item)
	if err != nil {
		return nil, err
	}
	f, _, err := c.Follow(ctx, item)
	if err != nil {
		return nil, err
	}
	defer f.Detach()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case ev, open := <-f.Events():
			switch {
			case !open:
				return followResult{}, nil
			case ev.Ended:
				return followResult{Ended: true, Outcome: string(ev.Outcome), Reason: ev.Reason, Dropped: ev.Dropped}, nil
			}
			if err := stream(followItem{Line: ev.Line, Dropped: ev.Dropped}); err != nil {
				return nil, err
			}
		}
	}
}

func handleContinue(ctx context.Context, c *app.Console, raw json.RawMessage, _ control.Stream) (any, error) {
	var p continueParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	item, err := resolve(c, p.Item)
	if err != nil {
		return nil, err
	}
	v, err := c.Continue(ctx, item, time.Duration(p.BudgetMinutes)*time.Minute)
	if err != nil {
		return nil, err
	}
	return antResult{Ant: antOf(v)}, nil
}

func handleCleanup(ctx context.Context, c *app.Console, raw json.RawMessage, _ control.Stream) (any, error) {
	var p cleanupParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	item, err := resolve(c, p.Item)
	if err != nil {
		return nil, err
	}
	plan, err := c.Cleanup(ctx, item, p.Confirm)
	if err != nil {
		return nil, err
	}
	return cleanupResult{RemoveWorktree: plan.RemoveWorktree, DeleteLocalBranch: plan.DeleteLocalBranch, DeleteRemoteBranch: plan.DeleteRemoteBranch}, nil
}

func handleQuestions(ctx context.Context, c *app.Console, _ json.RawMessage, _ control.Stream) (any, error) {
	qs, err := c.Questions(ctx)
	if err != nil {
		return nil, err
	}
	out := questionsResult{Now: c.Now(), Questions: []questionJSON{}}
	for _, q := range qs {
		out.Questions = append(out.Questions, questionJSON{Item: itemOf(q.Item), On: itemOf(q.On), Text: q.Text, AskedAt: q.AskedAt, Stale: q.Stale})
	}
	return out, nil
}

func handleAnswer(ctx context.Context, c *app.Console, raw json.RawMessage, _ control.Stream) (any, error) {
	var p answerParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	item, err := resolve(c, p.Item)
	if err != nil {
		return nil, err
	}
	posted, err := c.Answer(ctx, item, p.Text)
	if err != nil {
		return nil, err
	}
	return answerResult{URL: posted.URL}, nil
}

func handleExtensions(ctx context.Context, c *app.Console, _ json.RawMessage, _ control.Stream) (any, error) {
	list, err := c.Extensions(ctx)
	if err != nil {
		return nil, err
	}
	out := extensionsResult{Extensions: []extensionJSON{}}
	for _, x := range list {
		out.Extensions = append(out.Extensions, extensionJSON{
			Item: itemOf(x.Item), Minutes: x.Request.Minutes, Reason: x.Request.Reason, At: x.Request.At, Remaining: seconds(x.Remaining),
		})
	}
	return out, nil
}

func handleDecideExtension(ctx context.Context, c *app.Console, raw json.RawMessage, _ control.Stream) (any, error) {
	var p decideExtensionParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	item, err := resolve(c, p.Item)
	if err != nil {
		return nil, err
	}
	req, err := c.DecideExtension(ctx, item, app.ExtensionDecision(p.Decision), p.Minutes)
	if err != nil {
		return nil, err
	}
	return decisionResult{Status: string(req.Status), Granted: req.Granted, Message: req.Message}, nil
}

func handleFollowUps(ctx context.Context, c *app.Console, _ json.RawMessage, _ control.Stream) (any, error) {
	list, err := c.FollowUps(ctx)
	if err != nil {
		return nil, err
	}
	out := followUpsResult{FollowUps: []followUpJSON{}}
	for _, f := range list {
		v := followUpJSON{Item: itemOf(f.Item), Changes: []string{}}
		if f.PR != nil {
			pr := itemOf(*f.PR)
			v.PR = &pr
		}
		for _, ch := range f.Changes {
			v.Changes = append(v.Changes, describeChange(ch))
		}
		out.FollowUps = append(out.FollowUps, v)
	}
	return out, nil
}

// describeChange is one line about a stored change for the console: who
// did what, and the first line of its text.
func describeChange(e domain.ChangeEvent) string {
	what := string(e.Kind)
	if e.Actor != "" {
		what = e.Actor + ": " + what
	}
	if line := firstLine(e.Body); line != "" {
		what += ": " + line
	}
	return what
}

// firstLine is the first non-empty line of s, cut to 80 runes.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			r := []rune(l)
			if len(r) > 80 {
				return string(r[:79]) + "…"
			}
			return l
		}
	}
	return ""
}
