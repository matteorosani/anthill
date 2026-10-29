package testkit_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

func TestAgentRunnerScripts(t *testing.T) {
	ctx := context.Background()
	tokens := int64(1000)
	r := testkit.NewAgentRunner(ports.Capabilities{Resume: true, LiveInput: true})
	r.Script(
		testkit.RunScript{SessionID: "s-1", Usage: ports.Usage{Tokens: &tokens}, Exit: &ports.Exit{Code: 0}},
		testkit.RunScript{Exit: &ports.Exit{Code: 2, Crashed: true}},
	)
	run, err := r.Start(ctx, ports.RunSpec{Worktree: "/w", Brief: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if exit := run.Wait(); exit != (ports.Exit{Code: 0}) || run.SessionID() != "s-1" || *run.Usage().Tokens != 1000 {
		t.Errorf("first run: exit %+v, session %s", exit, run.SessionID())
	}
	resumed, err := r.Resume(ctx, "s-1", ports.RunSpec{Brief: "60 seconds"})
	if err != nil {
		t.Fatal(err)
	}
	if exit := resumed.Wait(); !exit.Crashed || resumed.SessionID() != "s-1" {
		t.Errorf("resumed run: exit %+v, session %s", exit, resumed.SessionID())
	}
	runs := r.Runs()
	if len(runs) != 2 || runs[0].Spec.Brief != "do it" || runs[1].Resumed != "s-1" {
		t.Errorf("Runs() = %+v", runs)
	}
	if err := runs[0].Send(ctx, "late"); err == nil {
		t.Error("Send to an ended run succeeded")
	}
}

func TestAgentRunnerActiveRun(t *testing.T) {
	ctx := context.Background()
	r := testkit.NewAgentRunner(ports.Capabilities{LiveInput: true})
	run, _ := r.Start(ctx, ports.RunSpec{})
	if run.SessionID() != "session-1" {
		t.Errorf("SessionID() = %s", run.SessionID())
	}
	if err := run.Send(ctx, "80% of the budget used"); err != nil {
		t.Fatal(err)
	}
	fake := r.Runs()[0]
	var wg sync.WaitGroup
	var exit ports.Exit
	wg.Go(func() { exit = run.Wait() })
	if err := run.Stop(time.Second); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if exit != testkit.StoppedExit || !fake.Stopped() || len(fake.Inputs()) != 1 {
		t.Errorf("exit %+v, stopped %v, inputs %v", exit, fake.Stopped(), fake.Inputs())
	}
	// Ending an ended run changes nothing.
	fake.End(ports.Exit{Code: 0})
	_ = run.Stop(0)
	if run.Wait() != testkit.StoppedExit {
		t.Error("exit changed after the run ended")
	}

	second, _ := r.Start(ctx, ports.RunSpec{})
	r.Runs()[1].End(ports.Exit{Code: 1})
	if second.Wait().Code != 1 || r.Runs()[1].Stopped() {
		t.Error("End did not end the run as an exit")
	}
}

func TestAgentRunnerUnsupported(t *testing.T) {
	ctx := context.Background()
	r := testkit.NewAgentRunner(ports.Capabilities{})
	if _, err := r.Resume(ctx, "s-1", ports.RunSpec{}); !errors.Is(err, ports.ErrUnsupported) {
		t.Errorf("Resume() without resume: %v", err)
	}
	run, _ := r.Start(ctx, ports.RunSpec{})
	if err := run.Send(ctx, "hi"); !errors.Is(err, ports.ErrUnsupported) {
		t.Errorf("Send() without live input: %v", err)
	}
	r.Runs()[0].SetUsage(ports.Usage{})
	if u := run.Usage(); u.Tokens != nil || u.CostUSD != nil {
		t.Errorf("Usage() = %+v, want unknown", u)
	}
	boom := errors.New("not installed")
	r.StartErr = boom
	if _, err := r.Start(ctx, ports.RunSpec{}); !errors.Is(err, boom) {
		t.Errorf("Start() = %v", err)
	}
}
