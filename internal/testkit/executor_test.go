package testkit_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

func collect(t *testing.T, events <-chan ports.ExecEvent) []ports.ExecEvent {
	t.Helper()
	var all []ports.ExecEvent
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return all
			}
			all = append(all, ev)
		case <-timeout:
			t.Fatalf("the events did not end: %+v", all)
		}
	}
}

func TestExecutorRecordsAndDrivesEvents(t *testing.T) {
	x := testkit.NewExecutor(map[domain.Runner]ports.Capabilities{domain.RunnerClaudeCode: {LiveInput: true, Resume: true}})
	deadline := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	ex, err := x.Start(context.Background(), ports.ExecSpec{Runner: domain.RunnerClaudeCode, Session: "ses-1", Deadline: deadline})
	if err != nil {
		t.Fatal(err)
	}
	f := x.Execution(ex.ID)
	if f == nil || f.Spec.Session != "ses-1" || !f.Deadline().Equal(deadline) || !ex.Capabilities.LiveInput {
		t.Fatalf("execution = %+v, %+v", f, ex)
	}
	if err := x.Send(context.Background(), ex.ID, "12 minutes left"); err != nil {
		t.Fatal(err)
	}
	if err := x.SetDeadline(ex.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	f.Emit(ports.ExecEvent{Kind: ports.ExecActivity, Line: "working"})
	if err := x.Stop(ex.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	var kinds []ports.ExecEventKind
	for _, ev := range collect(t, ex.Events) {
		kinds = append(kinds, ev.Kind)
	}
	if want := []ports.ExecEventKind{ports.ExecStarted, ports.ExecActivity, ports.ExecExited}; !slices.Equal(kinds, want) {
		t.Errorf("events = %v, want %v", kinds, want)
	}
	if !slices.Equal(f.Inputs(), []string{"12 minutes left"}) || !f.Deadline().IsZero() || len(f.Deadlines()) != 1 || !slices.Equal(f.Stops(), []time.Duration{time.Second}) {
		t.Errorf("recorded inputs %q, deadline %v, deadlines %v, stops %v", f.Inputs(), f.Deadline(), f.Deadlines(), f.Stops())
	}
	if err := x.Send(context.Background(), ex.ID, "late"); !errors.Is(err, ports.ErrUnknownExecution) {
		t.Errorf("send after the end: %v", err)
	}
}

func TestExecutorRefusals(t *testing.T) {
	x := testkit.NewExecutor(nil)
	if _, err := x.Start(context.Background(), ports.ExecSpec{Runner: domain.RunnerCodex, Session: "s"}); !errors.Is(err, ports.ErrUnsupported) {
		t.Errorf("resume without the capability: %v", err)
	}
	ex, err := x.Start(context.Background(), ports.ExecSpec{Runner: domain.RunnerCodex})
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Send(context.Background(), ex.ID, "hi"); !errors.Is(err, ports.ErrUnsupported) {
		t.Errorf("send without live input: %v", err)
	}
	x.Execution(ex.ID).End(ports.ExecEndedAtDeadline, testkit.StoppedExit)
	if evs := collect(t, ex.Events); evs[len(evs)-1].Kind != ports.ExecEndedAtDeadline {
		t.Errorf("last event = %+v", evs[len(evs)-1])
	}

	boom := errors.New("boom")
	x = testkit.NewExecutor(map[domain.Runner]ports.Capabilities{domain.RunnerCodex: {LiveInput: true}})
	x.SendErr = boom
	ex, _ = x.Start(context.Background(), ports.ExecSpec{Runner: domain.RunnerCodex})
	if err := x.Send(context.Background(), ex.ID, "hi"); !errors.Is(err, boom) {
		t.Errorf("send = %v, want SendErr", err)
	}
	x.StartErr = boom
	if _, err := x.Start(context.Background(), ports.ExecSpec{Runner: domain.RunnerCodex}); !errors.Is(err, boom) {
		t.Errorf("start = %v, want StartErr", err)
	}
}
