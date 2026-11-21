package domain

import "slices"

// PRFinding is a finding of a round as the playbook sees it.
type PRFinding struct {
	Finding
	// Resolved reports that the finding's thread is resolved, such as
	// after a fix pass pushed its fix. A finding without a thread, which
	// is only in the round's summary, is never resolved.
	Resolved bool
	// Thread is the ID of the finding's review thread, or "" for a
	// finding only in the round's summary.
	Thread string
}

// PRRound is one round on the PR, done or in progress, by any instance.
type PRRound struct {
	// Head is the head commit the round reviewed.
	Head string
	// Requested is set for a round started by /anthill review.
	Requested bool
	// Findings are the round's findings, as it posted them. A finding a
	// fix pass could not fix is reclassified needs-developer (spec
	// pr-shepherd, "Failed auto-fix falls back to developer").
	Findings []PRFinding
	// Handed reports that the round's findings were handed to the owning
	// ant (spec change-reaction, "Shepherd findings on ant PRs").
	Handed bool
}

// PRSnapshot is the state the default playbook decides a PR from (design
// D22). The service builds it from the PR, the comments of earlier rounds
// on it, the local store and the repository's settings. Claims,
// participation and the review runner are left out on purpose: the
// shepherd applies them to the decision, whatever it is.
type PRSnapshot struct {
	// Head is the PR's current head commit.
	Head  string
	Draft bool
	// AntPR reports that the PR is an ant PR, and OwnAnt that its ant is
	// on this instance.
	AntPR  bool
	OwnAnt bool
	// InScope reports that the PR is in the repository's shepherd scope
	// (InScope).
	InScope bool
	// Autofix reports that a human PR opted in to auto-fix, by the
	// autofix label or by /anthill fix.
	Autofix bool
	// Rounds are the PR's rounds, oldest first.
	Rounds []PRRound
	// RoundRequested reports that /anthill review requested a round that
	// has not started yet. One round serves every pending request.
	RoundRequested bool
	// RoundLimit is the repository's round limit for automatic rounds.
	RoundLimit int
	// LimitPosted reports that the round limit comment is on the PR.
	LimitPosted bool
	// FixHeads are the head commits that fix passes pushed. A push by a
	// fix pass starts no round (spec pr-shepherd, "Automatic fixes").
	FixHeads []string
	// VerificationCommand reports that the repository has one, and
	// Pushable that Anthill can push to the PR branch: false for a fork
	// that does not allow maintainer edits.
	VerificationCommand bool
	Pushable            bool
}

// PRDecision is the one next action the default playbook returns for a
// PR. It is one of StartRound, PostRoundLimit, HandFindings, StartFixPass
// and Nothing. The unexported method seals the interface.
type PRDecision interface {
	prDecision()
}

// StartRound starts a round on the PR's head commit. Its effect, recorded
// with the start: the PR has a round on Head, and a requested round
// serves every pending request.
type StartRound struct {
	Head      string
	Requested bool
}

// PostRoundLimit posts the comment that the automatic rounds are used up
// and that /anthill review requests another one. Its effect: the comment
// is on the PR.
type PostRoundLimit struct {
	Limit int
}

// HandFindings hands the findings of one round on an ant PR to the owning
// ant, whose next follow-up run gets them (spec change-reaction, "Shepherd
// findings on ant PRs"): the blocking and suggestion findings, of either
// classification, and the auto-fixable nits. Its effect: the round is
// handed.
type HandFindings struct {
	Head     string
	Findings []Finding
}

// StartFixPass starts a fix pass for the auto-fixable findings of the
// round on the PR's head commit, on a human PR that opted in (spec
// pr-shepherd, "Fix agent pass"). Its effect: each finding is resolved or
// reclassified needs-developer, and a push is recorded in FixHeads.
type StartFixPass struct {
	Head     string
	Findings []Finding
}

func (StartRound) prDecision()     {}
func (PostRoundLimit) prDecision() {}
func (HandFindings) prDecision()   {}
func (StartFixPass) prDecision()   {}
func (Nothing) prDecision()        {}

