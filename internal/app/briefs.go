package app

import (
	"context"
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
	Runs      RunLister
	Clock     ports.Clock
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
	in, _, err := b.taskInput(ctx, ant, answer, changes)
	if err != nil {
		return "", err
	}
	return domain.RenderTaskBrief(in), nil
}

// Summary builds the summary brief of the ant: the task brief's content
// plus the time left, the earlier runs, the commits of the ant branch and
// the ant PR with its human conversation, and the run's input.
func (b *BriefBuilder) Summary(ctx context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error) {
	task, ws, err := b.taskInput(ctx, ant, answer, changes)
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
		in.PR = &domain.BriefPR{URL: pr.URL, Title: pr.Title, Body: pr.Body, Draft: pr.Draft, Comments: prComments(pr)}
	}
	return domain.RenderSummaryBrief(in), nil
}

// Resume builds the input of a run that resumes the ant's session: the
// answer and the stored changes. It reads nothing.
func (b *BriefBuilder) Resume(_ context.Context, _ ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error) {
	return domain.RenderResumeInput(answer, changes), nil
}

// taskInput reads what both briefs hold, and the ant's workspace.
func (b *BriefBuilder) taskInput(ctx context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (domain.TaskBriefInput, ToolWorkspace, error) {
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
	in := domain.TaskBriefInput{
		Item: domain.BriefItem{
			Ref:                ant.Item,
			URL:                item.URL,
			Title:              item.Title,
			Body:               item.Body,
			AcceptanceCriteria: item.AcceptanceCriteria,
		},
		Comments:          briefComments(item.Comments, "", 0),
		Branch:            ant.Branch,
		BaseBranch:        ant.BaseBranch,
		Budget:            ant.Budget.Total(),
		AgentInstructions: ws.Settings.AgentInstructions,
		Answer:            answer,
		Changes:           changes,
	}
	return in, ws, nil
}

// prComments are the human comments of a PR and of its review threads,
// oldest first.
func prComments(pr domain.PullRequest) []domain.BriefComment {
	out := briefComments(pr.Comments, "", 0)
	for _, th := range pr.Threads {
		out = append(out, briefComments(th.Comments, th.Path, th.Line)...)
	}
	slices.SortStableFunc(out, func(a, b domain.BriefComment) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

// briefComments keeps the comments a brief quotes: the human ones, which
// the marker decides. It is the one place that filters Anthill comments
// out of both briefs.
func briefComments(comments []domain.Comment, path string, line int) []domain.BriefComment {
	out := []domain.BriefComment{}
	for _, c := range domain.HumanComments(comments) {
		out = append(out, domain.BriefComment{Author: c.Author, CreatedAt: c.CreatedAt, Body: c.Body, Path: path, Line: line})
	}
	return out
}

// runRole is the agent role of a run kind.
func runRole(k ports.RunKind) domain.AgentRole {
	if k == ports.RunFollowUp {
		return domain.RoleFollowUpRun
	}
	return domain.RoleTaskRun
}
