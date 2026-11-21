package app

import (
	"context"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// fixReplies answers the threads of a fix pass's findings, idempotently
// by marker ID.
type fixReplies struct {
	host     ports.CodeHostPort
	pr       domain.PullRequest
	instance string
}

func (r fixReplies) reply(ctx context.Context, thread, id, body string) error {
	_, err := r.host.ReplyToThread(ctx, r.pr.Ref, thread, domain.AnthillComment{
		Role: domain.RoleShepherd,
		Body: body,
		Marker: domain.Marker{
			Version: domain.MarkerVersion, ID: id, Instance: r.instance,
			Kind: domain.CommentReply, Ref: r.pr.Ref.String(),
		},
	})
	if err != nil {
		return fmt.Errorf("replying in thread %s on %s: %w", thread, r.pr.Ref, err)
	}
	return nil
}

// fixed answers a fixed finding's thread with its commit link and
// resolves it.
func (r fixReplies) fixed(ctx context.Context, thread, commit string) error {
	if err := r.reply(ctx, thread, domain.FixedMarkerID(thread), domain.FixedText(commit, domain.CommitLink(r.pr, commit))); err != nil {
		return err
	}
	if err := r.host.ResolveThread(ctx, r.pr.Ref, thread); err != nil {
		return fmt.Errorf("resolving thread %s on %s: %w", thread, r.pr.Ref, err)
	}
	return nil
}

// reclassify answers a finding's thread with body, which reclassifies it
// needs-developer.
func (r fixReplies) reclassify(ctx context.Context, thread, body string) error {
	return r.reply(ctx, thread, domain.ReclassifiedMarkerID(thread), body)
}

// fail reclassifies every finding of the pass after a failure: nothing
// was pushed.
func (r fixReplies) fail(ctx context.Context, threads []string, why domain.FixFailure, detail string) (FixOutcome, error) {
	out := FixOutcome{Failure: why}
	for _, t := range threads {
		if err := r.reclassify(ctx, t, domain.FixFailedText(why, detail)); err != nil {
			return out, err
		}
		out.Reclassified++
	}
	return out, nil
}
