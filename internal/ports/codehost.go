package ports

import (
	"context"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

// PullRequestPage is the result of one poll of a code repository.
type PullRequestPage struct {
	// PullRequests are the open PRs changed since the cursor, normalized
	// (spec work-item-sources, "Pull request ingestion").
	PullRequests []domain.PullRequest
	Cursor       string
	// ServerTime is the provider's time when it answered (design D3).
	ServerTime time.Time
}

// NewPullRequest is what open_pull_request sends, after Anthill has added
// the closing reference and the anthill label (spec ant-tools, "Pull
// request tools").
type NewPullRequest struct {
	Repo   domain.Repository
	Source string
	Target string
	Title  string
	Body   string
	Draft  bool
	Labels []string
}

// PullRequestUpdate changes the fields that are set and leaves nil ones
// as they are.
type PullRequestUpdate struct {
	Title *string
	Body  *string
	Draft *bool
}

// ReviewComment is an inline comment on lines of a PR, used for findings
// with a known location (spec pr-shepherd, "Findings are posted as PR
// comments").
type ReviewComment struct {
	Path      string
	StartLine int
	// EndLine is 0 for a single line.
	EndLine int
	Comment domain.AnthillComment
}

// CodeHostPort reads and writes pull requests on a provider (design D1).
type CodeHostPort interface {
	// PullRequests polls a code repository for open PRs changed since
	// cursor (spec work-item-sources, "Pull request ingestion").
	PullRequests(ctx context.Context, repo domain.Repository, cursor string) (PullRequestPage, error)
	// PullRequest reads one PR fresh, with the provider's server time,
	// such as before a review round (spec pr-shepherd).
	PullRequest(ctx context.Context, ref domain.Ref) (domain.PullRequest, time.Time, error)
	// OpenPullRequest opens a PR (spec ant-tools, "Pull request tools").
	OpenPullRequest(ctx context.Context, pr NewPullRequest) (domain.PullRequest, error)
	// UpdatePullRequest changes a PR's title, body or draft state, also to
	// force a draft after a failed or timed-out run (spec ant-execution,
	// "Draft PRs for incomplete work").
	UpdatePullRequest(ctx context.Context, ref domain.Ref, u PullRequestUpdate) (domain.PullRequest, error)
	// AddComment posts an Anthill comment on a PR, idempotently by marker
	// ID as TrackerPort.AddComment does (design D7).
	AddComment(ctx context.Context, ref domain.Ref, c domain.AnthillComment) (domain.Comment, error)
	// EditComment replaces the text of an Anthill comment on a PR.
	EditComment(ctx context.Context, ref domain.Ref, commentID string, c domain.AnthillComment) (domain.Comment, error)
	// AddReviewComment posts an inline finding (design D9).
	AddReviewComment(ctx context.Context, ref domain.Ref, c ReviewComment) (domain.Comment, error)
	// ReplyToThread answers in a review thread (spec ant-tools,
	// "Conversation tools"; spec pr-shepherd, "Anthill verifies, pushes
	// and replies").
	ReplyToThread(ctx context.Context, ref domain.Ref, threadID string, c domain.AnthillComment) (domain.Comment, error)
	// ResolveThread resolves a review thread after a verified auto-fix
	// (spec pr-shepherd, "Anthill verifies, pushes and replies").
	ResolveThread(ctx context.Context, ref domain.Ref, threadID string) error
	// CanWrite reports whether login has write access to the code
	// repository, for commands on PRs (spec agent-conversation, "Commands
	// are authorized"). It is separate from TrackerPort.CanWrite because
	// tickets and code may live on different providers (design D1).
	CanWrite(ctx context.Context, repo domain.Repository, login string) (bool, error)
}
