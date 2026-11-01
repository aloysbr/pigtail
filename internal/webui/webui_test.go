package webui

import (
	"io/fs"
	"testing"
)

func TestEmbeddedIndexPresent(t *testing.T) {
	b, err := fs.ReadFile(FS(), "index.html")
	if err != nil {
		t.Fatalf("index.html not embedded: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("embedded index.html is empty")
	}
}

// FS is rooted in dist: index.html sits at its top, no "dist/" prefix.
func TestAssetsSubFSRooted(t *testing.T) {
	if _, err := fs.Stat(FS(), "dist"); err == nil {
		t.Fatal("FS() exposes a dist/ directory: it must be rooted inside dist")
	}
	if _, err := fs.Stat(FS(), "index.html"); err != nil {
		t.Fatalf("index.html not at the root of FS(): %v", err)
	}
}
