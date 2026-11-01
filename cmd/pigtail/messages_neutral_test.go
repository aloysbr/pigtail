package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoClaudeSyncInMessages: no user-facing string of cmd/ or internal/
// sends the user to a private launcher or sync tool (a config file, an update
// command): an open-source user has none of it. String literals may not.
func TestNoClaudeSyncInMessages(t *testing.T) {
	t.Parallel()
	banned := []string{"config-sync", "profiles.json", "update methods"}
	fset := token.NewFileSet()
	files := 0
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			files++
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					v = lit.Value
				}
				for _, b := range banned {
					if strings.Contains(v, b) {
						t.Errorf("%s: a message names %q — point to docs/CONFIG.md § claude-cells.yaml instead", fset.Position(lit.Pos()), b)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files < 100 { // positive control: the walk read the tree
		t.Fatalf("parsed only %d files", files)
	}
}
