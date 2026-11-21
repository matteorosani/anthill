package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// flagName finds the flag of each line of cobra's flag usages.
var flagName = regexp.MustCompile(`(?m)^\s+(?:-\w, )?--([\w-]+)`)

// docCommand is one "### `anthill ...`" section of docs/cli.md, with
// the flags its list items name.
type docCommand struct {
	path  []string
	flags []string
}

// readCLIDoc parses the command sections of docs/cli.md: the words of
// each heading before its first argument, and the "- `--flag ...`" items
// that follow it.
func readCLIDoc(t *testing.T) []docCommand {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "cli.md"))
	if err != nil {
		t.Fatal(err)
	}
	var out []docCommand
	inSection := false
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if heading, ok := strings.CutPrefix(line, "### `anthill"); ok {
			var path []string
			for _, word := range strings.Fields(strings.TrimSuffix(heading, "`")) {
				if strings.ContainsAny(word[:1], "<[-") {
					break
				}
				path = append(path, word)
			}
			out = append(out, docCommand{path: path})
			inSection = true
			continue
		}
		if strings.HasPrefix(line, "#") {
			inSection = false
			continue
		}
		if flag, ok := strings.CutPrefix(line, "- `--"); ok && inSection {
			end := strings.IndexAny(flag, " `")
			if end < 0 {
				end = len(flag)
			}
			out[len(out)-1].flags = append(out[len(out)-1].flags, flag[:end])
		}
	}
	return out
}

// Task 17.5: every command documented in docs/cli.md exists in the cobra
// command tree with the flags it documents, and every command a queen
// runs is documented.
func TestCLIDocMatchesTheCommands(t *testing.T) {
	root := newRootCommand(newTestEnv(t).environment)
	docs := readCLIDoc(t)
	if len(docs) < 20 {
		t.Fatalf("found %d documented commands", len(docs))
	}
	documented := map[string]bool{}
	for _, d := range docs {
		cmd, rest, err := root.Find(d.path)
		if err != nil || len(rest) > 0 || cmd == root {
			t.Errorf("documented command anthill %s is not a command", strings.Join(d.path, " "))
			continue
		}
		documented[cmd.CommandPath()] = true
		for _, f := range d.flags {
			if cmd.Flags().Lookup(f) == nil {
				t.Errorf("%s documents --%s, which it does not have", cmd.CommandPath(), f)
			}
		}
		// FlagUsages lists the visible flags, one per line.
		var missing []string
		for _, m := range flagName.FindAllStringSubmatch(cmd.Flags().FlagUsages(), -1) {
			if m[1] != "help" && !slices.Contains(d.flags, m[1]) {
				missing = append(missing, m[1])
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s does not document %v", cmd.CommandPath(), missing)
		}
	}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			if sub.Runnable() && !sub.HasSubCommands() && !documented[sub.CommandPath()] {
				t.Errorf("%s is not documented in docs/cli.md", sub.CommandPath())
			}
			walk(sub)
		}
	}
	walk(root)
}
