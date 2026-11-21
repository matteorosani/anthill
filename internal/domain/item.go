package domain

import "time"

// ItemState is the provider-independent state of an item.
type ItemState string

// The item states. GitHub's "closed as not planned" and Azure DevOps'
// removed states map to StateClosedNotPlanned, so that a dependency closed
// that way is not mistaken for completed work.
const (
	StateOpen             ItemState = "open"
	StateClosedCompleted  ItemState = "closed-completed"
	StateClosedNotPlanned ItemState = "closed-not-planned"
)

// ItemType is the provider-independent type of an item.
type ItemType string

// The item types.
const (
	TypeTask    ItemType = "task"
	TypeBug     ItemType = "bug"
	TypeFeature ItemType = "feature"
	TypeOther   ItemType = "other"
)

// Comment is one comment on an item or a pull request.
type Comment struct {
	ID     string
	Author string
	// URL is the comment's web address, used to link to it in messages.
	URL       string
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
	// Thread is the thread the comment is in where the provider threads
	// every PR comment, as Azure DevOps does, and "" elsewhere. A reply in
	// the thread of an ant's question answers it (spec
	// agent-conversation, "Answers resume the session").
	Thread string
}

// WorkItem is the normalized form of a GitHub issue or an Azure DevOps work
// item (spec work-item-sources). Dependencies, Parent and Children come
// only from provider-native links.
type WorkItem struct {
	Ref Ref
	URL string
	// Author is the login of the ticket author.
	Author string
	Title  string
	Body   string
	// AcceptanceCriteria is Azure DevOps' Acceptance Criteria field. It is
	// empty on GitHub, where acceptance criteria are a section of Body.
	AcceptanceCriteria string
	Type               ItemType
	State              ItemState
	Labels             []string
	Assignees          []string
	Dependencies       []Ref
	// Parent is nil when the item has no parent.
	Parent   *Ref
	Children []Ref
	Comments []Comment
	Revision string
	// CreatedAt is when the item was created, for its age in the
	// proposal list (spec queen-console, "Viewing proposals").
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ReviewThread is one review conversation on a pull request.
type ReviewThread struct {
	ID       string
	Path     string
	Line     int
	Resolved bool
	Comments []Comment
}

// PullRequest is the normalized form of a GitHub or Azure DevOps pull
// request (spec work-item-sources).
type PullRequest struct {
	Ref          Ref
	URL          string
	Title        string
	Body         string
	SourceBranch string
	TargetBranch string
	Author       string
	HeadCommit   string
	Draft        bool
	Labels       []string
	Threads      []ReviewThread
	Comments     []Comment
	// Item is the work item the PR was opened for, or nil when none was
	// detected.
	Item *Ref
	// State is PROpen, PRMerged or PRClosed. The PRs of a poll are open;
	// reading one PR tells whether it was merged or closed since (spec
	// pr-shepherd, "PR closure is human").
	State PRState
	// Fork is set when the source branch is in another repository than
	// the PR's. ForkRepo is that repository and ForkURL its clone URL,
	// where an auto-fix of the branch is pushed; both are empty when the
	// fork was deleted. MaintainerCanModify is set when
	// the fork lets the repository's maintainers push to the branch
	// (GitHub's "Allow edits by maintainers"). Azure DevOps has no such
	// setting, so it is never set there and a fork PR there is not
	// pushable (spec pr-shepherd, "Fork branch not pushable").
	Fork                bool
	ForkRepo            Repository
	ForkURL             string
	MaintainerCanModify bool
	Revision            string
	UpdatedAt           time.Time
}
