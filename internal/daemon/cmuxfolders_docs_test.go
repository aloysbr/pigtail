package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// D2 (sub_x000000000000041) lifts « never reorders » for the folder
// arrangement: the docs say so, and say the flag acts only with the loop on.
func TestDocsCmuxFolders(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, f := range []string{"docs/wire-protocol.md", "docs/architecture.md"} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		if len(b) == 0 {
			t.Fatalf("%s read empty", f)
		}
		if strings.Contains(strings.ToLower(string(b)), "never reorders") {
			t.Errorf("%s still says « never reorders » (D2 lifts it for folders)", f)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "docs/architecture.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "`--cmux-folders` acts only with the loop on") {
		t.Error("architecture.md lacks « `--cmux-folders` acts only with the loop on »")
	}
}
