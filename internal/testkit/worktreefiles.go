package testkit

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/matteorosani/anthill/internal/ports"
)

// WorktreeFiles is an in-memory ports.WorktreeFiles. Add files with Put,
// and paths whose symbolic link leads outside the worktree with
// PutOutside.
type WorktreeFiles struct {
	mu      sync.Mutex
	files   map[string]string
	outside map[string]bool
	err     error
}

var _ ports.WorktreeFiles = (*WorktreeFiles)(nil)

// NewWorktreeFiles returns worktree files with no file.
func NewWorktreeFiles() *WorktreeFiles {
	return &WorktreeFiles{files: map[string]string{}, outside: map[string]bool{}}
}

func fileKey(worktree, p string) string {
	return worktree + "\x00" + path.Clean(p)
}

// Put stores a file of a worktree.
func (w *WorktreeFiles) Put(worktree, p, content string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.files[fileKey(worktree, p)] = content
}

// PutOutside makes p a symbolic link that leads outside the worktree.
func (w *WorktreeFiles) PutOutside(worktree, p string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.outside[fileKey(worktree, p)] = true
}

// Fail makes every read fail with err until Fail(nil).
func (w *WorktreeFiles) Fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.err = err
}

// ReadWorktreeFile returns the stored file, cut at limit bytes.
func (w *WorktreeFiles) ReadWorktreeFile(_ context.Context, worktree, p string, limit int) ([]byte, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return nil, false, w.err
	}
	clean := path.Clean(p)
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || w.outside[fileKey(worktree, p)] {
		return nil, false, fmt.Errorf("%s: %w", p, ports.ErrOutsideWorktree)
	}
	content, ok := w.files[fileKey(worktree, p)]
	if !ok {
		return nil, false, fmt.Errorf("%s: %w", p, ports.ErrNotFound)
	}
	if len(content) > limit {
		return []byte(content[:limit]), true, nil
	}
	return []byte(content), false, nil
}
