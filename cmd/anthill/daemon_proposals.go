package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// maxListedProposals is how many new proposals one notification names.
const maxListedProposals = 5

// proposalWatch raises the "new proposal" notification (spec
// queen-console, "Notifications"). It keeps in memory the items of the
// proposal list as last evaluated, and after each evaluation tells the
// queen about the items that were not in it. Hidden proposals are not in
// the list, so a declined or snoozed item raises nothing. After a
// restart, the first evaluation tells about the whole list once.
type proposalWatch struct {
	notifier ports.Notifier

	mu       sync.Mutex
	proposed map[domain.Ref]bool
}

func newProposalWatch(notifier ports.Notifier) *proposalWatch {
	return &proposalWatch{notifier: notifier, proposed: map[domain.Ref]bool{}}
}

// update records a new evaluation of the proposals of one provider
// connection, or of every connection when connection is "", and notifies
// the new ones. When the list is incomplete, because a tracker could not
// be read, items missing from it are kept, so that they are not told
// about again once the tracker answers.
func (w *proposalWatch) update(ctx context.Context, connection string, list app.ProposalList) {
	w.mu.Lock()
	var fresh []app.Proposal
	now := map[domain.Ref]bool{}
	for _, p := range list.Proposals {
		now[p.Item] = true
		if !w.proposed[p.Item] {
			fresh = append(fresh, p)
		}
	}
	if len(list.Warnings) == 0 {
		for ref := range w.proposed {
			if !now[ref] && (connection == "" || domain.ConnectionName(ref.Repo) == connection) {
				delete(w.proposed, ref)
			}
		}
	}
	for ref := range now {
		w.proposed[ref] = true
	}
	w.mu.Unlock()
	if len(fresh) == 0 {
		return
	}
	// A notification that cannot be shown is not a reason to fail.
	_ = w.notifier.Notify(ctx, proposalNotification(fresh))
}

// proposalNotification names one new proposal, or counts several and
// names the first few.
func proposalNotification(fresh []app.Proposal) ports.Notification {
	if len(fresh) == 1 {
		p := fresh[0]
		ref := p.Item
		return ports.Notification{
			Kind:  ports.NotifyProposal,
			Title: "New proposal: " + app.ItemName(p.Item),
			Body:  p.Title,
			Ref:   &ref,
		}
	}
	var lines []string
	for i, p := range fresh {
		if i == maxListedProposals {
			lines = append(lines, fmt.Sprintf("and %d more", len(fresh)-i))
			break
		}
		lines = append(lines, app.ItemName(p.Item)+" "+p.Title)
	}
	return ports.Notification{
		Kind:  ports.NotifyProposal,
		Title: fmt.Sprintf("%d new proposals", len(fresh)),
		Body:  strings.Join(lines, "\n"),
	}
}
