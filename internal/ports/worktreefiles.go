package ports

import (
	"context"
	"errors"
)

// ErrOutsideWorktree is returned for a path, or a symbolic link on it,
// that leads outside the worktree.
var ErrOutsideWorktree = errors.New("path leads outside the worktree")

// WorktreeFiles reads files of an ant's worktree, such as the repository's
// agent instructions file for the brief (spec ant-execution, "Task
// brief").
type WorktreeFiles interface {
	// ReadWorktreeFile reads the regular file at path, a slash-separated
	// path relative to worktree, up to limit bytes. truncated reports
	// that the file is longer and only its first limit bytes are
	// returned. It returns ErrNotFound when there is no such file, and
	// ErrOutsideWorktree when path, or a symbolic link on it, leads
	// outside the worktree.
	ReadWorktreeFile(ctx context.Context, worktree, path string, limit int) (data []byte, truncated bool, err error)
}
