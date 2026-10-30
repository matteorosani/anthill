package app

import (
	"context"
	"errors"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// RunChannel reaches the agent of an active run: what its runner
// supports, and the run itself for live input. Ant supervision sets it
// when it issues the run's token.
type RunChannel struct {
	Capabilities ports.Capabilities
	// Run is the active run, or nil before its process is known.
	Run ports.Run
}

// ErrNoActiveRun is returned when a message is for an ant without an
// active run.
var ErrNoActiveRun = errors.New("the ant has no active run")

// Deliver gives a message, such as the budget warning, a late extension
// decision or a ticket update, to the agent of the ant's active run, by
// the first means its runner supports (spec ant-tools, "Messages during
// a run"; design D5):
//   - live input: the message is sent to the run at once. When sending
//     fails, the next means is used;
//   - a hook: the message is queued, and the runner's hook takes it with
//     Notices after the agent's next tool use;
//   - otherwise it is queued and attached to the result of the agent's
//     next ant-tool call.
//
// A queued message is taken once, by the hook or by the next tool result,
// whichever comes first. Deliver returns the means used.
func (t *AntTools) Deliver(ctx context.Context, ant ports.AntID, text string) (domain.DeliveryMeans, error) {
	ch, ok := t.runChannel(ant)
	if !ok {
		return "", ErrNoActiveRun
	}
	caps := ch.Capabilities
	means := domain.ChooseDelivery(caps.LiveInput && ch.Run != nil, caps.Hooks)
	if means == domain.DeliverLiveInput {
		if err := ch.Run.Send(ctx, text); err == nil {
			return means, nil
		}
		means = domain.ChooseDelivery(false, caps.Hooks)
	}
	t.mu.Lock()
	t.notices[ant] = append(t.notices[ant], text)
	t.mu.Unlock()
	return means, nil
}

// runChannel finds the channel of the ant's run that holds a live token.
func (t *AntTools) runChannel(ant ports.AntID) (RunChannel, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var (
		found RunChannel
		run   ports.RunID
		ok    bool
	)
	for _, s := range t.sessions {
		// With several tokens, the latest run's wins.
		if s.Role.IsRun() && s.Ant == ant && (!ok || s.Run > run) {
			found, run, ok = s.Channel, s.Run, true
		}
	}
	return found, ok
}

// Notices takes the messages queued for the token's run, for the
// runner's hook to add as context (design D5). It returns
// ErrUnknownToken for an unknown or revoked token, and nothing for a
// review or fix agent.
func (t *AntTools) Notices(token string) ([]string, error) {
	s, _, ok := t.session(token)
	if !ok {
		return nil, ErrUnknownToken
	}
	if !s.Role.IsRun() {
		return nil, nil
	}
	return t.takeNotices(s.Ant), nil
}
