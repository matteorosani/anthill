package domain

// RunCost is the cost of one run or round from the cost its runner
// reported (spec ant-execution, "Usage is recorded", "Cost reported for a
// whole session"). A nil cost is unknown, which is different from zero.
//
// When sessionTotal is false, reported is the run's own cost and is kept
// as it is. Otherwise reported is the running total of the runner's
// session, and the run's cost is reported minus last, the last running
// total the runner reported for the session. last counts as zero for a
// run that started a new session. The cost is unknown when reported is
// unknown, when last is unknown for a resumed session, or when the
// difference is negative.
func RunCost(reported *float64, sessionTotal bool, last *float64, newSession bool) *float64 {
	if reported == nil || !sessionTotal {
		return reported
	}
	if newSession {
		c := *reported
		return &c
	}
	if last == nil {
		return nil
	}
	c := *reported - *last
	if c < 0 {
		return nil
	}
	return &c
}
