package heavycmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"
)

func TestRunCeiling(t *testing.T) {
	want := map[Class]time.Duration{
		ClassESLint:         30 * time.Minute,
		ClassJest:           30 * time.Minute,
		ClassBuild:          30 * time.Minute,
		ClassStorybookBuild: 30 * time.Minute,
		ClassTSC:            45 * time.Minute,
		ClassGoTest:         45 * time.Minute,
		ClassVitest:         60 * time.Minute,
		ClassVitestBrowser:  60 * time.Minute,
		ClassNx:             60 * time.Minute,
		ClassPrecommit:      60 * time.Minute,
		ClassGeneric:        60 * time.Minute,
		Class("inconnue"):   60 * time.Minute,
	}
	for c, d := range want {
		if got := RunCeiling(c); got != d {
			t.Errorf("RunCeiling(%q) = %s, want %s", c, got, d)
		}
	}
}

// TestRunCeilingCoversEveryClass reads the Class constants of class.go: a
// class added there must be placed in the ceiling table on purpose, never
// fall into generic unnoticed.
func TestRunCeilingCoversEveryClass(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "class.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var classes []string
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for _, name := range vs.Names {
			if strings.HasPrefix(name.Name, "Class") && len(vs.Values) == 1 {
				if lit, ok := vs.Values[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					classes = append(classes, strings.Trim(lit.Value, `"`))
				}
			}
		}
		return true
	})
	if len(classes) < 11 {
		t.Fatalf("found only %v in class.go — the reader is not looking at the right thing", classes)
	}
	for _, c := range classes {
		if _, ok := runCeilings[Class(c)]; !ok {
			t.Errorf("class %q has no entry in runCeilings", c)
		}
	}
}
