package app

// Detach lets the daemon stop with bounded work left. Each round and fix
// pass in progress is ended as when its PR ends (StopPR): its agent is
// stopped with StopGrace and pushes nothing. The round's end is recorded
// as usual, and the review claim it held is released, but nothing new
// starts: no round, fix pass or other decision, on any PR. The next
// start's triggers decide again from the current state (design D22).
//
// Wait returns once the work ended this way has returned, after which the
// shepherd uses the store no more, so the daemon can close it. Calling
// Detach again does nothing.
func (s *Shepherd) Detach() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detached = true
	for _, cancel := range s.cancels {
		cancel()
	}
}

// isDetached reports whether Detach was called.
func (s *Shepherd) isDetached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detached
}
