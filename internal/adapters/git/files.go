package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/matteorosani/anthill/internal/ports"
)

var _ ports.WorktreeFiles = (*Git)(nil)

// ReadWorktreeFile reads a file of a worktree through an os.Root, which
// refuses any path or symbolic link that leads outside the worktree.
func (g *Git) ReadWorktreeFile(_ context.Context, worktree, path string, limit int) ([]byte, bool, error) {
	if limit < 0 {
		return nil, false, fmt.Errorf("limit %d is negative", limit)
	}
	rel := filepath.FromSlash(path)
	if !filepath.IsLocal(rel) {
		return nil, false, fmt.Errorf("%s: %w", path, ports.ErrOutsideWorktree)
	}
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = root.Close() }()

	f, err := root.Open(rel)
	if err != nil {
		return nil, false, openError(worktree, path, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		// A directory, or a pipe that could block the read.
		return nil, false, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

// openError tells a missing file from one whose path escapes the
// worktree. os.Root does not export its escape error, so a path that
// still exists is resolved to check where it leads.
func openError(worktree, path string, err error) error {
	if resolved, rerr := filepath.EvalSymlinks(filepath.Join(worktree, filepath.FromSlash(path))); rerr == nil {
		if top, terr := filepath.EvalSymlinks(worktree); terr == nil && !within(top, resolved) {
			return fmt.Errorf("%s: %w", path, ports.ErrOutsideWorktree)
		}
	}
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: %w", path, ports.ErrNotFound)
	}
	if strings.Contains(err.Error(), "escapes") {
		return fmt.Errorf("%s: %w", path, ports.ErrOutsideWorktree)
	}
	return err
}

// within reports whether path is dir or inside it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && filepath.IsLocal(rel)
}
