package app

import (
	"context"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// UsageRecorder records the usage of runs and shepherd rounds as they end
// (spec ant-execution, "Usage is recorded", "Cost reported for a whole
// session", "Usage of a stopped run"). The ant supervisor and the
// shepherd call it in place of RunLog.EndRun and RunLog.EndRound.
//
// The usage it is given is the one the executor reported last: the Usage
// of the ExecExited or ExecEndedAtDeadline event, or of the last
// ExecUsage event when the run ended otherwise. The runner adapters count
// the tokens of each message once, and keep those reported before a stop,
// so a stopped run keeps its tokens. Values the runner did not report stay
// unknown.
type UsageRecorder struct {
	log ports.RunLog
}

// NewUsageRecorder returns a recorder that stores in log.
func NewUsageRecorder(log ports.RunLog) *UsageRecorder {
	return &UsageRecorder{log: log}
}

// EndRun stores the end of run r, as RunLog.EndRun does, with the usage
// the runner reported. The caller sets r's end time, outcome, reason and
// session; r.Usage and r.SessionCostUSD are replaced.
//
// resumedSession is the session the run resumed (ExecSpec.Session), or
// empty for a run that started a new session. When the runner reports
// cost as a running total for its session, the run's cost is that total
// minus the last running total the runner reported for the session: the
// latest one stored by an earlier run of the ant on that session, whichever
// run reported it. A run that reported none is skipped. So when run 1
// reports $0.30, run 2 resumes it and is stopped before reporting a total,
// and run 3 resumes it and reports $0.50, run 2's cost is unknown and run
// 3's is $0.20. The earlier runs are found by the resumed session, not by
// the id the run reports, which some runners change on resume. A resumed
// session with no reported total in the log gives an unknown cost
// (domain.RunCost).
//
// The stored cost is the run's own, so the stored CostIsSessionTotal is
// always false; the total itself is kept in r.SessionCostUSD for the next
// run that resumes the session.
func (u *UsageRecorder) EndRun(ctx context.Context, r ports.RunRecord, reported ports.Usage, resumedSession string) error {
	var last *float64
	if reported.CostIsSessionTotal && reported.CostUSD != nil && resumedSession != "" {
		runs, err := u.log.Runs(ctx, r.Ant)
		if err != nil {
			return fmt.Errorf("end run %d: %w", r.ID, err)
		}
		last = lastSessionTotal(runs, r.ID, resumedSession)
	}
	r.Usage = ports.Usage{
		Tokens:  copyPtr(reported.Tokens),
		CostUSD: domain.RunCost(reported.CostUSD, reported.CostIsSessionTotal, last, resumedSession == ""),
	}
	r.SessionCostUSD = nil
	if reported.CostIsSessionTotal {
		r.SessionCostUSD = copyPtr(reported.CostUSD)
	}
	return u.log.EndRun(ctx, r)
}

// EndRound stores the end of shepherd round r, as RunLog.EndRound does,
// with the usage the runner reported. The caller sets r's end time;
// r.Usage is replaced. Review rounds never resume a session (design D17),
// so a running total for the session is the round's own cost.
func (u *UsageRecorder) EndRound(ctx context.Context, r ports.RoundRecord, reported ports.Usage) error {
	r.Usage = ports.Usage{
		Tokens:  copyPtr(reported.Tokens),
		CostUSD: domain.RunCost(reported.CostUSD, reported.CostIsSessionTotal, nil, true),
	}
	return u.log.EndRound(ctx, r)
}

// lastSessionTotal is the latest running total stored for session by the
// runs before run, or nil when none was.
func lastSessionTotal(runs []ports.RunRecord, run ports.RunID, session string) *float64 {
	var last *float64
	for _, r := range runs {
		if r.ID < run && r.SessionID == session && r.SessionCostUSD != nil {
			last = r.SessionCostUSD
		}
	}
	return last
}

func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
