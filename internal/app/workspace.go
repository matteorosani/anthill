package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Workspace makes, pushes from and cleans up ant branches and worktrees
// under the branching policy (spec branching-policy). It works in the
// developer's clone only through git calls that leave its working copy,
// checked-out branch and uncommitted changes alone.
type Workspace struct {
	git  ports.SourceControl
	root string
}

// NewWorkspace returns a workspace that puts worktrees under root, the
// effective worktree root (domain.WorktreeRoot).
func NewWorkspace(git ports.SourceControl, root string) *Workspace {
	return &Workspace{git: git, root: root}
}

// PrepareRequest asks for the branch and worktree of an ant.
type PrepareRequest struct {
	Clone ports.Clone
	Repo  domain.Repository
	Item  domain.Ref
	Title string
	// Settings is the effective configuration after the item's labels
	// (Effective.ForItem), for the branch prefix and the base branch.
	Settings domain.Effective
	Trailers domain.Trailers
	// Branch and Worktree come from the ant's record when an earlier run
	// made them. Both empty means a new ant.
	Branch   string
	Worktree string
}

// AntWorkspace is where an ant works.
type AntWorkspace struct {
	Branch   string
	Base     string
	Worktree string
}

// Prepare fetches, resolves the base branch, and returns the ant's branch
// and worktree. For a new ant it creates the branch from the fetched
// base, with a numeric suffix when the name is taken by someone else, and
// a worktree under the root. For an ant with a record it reuses both,
// recreating the worktree from the local or pushed branch when it is
// missing. A base branch that is not on the remote is a
// *domain.BaseBranchMissingError, and nothing is created.
func (w *Workspace) Prepare(ctx context.Context, req PrepareRequest) (AntWorkspace, error) {
	if err := req.Trailers.Validate(); err != nil {
		return AntWorkspace{}, err
	}
	if err := w.git.Fetch(ctx, req.Clone); err != nil {
		return AntWorkspace{}, fmt.Errorf("fetching: %w", err)
	}
	base, err := w.base(ctx, req.Clone, req.Settings.BaseBranch)
	if err != nil {
		return AntWorkspace{}, err
	}
	if req.Branch != "" {
		return w.reuse(ctx, req, base)
	}

	name, err := domain.BranchName(req.Settings.BranchPrefix, req.Item, req.Title)
	if err != nil {
		return AntWorkspace{}, err
	}
	branch, reuse, err := domain.ChooseBranchName(name, req.Trailers.Owner(), func(n string) (domain.BranchCandidate, error) {
		info, err := w.git.Branch(ctx, req.Clone, n, base)
		if errors.Is(err, ports.ErrNotFound) {
			return domain.BranchCandidate{}, nil
		}
		return domain.BranchCandidate{Exists: err == nil, Owners: info.Owners}, err
	})
	if err != nil {
		return AntWorkspace{}, err
	}
	created := false
	if !reuse {
		if err := w.git.CreateBranch(ctx, req.Clone, branch, base); err != nil {
			return AntWorkspace{}, fmt.Errorf("creating branch %s: %w", branch, err)
		}
		created = true
	} else if created, err = w.ensureLocal(ctx, req.Clone, branch); err != nil {
		return AntWorkspace{}, err
	}
	path := domain.WorktreePath(w.root, req.Repo, branch)
	if err := w.git.AddWorktree(ctx, req.Clone, path, branch, req.Trailers); err != nil {
		// Undo the steps already done (spec ant-execution, "Starting an
		// ant").
		if created {
			_ = w.git.DeleteBranch(ctx, req.Clone, branch, false)
		}
		return AntWorkspace{}, fmt.Errorf("adding worktree %s: %w", path, err)
	}
	return AntWorkspace{Branch: branch, Base: base, Worktree: path}, nil
}

