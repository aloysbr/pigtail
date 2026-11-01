package testlint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A test that creates its temporary directory with a bare os.MkdirTemp leaves
// it behind when the test is killed, and cannot remove a read-only tree (a Go
// module cache): the incident of 2026-10-31 filled the disk this way. Every
// such directory goes through testutil.MkdirTemp (prefix pt-, cleaned, the
// failure reported), which the daemon's tmp sweeper also recognises.

// findRawMkdirTemp reports the line of every os.MkdirTemp call (aliased
// import included) in src. It reads the AST, so a comment or a string never
// matches.
func findRawMkdirTemp(filename string, src []byte) ([]int, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, err
	}
	osNames := map[string]bool{}
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "os" {
			name := "os"
			if imp.Name != nil {
				name = imp.Name.Name
			}
			if name != "_" && name != "." {
				osNames[name] = true
			}
		}
	}
	if len(osNames) == 0 {
		return nil, nil
	}
	var lines []int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "MkdirTemp" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && osNames[id.Name] {
			lines = append(lines, fset.Position(call.Pos()).Line)
		}
		return true
	})
	return lines, nil
}

// TestFindRawMkdirTemp pins the detector on testdata/rawmkdirtemp (positive
// control: a fixture holding os.MkdirTemp is detected; a comment is not).
func TestFindRawMkdirTemp(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "rawmkdirtemp", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixture: %v", err)
	}
	flagged := 0
	for _, p := range files {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := findRawMkdirTemp(p, src)
		if err != nil {
			t.Fatal(err)
		}
		var want []int
		for i, l := range strings.Split(string(src), "\n") {
			if strings.Contains(l, "// want rawmkdirtemp") {
				want = append(want, i+1)
			}
		}
		flagged += len(want)
		if len(got) != len(want) {
			t.Errorf("%s: got lines %v, want %v", p, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%s: got lines %v, want %v", p, got, want)
			}
		}
	}
	if flagged == 0 {
		t.Fatal("the fixture flags nothing: the control proves nothing")
	}
}

// TestNoRawMkdirTempInTests refuses os.MkdirTemp in every _test.go of the
// module (spikes/ included, testdata excluded) outside internal/testutil.
func TestNoRawMkdirTempInTests(t *testing.T) {
	read := 0
	var bad []string
	err := filepath.WalkDir(moduleRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(moduleRoot, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch {
			case rel == ".":
			case d.Name() == "testdata", d.Name() == "node_modules", strings.HasPrefix(d.Name(), "."),
				rel == "web", rel == "macapp", rel == "internal/testutil":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		read++
		lines, err := findRawMkdirTemp(p, src)
		if err != nil {
			return err
		}
		for _, l := range lines {
			bad = append(bad, rel+":"+strconv.Itoa(l))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if read <= 500 {
		t.Fatalf("read %d _test.go files, want more than 500: the walk read nothing", read)
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("%s: raw os.MkdirTemp in a test — use testutil.MkdirTemp (cleans read-only trees, reports a failure, pt- prefix the tmp sweeper knows)", b)
	}
}
