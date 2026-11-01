package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sub_x000000000000030: the docs name the sort switch.
func TestDocsCmuxFolderSort(t *testing.T) {
	for _, f := range []string{"docs/architecture.md", "docs/wire-protocol.md"} {
		b, err := os.ReadFile(filepath.Join("..", "..", f))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "`--cmux-folder-sort=false`") {
			t.Errorf("%s does not name `--cmux-folder-sort=false`", f)
		}
	}
}

// D39 of sub_x000000000000014: with the sort off, the docs say a tab moves
// alone (`reorder-workspace`, cmux probe P26) and drop the former whole-reorder
// sentences. Blanks are normalised: the sentences wrap across lines.
func TestDocsNoSortMovesOneTab(t *testing.T) {
	gone := []string{
		"no reorder but the bands'",
		"no reorder but the one that brings a workspace pigtail just added",
		"and reorders only them there",
	}
	for _, f := range []string{"docs/architecture.md", "docs/wire-protocol.md"} {
		b, err := os.ReadFile(filepath.Join("..", "..", f))
		if err != nil {
			t.Fatal(err)
		}
		s := strings.Join(strings.Fields(string(b)), " ")
		for _, want := range []string{"`reorder-workspace`", "P26"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s does not name %s", f, want)
			}
		}
		for _, old := range gone {
			if strings.Contains(s, old) {
				t.Errorf("%s still says %q", f, old)
			}
		}
	}
}
