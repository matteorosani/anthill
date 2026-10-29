package domain

import (
	"path/filepath"
	"strings"
)

// AnthillHomeDir is the name of Anthill's directory in the user's home:
// <anthill-home> is ~/.anthill on every OS.
const AnthillHomeDir = ".anthill"

// DefaultWorktreeRootText is the default worktree root as the
// documentation shows it.
const DefaultWorktreeRootText = "~/" + AnthillHomeDir + "/worktrees"

// WorktreeRoot returns the directory that holds every worktree:
// configured, the worktree-root setting, or <home>/.anthill/worktrees
// when it is empty. home is the user's home directory.
func WorktreeRoot(configured, home string) string {
	if configured != "" {
		return filepath.Clean(configured)
	}
	return filepath.Join(home, AnthillHomeDir, "worktrees")
}

// WorktreePath returns where the worktree of branch lives:
// <root>/<provider>-<org>[-<project>]-<name>/<branch>, such as
// ~/.anthill/worktrees/gh-acme-web/ant/gh-12-fix-login. The slashes of
// the branch become directories (spec branching-policy, "Worktree
// location").
func WorktreePath(root string, repo Repository, branch string) string {
	parts := []string{string(repo.Provider)}
	for _, p := range []string{repo.Org, repo.Project, repo.Name} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return filepath.Join(root, strings.Join(parts, "-"), filepath.FromSlash(branch))
}