// DecidePR is the default playbook for PRs (design D22): it returns the
// one next action for the PR in s. Like DecideAnt, it decides from the
// state alone, and after a decision's effect is recorded the same
// decision never comes back.
//
// The decisions come in this order:
//  1. On an ant PR whose ant is on this instance, the oldest round not
//     handed yet that has blocking or suggestion findings still open is
//     handed to the ant, with its auto-fixable nits. Nits alone are left
//     to developers.
//  2. On a human PR that opted in to auto-fix, in a repository with a
//     verification command and a branch Anthill can push to, the open
//     auto-fixable findings of the round on the head commit get a fix
//     pass.
//  3. A pending /anthill review starts a requested round on the head
//     commit, even out of scope, on a draft, after the round limit or on a
//     head commit already reviewed.
//  4. An in-scope PR that is not a draft starts an automatic round when
//     its head commit has no round, was not pushed by a fix pass, and
//     automatic rounds remain.
//  5. When no automatic round remains for such a head commit, the round
//     limit comment is posted, once per PR.
func DecidePR(s PRSnapshot) PRDecision {
	if d, ok := s.handFindings(); ok {
		return d
	}
	if d, ok := s.fixPass(); ok {
		return d
	}
	if s.RoundRequested {
		return StartRound{Head: s.Head, Requested: true}
	}
	if !s.InScope || s.Draft || s.reviewed(s.Head) || slices.Contains(s.FixHeads, s.Head) {
		return Nothing{}
	}
	if s.automaticRounds() < s.RoundLimit {
		return StartRound{Head: s.Head}
	}
	if !s.LimitPosted {
		return PostRoundLimit{Limit: s.RoundLimit}
	}
	return Nothing{}
}

// handFindings decides the hand-over of a round's findings to the ant.
func (s PRSnapshot) handFindings() (HandFindings, bool) {
	if !s.AntPR || !s.OwnAnt {
		return HandFindings{}, false
	}
	for _, r := range s.Rounds {
		if r.Handed {
			continue
		}
		var handed []Finding
		actionable := false
		for _, f := range r.Findings {
			switch {
			case f.Resolved:
			case f.Severity == SeverityBlocking || f.Severity == SeveritySuggestion:
				actionable = true
				handed = append(handed, f.Finding)
			case f.Classification == AutoFixable:
				handed = append(handed, f.Finding)
			}
		}
		if actionable {
			return HandFindings{Head: r.Head, Findings: handed}, true
		}
	}
	return HandFindings{}, false
}

// fixPass decides the fix pass of a human PR that opted in.
func (s PRSnapshot) fixPass() (StartFixPass, bool) {
	if s.AntPR || !s.Autofix || !s.VerificationCommand || !s.Pushable {
		return StartFixPass{}, false
	}
	r, ok := s.round(s.Head)
	if !ok {
		return StartFixPass{}, false
	}
	var fixes []Finding
	for _, f := range r.Findings {
		if !f.Resolved && f.Classification == AutoFixable {
			fixes = append(fixes, f.Finding)
		}
	}
	if len(fixes) == 0 {
		return StartFixPass{}, false
	}
	return StartFixPass{Head: s.Head, Findings: fixes}, true
}

// round returns the last round on head.
func (s PRSnapshot) round(head string) (PRRound, bool) {
	for i := len(s.Rounds) - 1; i >= 0; i-- {
		if s.Rounds[i].Head == head {
			return s.Rounds[i], true
		}
	}
	return PRRound{}, false
}

// reviewed reports whether a round reviewed head.
func (s PRSnapshot) reviewed(head string) bool {
	_, ok := s.round(head)
	return ok
}

// automaticRounds counts the automatic rounds; requested rounds are
// counted apart (spec pr-shepherd, "Developer-requested rounds").
func (s PRSnapshot) automaticRounds() int {
	n := 0
	for _, r := range s.Rounds {
		if !r.Requested {
			n++
		}
	}
	return n
}
