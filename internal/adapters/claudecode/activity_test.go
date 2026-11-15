package claudecode_test

import (
	"context"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/testkit"
)

// Task 14.10: each run writes its activity, its output lines, to its
// own RunSpec.Activity, not to one log shared by all runs (design D21).
func TestActivityIsPerRun(t *testing.T) {
	recorded, err := fixtures.ReadFile("testdata/failed/turn-1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var want strings.Builder
	for _, l := range strings.Split(string(recorded), "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			want.WriteString(l + "\n")
		}
	}
	var buffers [2]testkit.SyncBuffer
	done := make(chan struct{}, len(buffers))
	for i := range buffers {
		f := newFake(t, "failed")
		spec := f.spec(t)
		spec.Activity = &buffers[i]
		run, err := f.runner.Start(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			run.Wait()
			done <- struct{}{}
		}()
	}
	for range buffers {
		<-done
	}
	for i := range buffers {
		if got := buffers[i].String(); got != want.String() {
			t.Errorf("run %d activity:\n%s\nwant once each line of the run's own output:\n%s", i+1, got, want.String())
		}
	}
}
