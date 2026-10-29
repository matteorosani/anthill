package domain_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

// docRow is one row of the settings table in docs/configuration.md.
type docRow struct {
	key, layers, def string
}

// readSettingsTable parses the Markdown table between the settings markers.
func readSettingsTable(t *testing.T) []docRow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	_, rest, ok := strings.Cut(text, "<!-- settings:begin -->\n")
	table, _, ok2 := strings.Cut(rest, "<!-- settings:end -->")
	if !ok || !ok2 {
		t.Fatal("settings markers not found")
	}
	lines := strings.Split(strings.TrimSpace(table), "\n")
	if len(lines) < 3 || lines[0] != "| Key | Layers | Default | Description |" {
		t.Fatalf("unexpected table header: %q", lines[0])
	}
	var rows []docRow
	for _, line := range lines[2:] {
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 4 {
			t.Fatalf("row %q has %d cells, want 4", line, len(cells))
		}
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		rows = append(rows, docRow{key: strings.Trim(cells[0], "`"), layers: cells[1], def: cells[2]})
	}
	return rows
}

// The settings table in docs/configuration.md lists every setting, in the
// order of AllSettings, with its layers and the default of the "empty
// configuration" test.
func TestConfigurationDocMatchesDefaults(t *testing.T) {
	rows := readSettingsTable(t)
	settings := domain.AllSettings()
	var docKeys, keys []string
	for _, r := range rows {
		docKeys = append(docKeys, r.key)
	}
	for _, s := range settings {
		keys = append(keys, s.Key)
	}
	if !slices.Equal(docKeys, keys) {
		t.Fatalf("documented keys %v\nwant %v", docKeys, keys)
	}
	for i, s := range settings {
		row := rows[i]
		var layers []string
		for _, l := range s.Layers {
			layers = append(layers, string(l))
		}
		if want := strings.Join(layers, ", "); row.layers != want {
			t.Errorf("%s: documented layers %q, want %q", s.Key, row.layers, want)
		}
		if want := domain.DefaultText(s.Key); row.def != want {
			t.Errorf("%s: documented default %q, want %q", s.Key, row.def, want)
		}
		if want, ok := emptyConfigurationDefaults[s.Key]; ok && s.Key != domain.KeyReviewRunner && row.def != want {
			t.Errorf("%s: documented default %q, but the empty configuration gives %q", s.Key, row.def, want)
		}
	}
}
