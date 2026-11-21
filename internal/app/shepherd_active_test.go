package app_test

import (
	"sync"
	"testing"
	"testing/synctest"

	"github.com/matteorosani/anthill/internal/app"
)

// The PR of a round in progress is listed until the round ends.
func TestShepherdActiveRounds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		a := e.instance(instanceA, "mrosani", nil)
		pr := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)
		if got := a.shepherd.ActiveRounds(); len(got) != 0 {
			t.Fatalf("active rounds before any = %v", got)
		}
		release := make(chan struct{})
		var once sync.Once
		a.rounds.result = func(app.RoundStart) (app.RoundResult, error) {
			once.Do(func() { <-release })
			return app.RoundResult{}, nil
		}
		a.trigger(pr)
		if got := a.shepherd.ActiveRounds(); len(got) != 1 || got[0] != pr {
			t.Errorf("active rounds = %v, want %v", got, pr)
		}
		close(release)
		e.settle()
		if got := a.shepherd.ActiveRounds(); len(got) != 0 {
			t.Errorf("active rounds after the round = %v", got)
		}
	})
}
