package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// RunLister lists an ant's runs, oldest first. The state store's run log
// implements it.
type RunLister interface {
	Runs(ctx context.Context, ant ports.AntID) ([]ports.RunRecord, error)
}

// BriefDeps are the ports the brief builder reads from.
type BriefDeps struct {
	Tracker   TrackerRoute
	CodeHost  CodeHostRoute
	Workspace WorkspaceRoute
	Git       ports.SourceControl
	// Files reads the repository's agent instructions file from the
	// ant's worktree.
	Files ports.WorktreeFiles
	Runs  RunLister
	Clock ports.Clock
	// Instance is this instance's id. The summary brief keeps only the
	// question and summary comments this instance posted.
	Instance string
}

// BriefBuilder builds the task brief of an ant's first run and the
// summary brief of a run that starts a new session (spec ant-execution,
// "Task brief" and "Sessions are persisted for resumption"). It gathers
// the inputs through ports and renders them with the domain.
type BriefBuilder struct {
	deps BriefDeps
}

// NewBriefBuilder returns a brief builder over deps.
func NewBriefBuilder(deps BriefDeps) *BriefBuilder {
	return &BriefBuilder{deps: deps}
}

// Task builds the task brief of the ant, with the run's input: the
// answer and the stored changes, if any. The item is read fresh, and
// Anthill's own comments are left out.
func (b *BriefBuilder) Task(ctx context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error) {
	in, _, err := b.taskInput(ctx, ant, answer, changes, taskBrief)
	if err != nil {
		return "", err
	}
	return domain.RenderTaskBrief(in), nil
}

// Summary builds the summary brief of the ant: the task brief's content
// plus the time left, the earlier runs, the commits of the ant branch and
// the ant PR with its human conversation, and the run's input.
func (b *BriefBuilder) Summary(ctx context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error) {
	task, ws, err := b.taskInput(ctx, ant, answer, changes, summaryBrief)
	if err != nil {
		return "", err
	}
	in := domain.SummaryBriefInput{TaskBriefInput: task, Remaining: ant.Budget.Remaining(b.deps.Clock.Now())}

	runs, err := b.deps.Runs.Runs(ctx, ant.ID)
	if err != nil {
		return "", fmt.Errorf("reading the runs of ant %d: %w", ant.ID, err)
	}
	for _, r := range runs {
		in.Runs = append(in.Runs, domain.BriefRun{Kind: runRole(r.Kind), StartedAt: r.StartedAt, Outcome: string(r.Outcome), Reason: r.Reason})
	}

	commits, err := b.deps.Git.NewCommits(ctx, ws.Clone, ant.Worktree, ant.BaseBranch)
	if err != nil {
		return "", fmt.Errorf("reading the commits of %s: %w", ant.Branch, err)
	}
	for _, c := range commits {
		in.Commits = append(in.Commits, domain.BriefCommit{Hash: c.Hash, Subject: c.Subject})
	}

	if ant.PR != nil {
		host, err := b.deps.CodeHost(ctx, ant.PR.Repo)
		if err != nil {
			return "", err
		}
		pr, _, err := host.PullRequest(ctx, *ant.PR)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", ant.PR, err)
		}
		in.PR = &domain.BriefPR{URL: pr.URL, Title: pr.Title, Body: pr.Body, Draft: pr.Draft, Comments: b.prComments(pr)}
	}
	return domain.RenderSummaryBrief(in), nil
}

// Resume builds the input of a run that resumes the ant's session: the
// answer and the stored changes. It reads nothing.
func (b *BriefBuilder) Resume(_ context.Context, _ ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error) {
	return domain.RenderResumeInput(answer, changes), nil
}

