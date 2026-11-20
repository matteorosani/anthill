package app_test

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// classify runs one classifier call on the fake executor, which end
// drives once the call has started.
func classify(t *testing.T, end func(x *testkit.FakeExecution)) (domain.Relevance, *testkit.FakeExecution, error) {
	t.Helper()
	clock := testkit.NewClock(t0)
	exec := testkit.NewExecutor(map[domain.Runner]ports.Capabilities{domain.RunnerClaudeCode: {}})
	c := app.NewClassifier(exec, clock, []string{"PATH=/usr/bin", "GH_TOKEN=secret"})
	type result struct {
		rel domain.Relevance
		err error
	}
	done := make(chan result, 1)
	go func() {
		rel, err := c.Classify(context.Background(), ports.AntRecord{Runner: string(domain.RunnerClaudeCode)},
			domain.WorkItem{Ref: ghRef(12), Title: "Fix the login timeout"},
			domain.ChangeEvent{Ref: ghRef(12), Kind: domain.ChangeCommentAdded, Actor: "lbianchi", Body: "thanks, looks good"})
		done <- result{rel, err}
	}()
	// Inside the synctest bubble, Wait returns once the call is blocked
	// reading the execution's events.
	synctest.Wait()
	x := exec.Executions()[0]
	end(x)
	r := <-done
	return r.rel, x, r.err
}

// Spec change-reaction, "Thank-you comment": the classifier's last
// message is its verdict.
func TestClassifierThankYouComment(t *testing.T) {
	synctest.Test(t, testClassifierThankYouComment)
}

func testClassifierThankYouComment(t *testing.T) {
	rel, x, err := classify(t, func(x *testkit.FakeExecution) {
		x.Emit(ports.ExecEvent{Kind: ports.ExecLastMessage, Message: "Thinking"})
		x.Emit(ports.ExecEvent{Kind: ports.ExecLastMessage, Message: "informational"})
		x.End(ports.ExecExited, ports.Exit{})
	})
	if err != nil || rel != domain.Informational {
		t.Fatalf("Classify = %q, %v", rel, err)
	}
	spec := x.Spec
	if spec.Session != "" || spec.Run.MCPCommand != nil || spec.Run.Worktree != "" || !spec.Deadline.Equal(t0.Add(app.ClassifierTimeout)) {
		t.Errorf("spec = %+v", spec)
	}
	for _, kv := range spec.Run.Env {
		if kv == "GH_TOKEN=secret" {
			t.Error("the classifier got the provider token")
		}
	}
}

// A classifier call that fails gives an error, so the change reactor
// counts the change as actionable (design D8).
func TestClassifierFailure(t *testing.T) {
	tests := map[string]func(x *testkit.FakeExecution){
		"crash": func(x *testkit.FakeExecution) { x.End(ports.ExecExited, ports.Exit{Code: 1, Crashed: true}) },
		"deadline": func(x *testkit.FakeExecution) {
			x.End(ports.ExecEndedAtDeadline, ports.Exit{})
		},
		"no verdict": func(x *testkit.FakeExecution) {
			x.Emit(ports.ExecEvent{Kind: ports.ExecLastMessage, Message: "I am not sure."})
			x.End(ports.ExecExited, ports.Exit{})
		},
	}
	for name, end := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				if rel, _, err := classify(t, end); err == nil {
					t.Errorf("Classify = %q, want an error", rel)
				}
			})
		})
	}
}
