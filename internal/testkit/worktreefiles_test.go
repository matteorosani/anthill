package testkit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

func TestWorktreeFiles(t *testing.T) {
	ctx := context.Background()
	w := testkit.NewWorktreeFiles()
	w.Put("/wt", "docs/AGENTS.md", "0123456789")
	w.PutOutside("/wt", "LINK.md")

	data, cut, err := w.ReadWorktreeFile(ctx, "/wt", "docs/AGENTS.md", 4)
	if err != nil || !cut || string(data) != "0123" {
		t.Fatalf("got %q, %v, %v", data, cut, err)
	}
	data, cut, err = w.ReadWorktreeFile(ctx, "/wt", "docs/./AGENTS.md", 64)
	if err != nil || cut || string(data) != "0123456789" {
		t.Fatalf("got %q, %v, %v", data, cut, err)
	}
	if _, _, err := w.ReadWorktreeFile(ctx, "/other", "docs/AGENTS.md", 64); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("other worktree: want ErrNotFound, got %v", err)
	}
	for _, p := range []string{"LINK.md", "../x", "/etc/passwd"} {
		if _, _, err := w.ReadWorktreeFile(ctx, "/wt", p, 64); !errors.Is(err, ports.ErrOutsideWorktree) {
			t.Fatalf("%s: want ErrOutsideWorktree, got %v", p, err)
		}
	}
	boom := errors.New("boom")
	w.Fail(boom)
	if _, _, err := w.ReadWorktreeFile(ctx, "/wt", "docs/AGENTS.md", 64); !errors.Is(err, boom) {
		t.Fatalf("want %v, got %v", boom, err)
	}
}