// taskInput reads what both briefs hold, and the ant's workspace.
func (b *BriefBuilder) taskInput(ctx context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange, kind briefKind) (domain.TaskBriefInput, ToolWorkspace, error) {
	tracker, err := b.deps.Tracker(ctx, ant.Item)
	if err != nil {
		return domain.TaskBriefInput{}, ToolWorkspace{}, err
	}
	item, _, err := tracker.Item(ctx, ant.Item)
	if err != nil {
		return domain.TaskBriefInput{}, ToolWorkspace{}, fmt.Errorf("reading %s: %w", ant.Item, err)
	}
	ws, err := b.deps.Workspace(ctx, ant.Item)
	if err != nil {
		return domain.TaskBriefInput{}, ToolWorkspace{}, err
	}
	instructions, err := b.instructions(ctx, ant, ws.Settings.AgentInstructions)
	if err != nil {
		return domain.TaskBriefInput{}, ToolWorkspace{}, err
	}
	in := domain.TaskBriefInput{
		Item: domain.BriefItem{
			Ref:                ant.Item,
			URL:                item.URL,
			Title:              item.Title,
			Body:               item.Body,
			AcceptanceCriteria: item.AcceptanceCriteria,
		},
		Comments:          b.briefComments(kind, item.Comments, "", 0),
		Branch:            ant.Branch,
		BaseBranch:        ant.BaseBranch,
		Budget:            ant.Budget.Total(),
		AgentInstructions: instructions,
		Answer:            answer,
		Changes:           changes,
	}
	return in, ws, nil
}

// instructions reads the repository's agent instructions file from the
// ant's worktree. A missing file, or one that leads outside the worktree,
// is noted in the brief and does not stop the run.
func (b *BriefBuilder) instructions(ctx context.Context, ant ports.AntRecord, path string) (domain.BriefInstructions, error) {
	out := domain.BriefInstructions{Path: path}
	if path == "" {
		return out, nil
	}
	data, cut, err := b.deps.Files.ReadWorktreeFile(ctx, ant.Worktree, path, domain.AgentInstructionsLimit)
	switch {
	case errors.Is(err, ports.ErrNotFound):
		out.Problem = domain.InstructionsMissing
	case errors.Is(err, ports.ErrOutsideWorktree):
		out.Problem = domain.InstructionsOutside
	case err != nil:
		return out, fmt.Errorf("reading the agent instructions %s: %w", path, err)
	default:
		out.Content, out.Truncated = string(data), cut
	}
	return out, nil
}

// briefKind is the brief whose comments briefComments chooses.
type briefKind int

const (
	taskBrief briefKind = iota
	summaryBrief
)

// prComments are the comments of a PR and of its review threads that the
// summary brief keeps, oldest first.
func (b *BriefBuilder) prComments(pr domain.PullRequest) []domain.BriefComment {
	out := b.briefComments(summaryBrief, pr.Comments, "", 0)
	for _, th := range pr.Threads {
		out = append(out, b.briefComments(summaryBrief, th.Comments, th.Path, th.Line)...)
	}
	slices.SortStableFunc(out, func(a, b domain.BriefComment) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

// briefComments keeps the comments a brief quotes. It is the one place
// that decides which Anthill comments, which only the marker identifies,
// a brief keeps. The task brief keeps only human comments. The summary
// brief also keeps the ant's own question and finish summary comments,
// posted by this instance, labelled as Anthill's, so that the answer and
// the earlier work make sense to a new session. Claim, status, finding
// and reply comments, and every other instance's comments, are left out.
func (b *BriefBuilder) briefComments(kind briefKind, comments []domain.Comment, path string, line int) []domain.BriefComment {
	out := []domain.BriefComment{}
	for _, c := range comments {
		bc := domain.BriefComment{Author: c.Author, CreatedAt: c.CreatedAt, Body: c.Body, Path: path, Line: line}
		if c.Origin() == domain.OriginAnthill {
			ac, ok := domain.ParseAnthillComment(c.Body)
			if kind != summaryBrief || !ok || !b.ownAntComment(ac) {
				continue
			}
			bc.Kind, bc.Body = ac.Marker.Kind, ac.Body
		}
		out = append(out, bc)
	}
	return out
}

// ownAntComment reports whether an Anthill comment is a question or a
// finish summary that an ant of this instance posted.
func (b *BriefBuilder) ownAntComment(ac domain.AnthillComment) bool {
	if ac.Role != domain.RoleAnt || ac.Marker.Instance != b.deps.Instance {
		return false
	}
	return ac.Marker.Kind == domain.CommentQuestion || ac.Marker.Kind == domain.CommentSummary
}

// runRole is the agent role of a run kind.
func runRole(k ports.RunKind) domain.AgentRole {
	if k == ports.RunFollowUp {
		return domain.RoleFollowUpRun
	}
	return domain.RoleTaskRun
}
