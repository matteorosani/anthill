package app

import (
	"context"
	"sync"

	"github.com/matteorosani/anthill/internal/ports"
)

// followBuffer is how many activity lines a follower can be behind before
// lines are dropped for it.
const followBuffer = 256

// ActivityHub passes the activity of active runs to the queen consoles
// that follow them (spec queen-console, "Monitoring ants"). It is the
// supervisor's RunActivity, and wraps its RunEnds to see each run end.
// Nothing a follower does reaches the run: a slow follower loses lines
// instead of holding the run back, and detaching only drops the
// follower ("Follow an ant").
type ActivityHub struct {
	mu        sync.Mutex
	followers map[ports.AntID]map[*Follower]struct{}
}

var _ RunActivity = (*ActivityHub)(nil)

// NewActivityHub returns a hub without followers.
func NewActivityHub() *ActivityHub {
	return &ActivityHub{followers: map[ports.AntID]map[*Follower]struct{}{}}
}

// FollowEvent is one event a follower receives: an activity line, or the
// end of the run, which is the last event.
type FollowEvent struct {
	Line string
	// Dropped counts the lines dropped just before this one because the
	// follower was behind.
	Dropped int
	// Ended is set on the last event, with the run's outcome and reason.
	Ended   bool
	Outcome ports.RunOutcome
	Reason  string
}

// Follower receives the activity of one ant's runs until it detaches or
// the run it follows ends.
type Follower struct {
	hub    *ActivityHub
	ant    ports.AntID
	events chan FollowEvent
	// dropped counts lines not delivered since the last delivered event;
	// closed is set once the follower is detached. Both are guarded by
	// the hub's mutex.
	dropped int
	closed  bool
}

// Events delivers the follower's events. The channel is closed after the
// end of the run and on Detach.
func (f *Follower) Events() <-chan FollowEvent {
	return f.events
}

// Detach stops following. It is safe to call more than once.
func (f *Follower) Detach() {
	f.hub.mu.Lock()
	defer f.hub.mu.Unlock()
	f.hub.remove(f)
}

// Follow starts following the activity of the ant's runs.
func (h *ActivityHub) Follow(ant ports.AntID) *Follower {
	h.mu.Lock()
	defer h.mu.Unlock()
	f := &Follower{hub: h, ant: ant, events: make(chan FollowEvent, followBuffer)}
	if h.followers[ant] == nil {
		h.followers[ant] = map[*Follower]struct{}{}
	}
	h.followers[ant][f] = struct{}{}
	return f
}

// Activity passes one line of a run's activity to the ant's followers. It
// never blocks.
func (h *ActivityHub) Activity(ant ports.AntID, _ ports.RunID, line string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for f := range h.followers[ant] {
		select {
		case f.events <- FollowEvent{Line: line, Dropped: f.dropped}:
			f.dropped = 0
		default:
			f.dropped++
		}
	}
}

// ended tells the ant's followers that its run ended, and detaches them.
func (h *ActivityHub) ended(r ports.RunRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for f := range h.followers[r.Ant] {
		ev := FollowEvent{Ended: true, Outcome: r.Outcome, Reason: r.Reason, Dropped: f.dropped}
		select {
		case f.events <- ev:
		default:
			// The follower is full: make room for the end, which matters
			// more than the oldest line.
			select {
			case <-f.events:
				ev.Dropped++
			default:
			}
			f.events <- ev
		}
		h.remove(f)
	}
}

// remove detaches f; the caller holds the mutex.
func (h *ActivityHub) remove(f *Follower) {
	if f.closed {
		return
	}
	f.closed = true
	delete(h.followers[f.ant], f)
	if len(h.followers[f.ant]) == 0 {
		delete(h.followers, f.ant)
	}
	close(f.events)
}

// RunEnds returns next wrapped so that the end of each run, once next
// recorded it, is also passed to the followers of its ant. The daemon
// gives the wrapped one to the supervisor.
func (h *ActivityHub) RunEnds(next RunEnds) RunEnds {
	return hubRunEnds{hub: h, next: next}
}

type hubRunEnds struct {
	hub  *ActivityHub
	next RunEnds
}

func (e hubRunEnds) EndRun(ctx context.Context, r ports.RunRecord, reported ports.Usage, resumedSession string) error {
	err := e.next.EndRun(ctx, r, reported, resumedSession)
	e.hub.ended(r)
	return err
}
