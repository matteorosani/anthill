package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// itemView is the item as get_task returns it.
type itemView struct {
	Ref                string   `json:"ref"`
	URL                string   `json:"url"`
	Title              string   `json:"title"`
	Body               string   `json:"body"`
	AcceptanceCriteria string   `json:"acceptance_criteria,omitempty"`
	Labels             []string `json:"labels"`
}

// commentView is one human comment as get_task returns it.
type commentView struct {
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// budgetView is the budget as get_task returns it.
type budgetView struct {
	TotalMinutes     int `json:"total_minutes"`
	RemainingMinutes int `json:"remaining_minutes"`
}

// prView is the ant PR as get_task returns it.
type prView struct {
	URL   string `json:"url"`
	Draft bool   `json:"draft"`
}

// taskResult is the result of get_task.
type taskResult struct {
	Item     itemView      `json:"item"`
	Comments []commentView `json:"comments"`
	Branch   string        `json:"branch"`
	Base     string        `json:"base"`
	Budget   budgetView    `json:"budget"`
	PR       *prView       `json:"pr,omitempty"`
}

// getTask reads the item and the PR fresh from the providers (spec
// ant-tools, "Task and time tools"). Anthill's own comments are left out,
// as in the brief (spec ant-execution, "Task brief").
func (t *AntTools) getTask(ctx context.Context, c *toolCall) (any, error) {
	rec := c.ant
	tracker, err := t.tracker(ctx, rec.Item)
	if err != nil {
		return nil, err
	}
	item, _, err := tracker.Item(ctx, rec.Item)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rec.Item, err)
	}
	out := taskResult{
		Item: itemView{
			Ref:                rec.Item.String(),
			URL:                item.URL,
			Title:              item.Title,
			Body:               item.Body,
			AcceptanceCriteria: item.AcceptanceCriteria,
			Labels:             append([]string{}, item.Labels...),
		},
		Comments: []commentView{},
		Branch:   rec.Branch,
		Base:     rec.BaseBranch,
	}
	for _, cm := range item.Comments {
		if cm.Origin() == domain.OriginHuman {
			out.Comments = append(out.Comments, commentView{Author: cm.Author, Body: cm.Body, CreatedAt: cm.CreatedAt})
		}
	}
	now := t.clock.Now()
	budget := rec.Budget
	out.Budget = budgetView{TotalMinutes: minutes(budget.Total()), RemainingMinutes: minutes(budget.Remaining(now))}
	if rec.PR != nil {
		host, err := t.codeHost(ctx, *rec.PR)
		if err != nil {
			return nil, err
		}
		pr, _, err := host.PullRequest(ctx, *rec.PR)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", rec.PR, err)
		}
		out.PR = &prView{URL: pr.URL, Draft: pr.Draft}
	}
	return out, nil
}

// extensionView is an open extension request as time_remaining returns
// it.
type extensionView struct {
	Minutes     int       `json:"minutes"`
	Reason      string    `json:"reason"`
	RequestedAt time.Time `json:"requested_at"`
}

// timeResult is the result of time_remaining.
type timeResult struct {
	RemainingMinutes int    `json:"remaining_minutes"`
	TotalMinutes     int    `json:"total_minutes"`
	UsedMinutes      int    `json:"used_minutes"`
	Warning          string `json:"warning,omitempty"`
	// OpenExtension is the request still waiting for the queen.
	OpenExtension *extensionView `json:"open_extension,omitempty"`
}

// timeRemaining answers from the ant's budget (spec ant-timebox,
// "Remaining time is queryable"). The warning is included once the
// warning threshold is reached; delivering it on time is the
// timekeeper's job.
func (t *AntTools) timeRemaining(_ context.Context, c *toolCall) (any, error) {
	now := t.clock.Now()
	b := c.ant.Budget
	out := timeResult{
		RemainingMinutes: minutes(b.Remaining(now)),
		TotalMinutes:     minutes(b.Total()),
		UsedMinutes:      minutes(b.UsedAt(now)),
	}
	if b.Warned || b.WarningDue(now) {
		out.Warning = b.WarningMessage(now)
	}
	if b.Open != nil {
		out.OpenExtension = &extensionView{Minutes: b.Open.Minutes, Reason: b.Open.Reason, RequestedAt: b.Open.At}
	}
	return out, nil
}

