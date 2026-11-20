package app_test

import (
	"context"
	"testing"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/ports"
)

type recordedEnds struct{ runs []ports.RunRecord }

func (r *recordedEnds) EndRun(_ context.Context, run ports.RunRecord, _ ports.Usage, _ string) error {
	r.runs = append(r.runs, run)
	return nil
}

func TestActivityHubFollowsOneAnt(t *testing.T) {
	hub := app.NewActivityHub()
	ends := &recordedEnds{}
	runEnds := hub.RunEnds(ends)
	f := hub.Follow(1)
	other := hub.Follow(2)

	hub.Activity(1, 10, "reading main.go")
	hub.Activity(2, 20, "elsewhere")
	if err := runEnds.EndRun(context.Background(), ports.RunRecord{ID: 10, Ant: 1, Outcome: ports.OutcomeComplete, Reason: "finished"}, ports.Usage{}, ""); err != nil {
		t.Fatal(err)
	}
	var got []app.FollowEvent
	for ev := range f.Events() {
		got = append(got, ev)
	}
	if len(got) != 2 || got[0].Line != "reading main.go" || !got[1].Ended || got[1].Outcome != ports.OutcomeComplete || got[1].Reason != "finished" {
		t.Errorf("events = %+v", got)
	}
	if len(ends.runs) != 1 {
		t.Errorf("the wrapped RunEnds got %d runs", len(ends.runs))
	}
	// The other ant's follower still follows.
	if ev := <-other.Events(); ev.Line != "elsewhere" {
		t.Errorf("other = %+v", ev)
	}
	other.Detach()
	other.Detach()
	if _, open := <-other.Events(); open {
		t.Error("a detached follower still receives")
	}
	hub.Activity(2, 20, "after detach") // no follower: dropped, never blocks
}

// A follower that does not read never holds the run back: lines are
// dropped and counted, and the end still arrives.
func TestActivityHubSlowFollower(t *testing.T) {
	hub := app.NewActivityHub()
	f := hub.Follow(1)
	for range 1000 {
		hub.Activity(1, 10, "line")
	}
	if err := hub.RunEnds(&recordedEnds{}).EndRun(context.Background(), ports.RunRecord{Ant: 1, Outcome: ports.OutcomeStopped}, ports.Usage{}, ""); err != nil {
		t.Fatal(err)
	}
	lines, dropped := 0, 0
	var last app.FollowEvent
	for ev := range f.Events() {
		dropped += ev.Dropped
		if !ev.Ended {
			lines++
		}
		last = ev
	}
	if !last.Ended || lines+dropped != 1000 {
		t.Errorf("lines %d + dropped %d, last = %+v", lines, dropped, last)
	}
}
