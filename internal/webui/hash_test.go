package webui

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The algorithm is frozen: sorted byte-wise relative paths, one
// "path\0sha256hex\n" line each, sha256 hex of the whole. The expected value
// below was computed outside Go (Python hashlib) from the same tree.
func TestSourcesHashPinned(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"web/package.json":      "{}\n",
		"web/tsconfig.app.json": "{\"a\":1}\n",
		"web/src/main.ts":       "x\n",
		"web/src/B.ts":          "b\n",
		"web/src/theme/t.css":   ":root{}\n",
		"web/README.md":         "not a source\n",
		"web/eslint.config.js":  "not listed\n",
		"web/src/.DS_Store":     "finder noise\n",
		"web/node_modules/x.js": "dep\n",
		"web/dist/index.html":   "built\n",
		"other/package.json":    "{}\n",
	})
	got, err := SourcesHash(root)
	if err != nil {
		t.Fatal(err)
	}
	const want = "5c9a8e84955acd6390912f1629fe4d2c88c51198acc346cb272aecc2d3ebe262"
	if got != want {
		t.Fatalf("SourcesHash = %s, want %s", got, want)
	}
}

func TestSourcesHashNoneWithoutSources(t *testing.T) {
	got, err := SourcesHash(t.TempDir())
	if err != nil || got != "none" {
		t.Fatalf("SourcesHash of an empty tree = %q, %v; want none", got, err)
	}
}

func TestSourcesHashIgnoresNodeModules(t *testing.T) {
	base := map[string]string{"web/package.json": "{}\n", "web/src/a.ts": "a\n"}
	a, b := t.TempDir(), t.TempDir()
	writeTree(t, a, base)
	writeTree(t, b, base)
	writeTree(t, b, map[string]string{
		"web/node_modules/dep/index.js": "dep\n",
		"web/src/node_modules/x/y.go":   "package y\n",
		"web/src/dist/z.js":             "z\n",
		"web/src/.vite/cache.json":      "{}\n",
		"web/dist/assets/app.js":        "built\n",
	})
	ha, err := SourcesHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := SourcesHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("node_modules, dist or caches changed the hash: %s vs %s", ha, hb)
	}
	writeTree(t, b, map[string]string{"web/src/a.ts": "changed\n"})
	if hc, _ := SourcesHash(b); hc == ha {
		t.Fatal("a source change left the hash unchanged (positive control)")
	}
}

// The embedded dist carries the hash of the sources it was built from.
// Lot 1: no web/ yet, the placeholder carries "none".
func TestDistStampMatchesSources(t *testing.T) {
	stamp, err := fs.ReadFile(FS(), StampFile)
	if err != nil {
		t.Fatalf("the embedded dist has no %s: %v", StampFile, err)
	}
	want, err := SourcesHash(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(stamp)); got != want {
		t.Fatalf("internal/webui/dist/%s = %s, sources hash %s: run `make web` then commit internal/webui/dist", StampFile, got, want)
	}
	if want == "none" {
		if _, err := os.Stat(filepath.Join("..", "..", "web", "package.json")); err == nil {
			t.Fatal("web/package.json exists but the hash is none: the source list is not read")
		}
	}
}

func TestGoModIgnoresWeb(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "ignore ./web" {
			return
		}
	}
	t.Fatal("go.mod lacks `ignore ./web`: ./... would walk web/node_modules")
}
