package app

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
)

// AutofixMarkerPrefix starts the marker ID of the status comment a fix
// pass posts after its push, autofix-<head>, where head is the commit
// it pushed. Every instance reads the pushed heads from these comments,
// so that a fix pass's push starts no round (spec pr-shepherd,
// "Automatic fixes").
const AutofixMarkerPrefix = "autofix-"

// AutofixUnavailableMarkerID is the marker ID of the comment that
// auto-fix is not possible on a PR for reason, posted once per PR and
// reason whichever instance posts it.
func AutofixUnavailableMarkerID(reason domain.AutofixUnavailableReason) string {
	return AutofixMarkerPrefix + "unavailable-" + string(reason)
}

// The service the shepherd implements for /anthill fix.
var _ FixRequests = (*Shepherd)(nil)

// OptInFix takes /anthill fix on a human PR on (spec agent-conversation,
// "Fix on a human PR"): the instance that wins the PR's review claim
// asks the playbook again, which starts a fix pass for the thread's
// finding when it is auto-fixable. The opt-in itself is the command
// comment in the thread, which every instance reads from the PR
// (fixThreads), so a repeated call is harmless. It returns false, and
// does nothing, when this instance does not review the PR or another
// instance holds its review claim. While a round runs on the PR here,
// the decisions after the round see the opt-in.
func (s *Shepherd) OptInFix(ctx context.Context, pr domain.Ref, thread, commandComment string) (bool, error) {
	if thread == "" {
		return false, fmt.Errorf("/anthill fix in comment %s on %s is not in a thread", commandComment, pr)
	}
	defer s.lockPR(pr)()
	if s.roundActive(pr) {
		return true, nil
	}
	ws, err := s.deps.Route(ctx, pr)
	if err != nil {
		return false, fmt.Errorf("finding the repository of %s: %w", pr, err)
	}
	held, err := s.claim(ctx, ws, pr)
	if err != nil || held == nil {
		return false, err
	}
	return true, s.decide(ctx, pr, held)
}

// autofixSnapshot fills in what the playbook needs for auto-fix from
// the PR: the threads opted in by /anthill fix, the heads that fix
// passes pushed, and the auto-fix unavailable comments already posted.
// Only comments by authors with write access count, and for /anthill
// fix also the PR's author (spec agent-conversation, "Commands are
// authorized").
func (s *Shepherd) autofixSnapshot(pr domain.PullRequest, writers func(string) (bool, error), snap *domain.PRSnapshot) error {
	var err error
	if snap.FixThreads, err = fixThreads(pr, writers); err != nil {
		return err
	}
	if err := reclassify(pr, writers, snap.Rounds); err != nil {
		return err
	}
	for _, c := range prComments(pr) {
		m, ok := domain.FindMarker(c.Body)
		if !ok || m.Kind != domain.CommentStatus || !strings.HasPrefix(m.ID, AutofixMarkerPrefix) {
			continue
		}
		if ok, err := writers(c.Author); err != nil {
			return err
		} else if !ok {
			continue
		}
		if reason, unavailable := strings.CutPrefix(m.ID, AutofixUnavailableMarkerID("")); unavailable {
			snap.UnavailablePosted = append(snap.UnavailablePosted, domain.AutofixUnavailableReason(reason))
		} else {
			snap.FixHeads = append(snap.FixHeads, strings.TrimPrefix(m.ID, AutofixMarkerPrefix))
		}
	}
	return nil
}

// fixThreads lists the review threads holding /anthill fix from the PR's
// author or a user with write access.
func fixThreads(pr domain.PullRequest, writers func(string) (bool, error)) ([]string, error) {
	var out []string
	for _, t := range pr.Threads {
		for _, c := range t.Comments {
			if _, anthill := domain.FindMarker(c.Body); anthill || !slices.Contains(domain.ParseCommands(c.Body), domain.CmdFix) {
				continue
			}
			ok := strings.EqualFold(c.Author, pr.Author)
			if !ok {
				var err error
				if ok, err = writers(c.Author); err != nil {
					return nil, err
				}
			}
			if ok {
				out = append(out, t.ID)
				break
			}
		}
	}
	return out, nil
}

// postAutofixUnavailable carries out a PostAutofixUnavailable decision.
// The comment is posted idempotently by its marker ID; its effect is the
// comment itself.
func (s *Shepherd) postAutofixUnavailable(ctx context.Context, t *prTurn, d domain.PostAutofixUnavailable) (bool, error) {
	_, err := t.host.AddComment(ctx, t.pr.Ref, domain.AnthillComment{
		Role: domain.RoleShepherd,
		Body: domain.AutofixUnavailableText(d.Reason),
		Marker: domain.Marker{
			Version:  domain.MarkerVersion,
			ID:       AutofixUnavailableMarkerID(d.Reason),
			Instance: t.held.Instance,
			Kind:     domain.CommentStatus,
			Ref:      t.pr.Ref.String(),
		},
	})
	if err != nil {
		return false, fmt.Errorf("posting that auto-fix is not possible on %s: %w", t.pr.Ref, err)
	}
	// Decide again only once the comment is visible; otherwise the next
	// trigger does (issue #192).
	return s.awaitMarker(ctx, t.pr.Ref, AutofixUnavailableMarkerID(d.Reason))
}

// reclassify marks needs-developer each finding of rounds whose thread
// holds the reply of a fix pass that reclassified it, by an author with
// write access (spec pr-shepherd, "Failed auto-fix falls back to
// developer").
func reclassify(pr domain.PullRequest, writers func(string) (bool, error), rounds []domain.PRRound) error {
	threads := map[string]domain.ReviewThread{}
	for _, t := range pr.Threads {
		threads[t.ID] = t
	}
	for i := range rounds {
		for j := range rounds[i].Findings {
			f := &rounds[i].Findings[j]
			if f.Thread == "" || f.Classification == domain.NeedsDeveloper {
				continue
			}
			for _, c := range threads[f.Thread].Comments {
				if !domain.Reclassified(c, f.Thread) {
					continue
				}
				if ok, err := writers(c.Author); err != nil {
					return err
				} else if ok {
					f.Classification = domain.NeedsDeveloper
					break
				}
			}
		}
	}
	return nil
}
