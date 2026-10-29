package ports

import (
	"context"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

// ItemPage is the result of one poll of a tracker repository.
type ItemPage struct {
	// Items are the items changed since the cursor, normalized (spec
	// work-item-sources, "Normalized work item model").
	Items []domain.WorkItem
	// Cursor is passed to the next poll to fetch only what changed.
	Cursor string
	// ServerTime is the provider's time when it answered, such as the
	// Date header. Claim checks use it as now (design D3).
	ServerTime time.Time
}

// TrackerPort reads and writes tickets on a provider: GitHub issues or
// Azure DevOps work items (design D1). Claim comments, the claim label
// and the assignee go through ClaimStore instead.
type TrackerPort interface {
	// Items polls a tracker repository for items changed since cursor; an
	// empty cursor reads every opted-in candidate (spec work-item-sources,
	// "Polling cadence" and "Change detection").
	Items(ctx context.Context, repo domain.Repository, cursor string) (ItemPage, error)
	// Item reads one item fresh, with the provider's server time, for the
	// re-check at acceptance (spec task-eligibility, "Re-check at
	// acceptance").
	Item(ctx context.Context, ref domain.Ref) (domain.WorkItem, time.Time, error)
	// AddComment posts an Anthill comment on an item (spec
	// agent-conversation). When the marker has an ID and a comment with
	// the same marker ID exists, it returns that comment instead of
	// posting again (design D7).
	AddComment(ctx context.Context, ref domain.Ref, c domain.AnthillComment) (domain.Comment, error)
	// EditComment replaces the text of an Anthill comment, such as the one
	// status comment of a run (design, Risks: comment noise).
	EditComment(ctx context.Context, ref domain.Ref, commentID string, c domain.AnthillComment) (domain.Comment, error)
	// AddLabel and RemoveLabel change an item's labels, such as the hold
	// label after finish(no-change) (spec ant-execution, "No change
	// needed").
	AddLabel(ctx context.Context, ref domain.Ref, label string) error
	RemoveLabel(ctx context.Context, ref domain.Ref, label string) error
	// CanWrite reports whether login has write access to the tracker
	// repository, so that only write-access users trigger work and only
	// their claim markers count (spec change-reaction, "Who can trigger
	// work"; design, Risks: forged markers).
	CanWrite(ctx context.Context, repo domain.Repository, login string) (bool, error)
}
