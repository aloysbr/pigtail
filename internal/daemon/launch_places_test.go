package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// cmuxRefs walks the daemon's non-test Go files and returns every file
// (slash path, relative to internal/daemon) that references cmux.<name>.
func cmuxRefs(t *testing.T, name string) []string {
	t.Helper()
	const cmuxPath = "github.com/aloysbr/pigtail/internal/cmux"
	fset := token.NewFileSet()
	var seen []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		local := ""
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) != cmuxPath {
				continue
			}
			local = "cmux"
			if imp.Name != nil {
				local = imp.Name.Name
			}
		}
		if local == "" {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != name {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == local {
				seen = append(seen, filepath.ToSlash(path))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 {
		t.Fatalf("no reference to cmux.%s found: the walk reads nothing (positive control)", name)
	}
	return seen
}

// TestNewWorkspaceOnlyInLaunch: the launcher (internal/daemon/launch) is the
// only daemon code that opens a cmux tab (D1 of sub_x000000000000015).
func TestNewWorkspaceOnlyInLaunch(t *testing.T) {
	var offenders []string
	for _, p := range cmuxRefs(t, "NewWorkspace") {
		if filepath.Dir(p) != "launch" {
			offenders = append(offenders, p)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("cmux.NewWorkspace referenced outside internal/daemon/launch: %v", offenders)
	}
}

// TestRevealOnlyInRevealHandler: POST /peers/{session}/reveal
// (handlers/reveal.go) is the only daemon code that brings a cmux tab to the
// front (sub_x000000000000025, task 15; CLAUDE.md § cmux sidebar).
func TestRevealOnlyInRevealHandler(t *testing.T) {
	var offenders []string
	for _, p := range cmuxRefs(t, "Reveal") {
		if p != "handlers/reveal.go" {
			offenders = append(offenders, p)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("cmux.Reveal referenced outside internal/daemon/handlers/reveal.go: %v", offenders)
	}
}
