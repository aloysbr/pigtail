package envlocal

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The bug this package was created for was not one bad parser — it was three
// hand-rolled ones drifting apart, each written by someone who only needed
// "the line that starts with PORT=". This guard is what makes the fourth one
// fail CI instead of shipping.
//
// It is a test rather than a golangci-lint rule because golangci-lint cannot
// express it. forbidigo — the natural candidate — matches patterns against the
// NAME of the called function only, never its arguments (verified: a pattern
// including the argument list matches nothing). A name-only rule on
// strings.HasPrefix/TrimPrefix would flag 43 call sites in this repo, all but
// a handful legitimate and unrelated (git porcelain prefixes, branch slugs,
// path handling). Muting those to satisfy a linter would be strictly worse
// than the bug. This check, working on the AST, says the precise thing:
// a file that READS .env.local must not also split lines on "=" itself.

// splitters are the functions a hand-rolled .env.local parser reaches for.
// Package-qualified so a local helper of the same name is not caught.
var splitters = map[string]bool{
	"strings.HasPrefix": true, "bytes.HasPrefix": true,
	"strings.TrimPrefix": true, "bytes.TrimPrefix": true,
	"strings.TrimSuffix": true, "bytes.TrimSuffix": true,
	"strings.Cut": true, "bytes.Cut": true,
	"strings.CutPrefix": true, "bytes.CutPrefix": true,
	"strings.Split": true, "bytes.Split": true,
	"strings.SplitN": true, "bytes.SplitN": true,
	"strings.Index": true, "bytes.Index": true,
	"strings.IndexByte": true, "bytes.IndexByte": true,
	"strings.IndexAny": true, "bytes.IndexAny": true,
	"strings.LastIndex": true, "bytes.LastIndex": true,
	"strings.Contains": true, "bytes.Contains": true,
}

// manualParse is one place where a file splits text on "=" by hand.
type manualParse struct {
	line int
	call string
}

// findEqualsSplitting reports every call to a splitter whose arguments carry
// an "=" — `strings.HasPrefix(line, envVar+"=")`, `strings.Cut(s, "=")`,
// `strings.IndexByte(body, '=')`. The literal may sit inside a larger
// expression, so each argument is searched whole.
func findEqualsSplitting(filename string, src []byte) ([]manualParse, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, err
	}
	var found []manualParse
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || !splitters[pkg.Name+"."+sel.Sel.Name] {
			return true
		}
		for _, arg := range call.Args {
			if containsEqualsLiteral(arg) {
				found = append(found, manualParse{
					line: fset.Position(call.Pos()).Line,
					call: pkg.Name + "." + sel.Sel.Name,
				})
				break
			}
		}
		return true
	})
	return found, nil
}

// containsEqualsLiteral reports whether an expression holds an "=" string or
// '=' rune literal anywhere inside it.
func containsEqualsLiteral(e ast.Expr) bool {
	hit := false
	ast.Inspect(e, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok {
			return true
		}
		switch lit.Kind {
		case token.STRING:
			if s, err := strconv.Unquote(lit.Value); err == nil && strings.Contains(s, "=") {
				hit = true
			}
		case token.CHAR:
			if lit.Value == `'='` {
				hit = true
			}
		}
		return !hit
	})
	return hit
}

// TestFindEqualsSplitting pins the detector against the three parsers this
// package replaced, and against the code it must leave alone. Every "flagged"
// source below is the real pre-fix code, trimmed.
func TestFindEqualsSplitting(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		flagged bool
	}{
		{
			name: "the old ReadPort", // internal/devserver/devserver.go
			src: `package p
import "strings"
func f(line, envVar string) {
	if strings.HasPrefix(line, envVar+"=") {
		_ = strings.TrimPrefix(line, envVar+"=")
	}
}`,
			flagged: true,
		},
		{
			name: "the old envLocalOwner", // internal/devserver/owner.go
			src: `package p
import "strings"
func f(trimmed string) {
	rawName, rawVal, found := strings.Cut(trimmed, "=")
	_, _, _ = rawName, rawVal, found
}`,
			flagged: true,
		},
		{
			name: "the old mergeEnvLocal", // internal/backend/git/git.go
			src: `package p
import "strings"
func f(trimmed string) {
	eq := strings.IndexByte(trimmed, '=')
	_ = eq
}`,
			flagged: true,
		},
		{
			name: "a named prefix constant is still a prefix", // the same bug, spelled differently
			src: `package p
import "strings"
const portPrefix = "PORT="
func f(line string) bool { return strings.HasPrefix(line, portPrefix) }`,
			flagged: false, // known blind spot, see TestNoManualEnvLocalParsing's comment
		},
		{
			name: "splitting on something else entirely", // git.go's real, unrelated calls
			src: `package p
import "strings"
func f(line, base, s string) {
	_ = strings.HasPrefix(line, "??")
	_ = strings.IndexByte(base, '/')
	_ = strings.IndexByte(s, '\n')
}`,
			flagged: false,
		},
		{
			name: "delegating to this package",
			src: `package p
import "github.com/aloysbr/pigtail/internal/envlocal"
func f(line string) { _, _, _ = envlocal.ParseAssignment(line) }`,
			flagged: false,
		},
		{
			name: "a local helper that merely shares a name",
			src: `package p
type t struct{}
func (x t) Cut(s, sep string) string { return s + sep }
func f(x t) { _ = x.Cut("a", "=") }`,
			flagged: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := findEqualsSplitting("x.go", []byte(tc.src))
			if err != nil {
				t.Fatal(err)
			}
			if (len(got) > 0) != tc.flagged {
				t.Fatalf("findEqualsSplitting found %v, want flagged=%v", got, tc.flagged)
			}
		})
	}
}

// TestNoManualEnvLocalParsing sweeps the repository: any file that reads or
// writes .env.local and splits lines on "=" itself is re-implementing this
// package. The two conditions together are what make the check precise — the
// CLI's `k=v` flag parsing (cmd/pigtail/theme.go, subject.go) splits on "="
// but has nothing to do with .env.local, and internal/backend/git splits on
// '/' and '\n' for reasons of its own.
//
// Known blind spot, accepted: a prefix hidden behind a named constant
// (`const portPrefix = "PORT="`) is invisible to it. Closing that needs type
// resolution for a case nobody has written yet; the check aims at the shape
// the three real parsers had.
func TestNoManualEnvLocalParsing(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root not where expected: %v", err)
	}
	// This package is where splitting a line on "=" is the whole job.
	exempt := filepath.Join(root, "internal", "envlocal")

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "node_modules", "testdata":
				return fs.SkipDir
			}
			if path == exempt {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			// Test files are out of scope, and deliberately so rather than by
			// omission: they assert ON .env.local content, so
			// `strings.Contains(got, "PORT=3010")` is the assertion itself, not
			// a parser. Fifteen such lines exist today across
			// internal/backend/git and internal/devserver; flagging them would
			// mean rewriting correct, unrelated test code to satisfy a check.
			// The rule is about production readers of the file.
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), ".env.local") {
			return nil
		}
		found, err := findEqualsSplitting(path, src)
		if err != nil {
			return err
		}
		for _, f := range found {
			t.Errorf("%s:%d: %s splits on \"=\" in a file that handles .env.local — "+
				"use internal/envlocal (ParseAssignment / Lookup / AssignPrefix) instead of "+
				"parsing the file again here. Three hand-rolled readers of this file drifted "+
				"apart once already; that is what this check exists to prevent.",
				path, f.line, f.call)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