// extensionArgs are the arguments of request_extension.
type extensionArgs struct {
	Minutes int    `json:"minutes"`
	Reason  string `json:"reason"`
}

// extensionResult is the result of request_extension.
type extensionResult struct {
	Status           domain.ExtensionStatus `json:"status"`
	GrantedMinutes   int                    `json:"granted_minutes"`
	RemainingMinutes int                    `json:"remaining_minutes"`
	Message          string                 `json:"message"`
}

// MessageExtensionPending is what request_extension says when the wait
// ends without a decision.
const MessageExtensionPending = "no decision yet: keep working within your remaining time; the decision will be delivered to you when the queen makes it"

// requestExtension applies the ant-timebox rules to a request. A request
// over the ceiling is denied and one within the auto-approve allowance
// approved at once. Otherwise the ant is awaiting-extension while the
// call waits up to the extension wait for DecideExtension, and running
// again afterwards, whatever ends the wait (spec ant-tools, "Extension
// tool"). During the wait the ant's lock is free, so other calls are
// checked and refused at once.
func (t *AntTools) requestExtension(ctx context.Context, c *toolCall) (any, error) {
	var args extensionArgs
	if err := decodeArgs(c.args, &args); err != nil {
		return nil, err
	}
	rec := c.ant
	ant, err := restoreAnt(rec)
	if err != nil {
		return nil, err
	}
	now := t.clock.Now()
	wasOpen := ant.Budget.Open != nil
	req, err := ant.Budget.RequestExtension(args.Minutes, args.Reason, now)
	if err != nil {
		return nil, domain.RefuseTool("%s", err.Error())
	}
	if req.Status != domain.ExtensionPending {
		rec.Budget = ant.Budget
		if err := t.store.SaveAnt(ctx, rec); err != nil {
			return nil, err
		}
		if _, err := t.store.SaveExtension(ctx, ports.ExtensionRecord{Ant: rec.ID, Request: req, DecidedAt: now}); err != nil {
			return nil, err
		}
		return extensionAnswer(req, ant.Budget, now), nil
	}
	if !wasOpen {
		if _, err := t.store.SaveExtension(ctx, ports.ExtensionRecord{Ant: rec.ID, Request: req}); err != nil {
			return nil, err
		}
	}
	if ant.State() != domain.AntRunning {
		// A starting ant cannot be awaiting-extension; the request stays
		// open without the wait.
		rec.Budget = ant.Budget
		if err := t.store.SaveAnt(ctx, rec); err != nil {
			return nil, err
		}
		return pendingAnswer(ant.Budget, now), nil
	}
	if err := ant.Transition(domain.AntAwaitingExtension, now, fmt.Sprintf("requested %d more minutes", args.Minutes)); err != nil {
		return nil, err
	}
	if err := t.store.SaveAnt(ctx, withAnt(rec, ant)); err != nil {
		return nil, err
	}

	decided, reason, waitErr := t.waitForDecision(ctx, c, rec.ID)

	// Back to running, whatever ended the wait. The lock is held again.
	end := context.WithoutCancel(ctx)
	rec, err = t.store.Ant(end, rec.ID)
	if err != nil {
		return nil, err
	}
	ant, err = restoreAnt(rec)
	if err != nil {
		return nil, err
	}
	now = t.clock.Now()
	if ant.State() == domain.AntAwaitingExtension {
		if err := ant.Transition(domain.AntRunning, now, reason); err != nil {
			return nil, err
		}
		if err := t.store.SaveAnt(end, withAnt(rec, ant)); err != nil {
			return nil, err
		}
	}
	if waitErr != nil {
		return nil, waitErr
	}
	if decided != nil {
		return extensionAnswer(*decided, ant.Budget, now), nil
	}
	return pendingAnswer(ant.Budget, now), nil
}

