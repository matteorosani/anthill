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
}

// readToolDoc parses the "### `name`" sections of docs/ant-tools.md and
// the "Roles:" line of each.
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
		if roles, ok := strings.CutPrefix(line, "Roles: "); ok && len(tools) > 0 {
			roles, _, _ = strings.Cut(roles, ".")
			tools[len(tools)-1].roles = strings.Split(roles, ", ")
		}
	}
	return tools
}

// Every tool in the code is documented, in the order of domain.AntTools,
// with the roles that have it, and the document describes no other tool.
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
	}
}
