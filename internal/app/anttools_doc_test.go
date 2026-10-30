package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

// docTool is one tool section of docs/ant-tools.md.
type docTool struct {
	name  string
	roles []string
	// parts are the list items the section starts, such as "Refusals".
	parts []string
}

// docParts are the parts every tool section must describe.
var docParts = []string{"Arguments", "Result", "Refusals"}

// readToolDoc parses the "### `name`" sections of docs/ant-tools.md and
// the "Roles:" line and the list items of each.
func readToolDoc(t *testing.T) []docTool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "ant-tools.md"))
	if err != nil {
		t.Fatal(err)
	}
	var tools []docTool
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if name, ok := strings.CutPrefix(line, "### `"); ok {
			tools = append(tools, docTool{name: strings.TrimSuffix(name, "`")})
			continue
		}
		if len(tools) > 0 && strings.HasPrefix(line, "- ") {
			if part, _, ok := strings.Cut(strings.TrimPrefix(line, "- "), ":"); ok {
				tools[len(tools)-1].parts = append(tools[len(tools)-1].parts, part)
			}
		}
		if strings.HasPrefix(line, "### ") || strings.HasPrefix(line, "## ") {
			if len(tools) > 0 && !strings.HasPrefix(line, "### `") {
				// A section that is not a tool ends the tool's section.
				tools = append(tools, docTool{})
			}
			continue
		}
		if roles, ok := strings.CutPrefix(line, "Roles: "); ok && len(tools) > 0 {
			roles, _, _ = strings.Cut(roles, ".")
			tools[len(tools)-1].roles = strings.Split(roles, ", ")
		}
	}
	return slices.DeleteFunc(tools, func(d docTool) bool { return d.name == "" })
}

// Every tool in the code is documented, in the order of domain.AntTools,
// with the roles that have it and its arguments, result and refusals, and
// the document describes no other tool. Adding a tool without
// documenting it fails here.
func TestAntToolsDocMatchesCode(t *testing.T) {
	doc := readToolDoc(t)
	var names []string
	for _, d := range doc {
		names = append(names, d.name)
	}
	if !slices.Equal(names, domain.AntTools) {
		t.Fatalf("documented tools = %v, want %v", names, domain.AntTools)
	}
	for _, d := range doc {
		if _, ok := toolCatalogue[d.name]; !ok {
			t.Errorf("%s is documented but not in the catalogue", d.name)
		}
		var want []string
		for _, r := range domain.AgentRoles {
			if slices.Contains(domain.ToolsFor(r), d.name) {
				want = append(want, r.Words())
			}
		}
		if !slices.Equal(d.roles, want) {
			t.Errorf("%s: documented roles = %q, want %q", d.name, d.roles, want)
		}
		for _, part := range docParts {
			if !slices.Contains(d.parts, part) {
				t.Errorf("%s: the section has no %q item", d.name, part)
			}
		}
	}
}
