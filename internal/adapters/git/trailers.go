package git

import (
	"context"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// SetTrailers writes the trailers to the worktree's own configuration,
// where the prepare-commit-msg hook reads them, as AddWorktree does.
func (g *Git) SetTrailers(ctx context.Context, c ports.Clone, path string, trailers domain.Trailers) error {
	if err := trailers.Validate(); err != nil {
		return err
	}
	if ok, err := g.worktreeRegistered(ctx, c, path); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("worktree %s: %w", path, ports.ErrNotFound)
	}
	for _, kv := range [][2]string{{configItem, trailers.Item}, {configAgent, trailers.Agent}, {configInstance, trailers.Instance}} {
		if _, err := g.run(ctx, path, "config", "--worktree", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}
