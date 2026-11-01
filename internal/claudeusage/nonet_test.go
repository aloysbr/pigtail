package claudeusage

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// D1: the package never calls the network — no net import, ever.
func TestPackageImportsNoNetwork(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		seen++
		for _, im := range af.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if p == "net" || strings.HasPrefix(p, "net/") || p == "os/exec" {
				t.Errorf("%s imports %s", f, p)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no source file read — the guard looks at nothing")
	}
}