// base resolves the base branch and checks that the remote has it.
func (w *Workspace) base(ctx context.Context, clone ports.Clone, configured string) (string, error) {
	base := configured
	if base == "" {
		def, err := w.git.RemoteDefaultBranch(ctx, clone)
		if err != nil {
			return "", fmt.Errorf("finding the remote's default branch: %w", err)
		}
		base = domain.ResolveBase(configured, def)
	}
	info, err := w.git.Branch(ctx, clone, base, "")
	if errors.Is(err, ports.ErrNotFound) || err == nil && !info.Remote {
		return "", &domain.BaseBranchMissingError{Branch: base}
	}
	if err != nil {
		return "", fmt.Errorf("looking up base branch %s: %w", base, err)
	}
	return base, nil
}

// reuse returns the recorded branch and worktree, recreating the worktree
// when it is gone (spec branching-policy, "Second run for the same item";
// spec ant-execution, "Session index recovery").
func (w *Workspace) reuse(ctx context.Context, req PrepareRequest, base string) (AntWorkspace, error) {
	ws := AntWorkspace{Branch: req.Branch, Base: base, Worktree: req.Worktree}
	if ws.Worktree == "" {
		ws.Worktree = domain.WorktreePath(w.root, req.Repo, req.Branch)
	}
	_, err := w.git.WorktreeStatus(ctx, req.Clone, ws.Worktree)
	if err == nil {
		return ws, nil
	}
	if !errors.Is(err, ports.ErrNotFound) {
		return AntWorkspace{}, err
	}
	if _, err := w.ensureLocal(ctx, req.Clone, req.Branch); err != nil {
		return AntWorkspace{}, err
	}
	if err := w.git.AddWorktree(ctx, req.Clone, ws.Worktree, req.Branch, req.Trailers); err != nil {
		return AntWorkspace{}, fmt.Errorf("adding worktree %s: %w", ws.Worktree, err)
	}
	return ws, nil
}

// ensureLocal makes sure a local branch exists, creating it from the
// pushed one when needed. created reports whether it did.
func (w *Workspace) ensureLocal(ctx context.Context, clone ports.Clone, branch string) (created bool, err error) {
	info, err := w.git.Branch(ctx, clone, branch, "")
	if err != nil {
		return false, fmt.Errorf("looking up branch %s: %w", branch, err)
	}
	if info.Local {
		return false, nil
	}
	if err := w.git.CreateBranch(ctx, clone, branch, branch); err != nil {
		return false, fmt.Errorf("creating branch %s from the remote: %w", branch, err)
	}
	return true, nil
}

// PushRequest asks to push a worktree's current branch.
type PushRequest struct {
	Clone    ports.Clone
	Worktree string
	// Owner is the item and instance of the pusher, from its trailers.
	Owner domain.BranchOwner
	// Policy holds the kind of push, the allowed branch, the opt-in, the
	// base branch, the protected patterns and the force flag. Push fills
	// in the branch and the remote's default branch.
	Policy domain.PushRequest
}

// Push checks the worktree's current branch against the branching policy
// and pushes it, never with force. A refusal is a
// *domain.PushRefusedError and pushes nothing. A push rejected because
// the remote moved is ports.ErrConflict; one rejected because it rewrote
// pushed commits is ports.ErrHistoryRewritten, with
// domain.MessageHistoryRewritten. The output is git's.
func (w *Workspace) Push(ctx context.Context, req PushRequest) (string, error) {
	status, err := w.git.WorktreeStatus(ctx, req.Clone, req.Worktree)
	if err != nil {
		return "", err
	}
	def, err := w.git.RemoteDefaultBranch(ctx, req.Clone)
	if err != nil {
		return "", fmt.Errorf("finding the remote's default branch: %w", err)
	}
	policy := req.Policy
	policy.Branch, policy.Default = status.Branch, def
	if err := domain.CheckPush(policy); err != nil {
		return "", err
	}
	out, err := w.git.Push(ctx, req.Clone, status.Branch, req.Owner)
	if errors.Is(err, ports.ErrHistoryRewritten) {
		return out, fmt.Errorf("%s: %w", domain.MessageHistoryRewritten, err)
	}
	return out, err
}

// ErrNeedsConfirmation is returned, wrapped in a
// *NeedsConfirmationError, when cleanup would delete unpushed work.
var ErrNeedsConfirmation = errors.New("cleanup needs confirmation")

