package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
)

// hooksDirName is the directory, inside a worktree's private git
// directory, that holds Anthill's hooks. git worktree remove deletes it
// with the worktree.
const hooksDirName = "anthill-hooks"

// The worktree settings the prepare-commit-msg hook reads.
const (
	configItem     = "anthill.item"
	configAgent    = "anthill.agent"
	configInstance = "anthill.instance"
)

// chainedHooks are the client-side hooks that, in an ant worktree, only
// call the repository's own hook of the same name, so that checks such
// as pre-commit and commit-msg still run. prepare-commit-msg calls it
// too, after adding the trailers. pre-push never does. Left out:
// reference-transaction, which would start a shell on every ref update,
// and the p4 and fsmonitor hooks, which git finds in other ways.
var chainedHooks = []string{
	"applypatch-msg",
	"pre-applypatch",
	"post-applypatch",
	"pre-commit",
	"pre-merge-commit",
	"commit-msg",
	"post-commit",
	"pre-rebase",
	"post-checkout",
	"post-merge",
	"post-rewrite",
	"post-index-change",
	"pre-auto-gc",
	"sendemail-validate",
}

// repoHooksFunc finds the repository's own hooks directory as git would
// without Anthill's worktree setting: core.hooksPath from the clone's
// configuration, the global or the system one, else the common .git/hooks.
// A relative core.hooksPath is relative to the top of the working tree,
// where git runs hooks, so husky's .husky works.
const repoHooksFunc = `repo_hooks() {
	dir=$(git config --file "$(git rev-parse --git-common-dir)/config" core.hooksPath) ||
		dir=$(git config --global core.hooksPath) ||
		dir=$(git config --system core.hooksPath) ||
		dir=
	case $dir in
	"") dir=$(git rev-parse --git-common-dir)/hooks ;;
	"~/"*) dir=$HOME/${dir#"~/"} ;;
	/* | [A-Za-z]:[\\/]*) ;;
	*) dir=$(git rev-parse --show-toplevel)/$dir ;;
	esac
	printf '%s\n' "$dir"
}
`

// chainScript returns the lines that run the repository's hook name with
// the same arguments and input, and pass on its exit code.
func chainScript(name string) string {
	return fmt.Sprintf(`hook=$(repo_hooks)/%s
if [ -f "$hook" ] && [ -x "$hook" ]; then
	exec "$hook" "$@"
fi
exit 0
`, name)
}

const scriptHeader = "#!/bin/sh\n# Installed by Anthill in an ant worktree. Do not edit.\n"

// hookScripts returns every hook Anthill installs, by name.
func hookScripts() map[string]string {
	scripts := map[string]string{}
	for _, name := range chainedHooks {
		scripts[name] = scriptHeader + repoHooksFunc + chainScript(name)
	}
	scripts["prepare-commit-msg"] = scriptHeader + repoHooksFunc + fmt.Sprintf(`git interpret-trailers --in-place --if-exists replace \
	--trailer "%s: $(git config %s)" \
	--trailer "%s: $(git config %s)" \
	--trailer "%s: $(git config %s)" \
	"$1" || exit
`, domain.TrailerItem, configItem, domain.TrailerAgent, configAgent, domain.TrailerInstance, configInstance) +
		chainScript("prepare-commit-msg")
	scripts["pre-push"] = scriptHeader + fmt.Sprintf("echo '%s' >&2\nexit 1\n", domain.MessageUsePushTool)
	return scripts
}

// writeHooks writes the hooks to dir with LF line endings, executable.
// On Windows git runs them through its bundled sh.
func writeHooks(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, script := range hookScripts() {
		if strings.Contains(script, "\r") {
			return fmt.Errorf("hook %s has a carriage return", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			return err
		}
	}
	return nil
}
