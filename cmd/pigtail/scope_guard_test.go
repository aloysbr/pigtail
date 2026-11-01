package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestScopedCommandsDeclareAccess is the guard behind --cross-scope: a perso
// cell must always be able to opt into writing work, so every CLI write that
// reaches a scoped route has to carry the flag.
//
// Part 1 (source): every cobra.Command literal whose RunE calls
// newScopedClient carries Annotations: scopedWrite() or scopedRead(), and
// newScopedClient is called nowhere else — a new scoped command cannot land
// without deciding which it is.
//
// Part 2 (command tree): a write command declares --cross-scope, a read
// command does not, and no unannotated command declares it.
func TestScopedCommandsDeclareAccess(t *testing.T) {
	annotated := checkScopedSource(t)
	if annotated == 0 {
		t.Fatal("found no scoped command in the source — the guard is not looking at the right thing")
	}

	var writes, reads int
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		access := c.Annotations[scopeAccessKey]
		hasFlag := c.Flags().Lookup("cross-scope") != nil
		switch access {
		case "write":
			writes++
			if !hasFlag {
				t.Errorf("%q writes a scoped route but has no --cross-scope (addCrossScopeFlag + WithCrossScopeWrite)", c.CommandPath())
			}
		case "read":
			reads++
			if hasFlag {
				t.Errorf("%q is read-only but declares --cross-scope", c.CommandPath())
			}
		case "session-write":
			// A relay command acting as the session that may cross scopes
			// (msg send): it declares --cross-scope, its client is sessionClient
			// (part 1 and TestRelaySessionCallsUseSessionClient).
			if !hasFlag {
				t.Errorf("%q is a session write but has no --cross-scope", c.CommandPath())
			}
		case "":
			if hasFlag {
				t.Errorf("%q declares --cross-scope without a scopedWrite() annotation", c.CommandPath())
			}
		default:
			t.Errorf("%q: unknown %s %q", c.CommandPath(), scopeAccessKey, access)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRootCmd())
	if writes+reads != annotated {
		t.Errorf("command tree has %d annotated commands, source has %d — a scoped command is not registered on the root", writes+reads, annotated)
	}
}

// checkScopedSource runs part 1 over the package's non-test files and returns
// how many annotated scoped commands it found.
func checkScopedSource(t *testing.T) int {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	annotated := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		covered := map[*ast.CallExpr]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isCobraCommand(lit.Type) {
				return true
			}
			var calls []*ast.CallExpr
			var access string
			var runE ast.Node
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, _ := kv.Key.(*ast.Ident)
				if key == nil {
					continue
				}
				switch key.Name {
				case "RunE", "Run":
					runE = kv.Value
					calls = append(calls, scopedClientCalls(kv.Value)...)
				case "Annotations":
					if call, ok := kv.Value.(*ast.CallExpr); ok {
						if fn, ok := call.Fun.(*ast.Ident); ok && (fn.Name == "scopedWrite" || fn.Name == "scopedRead" || fn.Name == "sessionWrite") {
							access = fn.Name
						}
					}
				}
			}
			if access == "sessionWrite" {
				if len(calls) > 0 {
					t.Errorf("%s: a sessionWrite() command acts as the session: sessionClient, never newScopedClient", fset.Position(lit.Pos()))
				} else if !callsIdent(runE, "sessionClient") {
					t.Errorf("%s: a sessionWrite() command must call sessionClient in its RunE", fset.Position(lit.Pos()))
				}
				return true
			}
			if len(calls) == 0 {
				if access != "" {
					t.Errorf("%s: command annotated %s() but never calls newScopedClient", fset.Position(lit.Pos()), access)
				}
				return true
			}
			for _, c := range calls {
				covered[c] = true
			}
			if access == "" {
				t.Errorf("%s: command calls newScopedClient without Annotations: scopedWrite() or scopedRead() — decide whether it writes (then add --cross-scope)", fset.Position(lit.Pos()))
			} else {
				annotated++
			}
			return true
		})
		for _, c := range scopedClientCalls(file) {
			if !covered[c] {
				t.Errorf("%s: newScopedClient called outside a cobra.Command RunE literal — the guard cannot classify it; call it from the command's RunE", fset.Position(c.Pos()))
			}
		}
	}
	return annotated
}

// isCobraCommand reports whether typ is cobra.Command.
func isCobraCommand(typ ast.Expr) bool {
	sel, ok := typ.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Command" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "cobra"
}

// scopedClientCalls returns every newScopedClient() call under n.
func scopedClientCalls(n ast.Node) []*ast.CallExpr {
	var out []*ast.CallExpr
	ast.Inspect(n, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "newScopedClient" {
				out = append(out, call)
			}
		}
		return true
	})
	return out
}

// callsIdent reports whether n holds a call to the package function name.
func callsIdent(n ast.Node, name string) bool {
	found := false
	if n == nil {
		return false
	}
	ast.Inspect(n, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == name {
				found = true
			}
		}
		return !found
	})
	return found
}
