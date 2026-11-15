package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/matteorosani/anthill/internal/adapters/git"
	"github.com/matteorosani/anthill/internal/ports"
)

// symlink makes a symbolic link, or skips the test where the OS does not
// let the user make one, as on Windows without developer mode.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot make a symbolic link: %v", err)
	}
}

func TestReadWorktreeFile(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	worktree := filepath.Join(tmp, "worktree")
	writeFile(t, filepath.Join(worktree, "docs", "AGENTS.md"), "Run make test.\n")
	writeFile(t, filepath.Join(worktree, "BIG.md"), "0123456789")
	writeFile(t, filepath.Join(tmp, "secret.txt"), "outside")
	g := git.New(nil, tmp)

	t.Run("reads a file", func(t *testing.T) {
		data, cut, err := g.ReadWorktreeFile(ctx, worktree, "docs/AGENTS.md", 1024)
		if err != nil || cut || string(data) != "Run make test.\n" {
			t.Fatalf("got %q, %v, %v", data, cut, err)
		}
	})
	t.Run("cuts at the limit", func(t *testing.T) {
		data, cut, err := g.ReadWorktreeFile(ctx, worktree, "BIG.md", 4)
		if err != nil || !cut || string(data) != "0123" {
			t.Fatalf("got %q, %v, %v", data, cut, err)
		}
		data, cut, err = g.ReadWorktreeFile(ctx, worktree, "BIG.md", 10)
		if err != nil || cut || string(data) != "0123456789" {
			t.Fatalf("at exactly the limit: got %q, %v, %v", data, cut, err)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if _, _, err := g.ReadWorktreeFile(ctx, worktree, "NONE.md", 1024); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})
	t.Run("path leading outside", func(t *testing.T) {
		for _, p := range []string{"../secret.txt", "docs/../../secret.txt", "/etc/passwd"} {
			if _, _, err := g.ReadWorktreeFile(ctx, worktree, p, 1024); !errors.Is(err, ports.ErrOutsideWorktree) {
				t.Errorf("%s: want ErrOutsideWorktree, got %v", p, err)
			}
		}
	})
	t.Run("directory", func(t *testing.T) {
		_, _, err := g.ReadWorktreeFile(ctx, worktree, "docs", 1024)
		if err == nil || errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("want an error for a directory, got %v", err)
		}
	})
}

func TestReadWorktreeFileRefusesSymlinksOutside(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	worktree := filepath.Join(tmp, "worktree")
	writeFile(t, filepath.Join(worktree, "real.md"), "inside")
	writeFile(t, filepath.Join(tmp, "secret.txt"), "outside")
	symlink(t, filepath.Join(tmp, "secret.txt"), filepath.Join(worktree, "AGENTS.md"))
	symlink(t, tmp, filepath.Join(worktree, "up"))
	symlink(t, filepath.Join(tmp, "gone.txt"), filepath.Join(worktree, "dangling.md"))
	symlink(t, "real.md", filepath.Join(worktree, "alias.md"))
	g := git.New(nil, tmp)

	for _, p := range []string{"AGENTS.md", "up/secret.txt", "dangling.md"} {
		if _, _, err := g.ReadWorktreeFile(ctx, worktree, p, 1024); !errors.Is(err, ports.ErrOutsideWorktree) {
			t.Errorf("%s: want ErrOutsideWorktree, got %v", p, err)
		}
	}
	data, _, err := g.ReadWorktreeFile(ctx, worktree, "alias.md", 1024)
	if err != nil || string(data) != "inside" {
		t.Fatalf("a link inside the worktree: got %q, %v", data, err)
	}
}