// waitForDecision gives back the ant's lock and waits for the queen's
// decision, the end of the extension wait, or the end of the call. It
// takes the lock again before it returns the decision, if any, and the
// reason of the transition back to running.
func (t *AntTools) waitForDecision(ctx context.Context, c *toolCall, id ports.AntID) (*domain.ExtensionRequest, string, error) {
	ch := make(chan domain.ExtensionRequest, 1)
	t.mu.Lock()
	t.waiters[id] = ch
	t.mu.Unlock()
	wait := domain.DefaultExtensionWait
	if t.opts.ExtensionWait != nil {
		wait = t.opts.ExtensionWait()
	}
	timer := t.clock.After(wait)
	c.unlock()

	var (
		decided *domain.ExtensionRequest
		reason  string
		err     error
	)
	select {
	case req := <-ch:
		decided, reason = &req, "extension decided: "+string(req.Status)
	case <-timer:
		reason = "no extension decision within the wait"
	case <-ctx.Done():
		reason, err = "extension call ended", ctx.Err()
	}

	c.lock()
	t.mu.Lock()
	if t.waiters[id] == ch {
		delete(t.waiters, id)
	}
	t.mu.Unlock()
	// A decision sent between the end of the wait and the removal above
	// is still in the channel.
	if decided == nil {
		select {
		case req := <-ch:
			decided = &req
			if err == nil {
				reason = "extension decided: " + string(req.Status)
			}
		default:
		}
	}
	return decided, reason, err
}

// DecideExtension applies the queen's decision on an ant's open request:
// minutes is the time granted, 0 to deny (spec ant-timebox, "Extension
// requests" and "Late extension decisions"). A request_extension call
// still waiting returns the decision. Otherwise a decision for an ant
// with an active run is delivered to the agent as a message. The effect
// tells the caller what else to do, such as starting a run for a
// timed-out ant.
func (t *AntTools) DecideExtension(ctx context.Context, id ports.AntID, minutes int) (domain.ExtensionRequest, domain.ExtensionEffect, error) {
	m := t.antLock(id)
	m.Lock()
	defer m.Unlock()

	rec, err := t.store.Ant(ctx, id)
	if err != nil {
		return domain.ExtensionRequest{}, "", err
	}
	ant, err := restoreAnt(rec)
	if err != nil {
		return domain.ExtensionRequest{}, "", err
	}
	now := t.clock.Now()
	req, effect, err := ant.DecideExtension(minutes, now)
	if err != nil {
		return req, "", err
	}
	if err := t.store.SaveAnt(ctx, withAnt(rec, ant)); err != nil {
		return req, "", err
	}
	if err := t.closeExtension(ctx, id, req, now); err != nil {
		return req, "", err
	}

	t.mu.Lock()
	ch := t.waiters[id]
	delete(t.waiters, id)
	t.mu.Unlock()
	switch {
	case ch != nil:
		ch <- req
	case effect == domain.EffectBudgetGrown || effect == domain.EffectWrapUp:
		t.notify(id, "extension "+string(req.Status)+": "+req.Message)
	}
	return req, effect, nil
}

// closeExtension stores the decision on the ant's latest open request.
func (t *AntTools) closeExtension(ctx context.Context, id ports.AntID, req domain.ExtensionRequest, now time.Time) error {
	exts, err := t.store.Extensions(ctx, id)
	if err != nil {
		return err
	}
	for i := len(exts) - 1; i >= 0; i-- {
		if exts[i].DecidedAt.IsZero() {
			exts[i].Request = req
			exts[i].DecidedAt = now
			_, err := t.store.SaveExtension(ctx, exts[i])
			return err
		}
	}
	return errors.New("no stored extension request to decide")
}

// extensionAnswer is request_extension's result for a decided request.
// A request over the ceiling is answered as denied (spec ant-timebox,
// "Ceiling reached").
func extensionAnswer(req domain.ExtensionRequest, b domain.Budget, now time.Time) extensionResult {
	status := req.Status
	if status == domain.ExtensionRejected || status == domain.ExtensionClosed {
		status = domain.ExtensionDenied
	}
	return extensionResult{
		Status:           status,
		GrantedMinutes:   req.Granted,
		RemainingMinutes: minutes(b.Remaining(now)),
		Message:          req.Message,
	}
}

func pendingAnswer(b domain.Budget, now time.Time) extensionResult {
	return extensionResult{
		Status:           domain.ExtensionPending,
		RemainingMinutes: minutes(b.Remaining(now)),
		Message:          MessageExtensionPending,
	}
}

// restoreAnt rebuilds the domain ant of a record.
func restoreAnt(rec ports.AntRecord) (*domain.Ant, error) {
	return domain.RestoreAnt(rec.Item, rec.Base, rec.Budget, rec.History)
}

// withAnt returns rec with the ant's budget and history.
func withAnt(rec ports.AntRecord, ant *domain.Ant) ports.AntRecord {
	rec.Budget = ant.Budget
	rec.History = ant.History()
	return rec
}