// NeedsConfirmationError lists what cleanup would delete, for the queen
// to confirm (spec branching-policy, "Unpushed work").
type NeedsConfirmationError struct {
	Unpushed    []ports.Commit
	Uncommitted bool
}

func (e *NeedsConfirmationError) Error() string {
	switch {
	case len(e.Unpushed) > 0 && e.Uncommitted:
		return fmt.Sprintf("the worktree has %d unpushed commits and uncommitted changes: confirm to delete them", len(e.Unpushed))
	case e.Uncommitted:
		return "the worktree has uncommitted changes: confirm to delete them"
	default:
		return fmt.Sprintf("the branch has %d unpushed commits: confirm to delete them", len(e.Unpushed))
	}
}

// Is makes errors.Is(err, ErrNeedsConfirmation) true.
func (e *NeedsConfirmationError) Is(target error) bool { return target == ErrNeedsConfirmation }

// CleanupRequest asks to clean up an ant.
type CleanupRequest struct {
	Clone    ports.Clone
	Branch   string
	Worktree string
	PR       domain.PRState
	// PRHead is the PR's last head commit, empty when there is no PR.
	// Commits up to it count as pushed even when the provider deleted the
	// branch after a squash or rebase merge.
	PRHead string
	// Abandoned, Requested and Confirmed are as in domain.CleanupInput.
	Abandoned bool
	Requested bool
	Confirmed bool
}

// Cleanup removes an ant's worktree and local branch, and its remote
// branch when the PR is merged and the provider kept it (spec
// branching-policy, "Worktree cleanup"). A cleanup that is not due does
// nothing. Unpushed commits or uncommitted changes without confirmation
// return a *NeedsConfirmationError and remove nothing. It returns what it
// did.
func (w *Workspace) Cleanup(ctx context.Context, req CleanupRequest) (domain.CleanupPlan, error) {
	in := domain.CleanupInput{PR: req.PR, Abandoned: req.Abandoned, Requested: req.Requested, Confirmed: req.Confirmed}
	if !in.Due() {
		return domain.CleanupPlan{}, nil
	}
	if err := w.git.Fetch(ctx, req.Clone); err != nil {
		return domain.CleanupPlan{}, fmt.Errorf("fetching: %w", err)
	}
	status, err := w.git.WorktreeStatus(ctx, req.Clone, req.Worktree)
	switch {
	case err == nil:
		in.Worktree, in.Uncommitted = true, status.Dirty
	case !errors.Is(err, ports.ErrNotFound):
		return domain.CleanupPlan{}, err
	}
	var unpushed []ports.Commit
	info, err := w.git.Branch(ctx, req.Clone, req.Branch, "")
	switch {
	case err == nil:
		in.RemoteBranch = info.Remote
		if info.Local {
			if unpushed, err = w.git.Unpushed(ctx, req.Clone, req.Branch, req.PRHead); err != nil {
				return domain.CleanupPlan{}, err
			}
			in.UnpushedCommits = len(unpushed)
		}
	case !errors.Is(err, ports.ErrNotFound):
		return domain.CleanupPlan{}, err
	}

	plan := domain.PlanCleanup(in)
	if plan.NeedsConfirmation {
		return plan, &NeedsConfirmationError{Unpushed: unpushed, Uncommitted: in.Uncommitted}
	}
	if plan.RemoveWorktree {
		if err := w.git.RemoveWorktree(ctx, req.Clone, req.Worktree, plan.Force); err != nil {
			return domain.CleanupPlan{}, fmt.Errorf("removing worktree %s: %w", req.Worktree, err)
		}
	}
	if plan.DeleteLocalBranch {
		if err := w.git.DeleteBranch(ctx, req.Clone, req.Branch, false); err != nil && !errors.Is(err, ports.ErrNotFound) {
			return domain.CleanupPlan{}, fmt.Errorf("deleting branch %s: %w", req.Branch, err)
		}
	}
	if plan.DeleteRemoteBranch {
		if err := w.git.DeleteBranch(ctx, req.Clone, req.Branch, true); err != nil && !errors.Is(err, ports.ErrNotFound) {
			return domain.CleanupPlan{}, fmt.Errorf("deleting remote branch %s: %w", req.Branch, err)
		}
	}
	return plan, nil
}
