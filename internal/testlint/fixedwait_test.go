package testlint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A test that sleeps a fixed time and then asserts is betting that the
// machine is fast enough: under -race and a loaded Mac it loses, and the
// push goes red at random. Wait for the state (testutil.Eventually), a
// channel or an event instead. This guard refuses the shape in every
// _test.go of the module.
//
// It is a syntactic heuristic, and accepted as one: a fixed wait hidden in
// a helper of one's own passes. It aims at the shape the inventoried flakes
// had.

// moduleRoot is the module root: internal/testlint is two levels down.
const moduleRoot = "../.."

// assertions are the testing.TB methods that fail a test.
var assertions = map[string]bool{
	"Fatal": true, "Fatalf": true, "Error": true, "Errorf": true, "FailNow": true, "Fail": true,
}

// fixedWait is one time.Sleep followed by an assertion in the same block.
type fixedWait struct {
	Test string // the top-level test function holding it
	Line int
}

// findFixedWaits reports every time.Sleep (aliased import included) inside
// a top-level Test function whose next statement in the same block is an if
// whose body (or else) calls an assertion, or a direct assertion call.
// Ignored: a Sleep in the body of a for or range loop (bounded polling), in
// a function started by go, in a function that is not a test, and in the
// function given to synctest.Test (virtual time). A closure inside a loop
// (t.Run in a table loop) is scanned like any other code.
func findFixedWaits(filename string, src []byte) ([]fixedWait, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, err
	}
	timeName := ""
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "time" {
			timeName = "time"
			if imp.Name != nil {
				timeName = imp.Name.Name
			}
		}
	}
	if timeName == "" || timeName == "_" || timeName == "." {
		return nil, nil
	}
	var found []fixedWait
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
			continue
		}
		scan := func(list []ast.Stmt) {
			for i, st := range list {
				if i+1 < len(list) && isSleep(st, timeName) && asserts(list[i+1]) {
					found = append(found, fixedWait{Test: fn.Name.Name, Line: fset.Position(st.Pos()).Line})
				}
			}
		}
		var walk func(root ast.Node)
		walk = func(root ast.Node) {
			ast.Inspect(root, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.GoStmt:
					return false
				case *ast.ForStmt, *ast.RangeStmt:
					// The loop's own statements are exempt (bounded polling), but
					// a closure inside it (t.Run in a table loop) is its own scope.
					ast.Inspect(loopBody(n), func(m ast.Node) bool {
						if lit, ok := m.(*ast.FuncLit); ok {
							walk(lit.Body)
							return false
						}
						return true
					})
					return false
				case *ast.CallExpr:
					if isSynctestTest(n) {
						return false // virtual time: a Sleep there waits for nothing real
					}
				case *ast.BlockStmt:
					scan(n.List)
				case *ast.CaseClause:
					scan(n.Body)
				case *ast.CommClause:
					scan(n.Body)
				}
				return true
			})
		}
		walk(fn.Body)
	}
	return found, nil
}

// loopBody is the body of a for or range statement.
func loopBody(n ast.Node) *ast.BlockStmt {
	if f, ok := n.(*ast.ForStmt); ok {
		return f.Body
	}
	return n.(*ast.RangeStmt).Body
}

// isSynctestTest reports a call to synctest.Test.
func isSynctestTest(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Test" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "synctest"
}

// isSleep reports a statement that is a call to <timeName>.Sleep.
func isSleep(st ast.Stmt, timeName string) bool {
	es, ok := st.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Sleep" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == timeName && pkg.Obj == nil
}

// isAssertCall reports a call to one of the assertion methods.
func isAssertCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && assertions[sel.Sel.Name]
}

// asserts reports a direct assertion call, or an if whose body or else
// calls one (outside a nested go func).
func asserts(st ast.Stmt) bool {
	switch st := st.(type) {
	case *ast.ExprStmt:
		return isAssertCall(st.X)
	case *ast.IfStmt:
		hit := false
		branches := []ast.Node{st.Body}
		if st.Else != nil {
			branches = append(branches, st.Else)
		}
		for _, b := range branches {
			ast.Inspect(b, func(n ast.Node) bool {
				if hit {
					return false
				}
				if _, ok := n.(*ast.GoStmt); ok {
					return false
				}
				if e, ok := n.(ast.Expr); ok && isAssertCall(e) {
					hit = true
				}
				return !hit
			})
		}
		return hit
	}
	return false
}

// TestFindFixedWaits pins the detector on testdata/fixedwait: the lines
// carrying a "want fixedwait" comment are reported, and nothing else.
func TestFindFixedWaits(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "fixedwait", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	total := 0
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var want []int
		for i, l := range strings.Split(string(src), "\n") {
			if strings.Contains(l, "// want fixedwait") {
				want = append(want, i+1)
			}
		}
		total += len(want)
		found, err := findFixedWaits(file, src)
		if err != nil {
			t.Fatal(err)
		}
		var got []int
		for _, f := range found {
			got = append(got, f.Line)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: flagged lines %v, want %v", file, got, want)
		}
	}
	if total < 10 {
		t.Fatalf("fixtures carry %d expected sites, want at least 10: the fixtures were not read", total)
	}
}

// fixedWaitOK is the existing code the guard tolerates, keyed by
// "path:TestFunction" (path relative to the module root), each with its
// reason. It only shrinks: an entry that matches nothing fails the test.
//
// The legitimate shapes: an absence window (the Sleep lets time pass to
// prove an event does NOT happen; no state to wait for) and a gap between
// two timestamps.
var fixedWaitOK = map[string]string{
	"internal/daemon/server_session_wiring_test.go:TestServerStartsSessionSamplerByDefault":       "absence window: proves the sampler writes no line once the daemon stopped",
	"internal/tui/toggle_server_test.go:TestToggleServerSparesForeignListener":                    "absence window: proves the other worktree's listener is not killed after the refusal",
	"internal/daemon/store/backenddata_test.go:TestUpdateWorkspaceBackendDataLeavesActivityAlone": "separates two timestamps: a bumped last_activity_at cannot equal the original one",
	"internal/daemon/outputs_listener_test.go:TestOutputsListenerOffWhenHTTPEmpty":                "absence window: proves a daemon with HTTP off binds no outputs listener",
	"spikes/h2/hold/hold_test.go:TestHolderSurvivesLauncherKill":                                  "absence window: proves the holder does not die once its launcher's group is killed",
}

// staleFixedWaitEntries lists the entries of ok that matched no fixed wait in a
// file that is present under root. An entry whose file is absent is skipped:
// the public export leaves some paths out (spikes/), and a missing file proves
// nothing about the entry.
func staleFixedWaitEntries(root string, ok map[string]string, seen map[string]bool) []string {
	var stale []string
	for k := range ok {
		if seen[k] {
			continue
		}
		file, _, _ := strings.Cut(k, ":")
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(file))); err != nil && os.IsNotExist(err) {
			continue
		}
		stale = append(stale, k)
	}
	sort.Strings(stale)
	return stale
}

// TestNoFixedWaitThenAssert sweeps every _test.go of the module and fails on
// a fixed sleep followed by an assertion outside fixedWaitOK.
func TestNoFixedWaitThenAssert(t *testing.T) {
	if _, err := os.Stat(filepath.Join(moduleRoot, "go.mod")); err != nil {
		t.Fatalf("module root not where expected: %v", err)
	}
	read := 0
	seen := map[string]bool{}
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
				rel == "web", rel == "macapp":
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
		found, err := findFixedWaits(p, src)
		if err != nil {
			return err
		}
		for _, f := range found {
			key := rel + ":" + f.Test
			seen[key] = true
			if fixedWaitOK[key] == "" {
				bad = append(bad, rel+":"+strconv.Itoa(f.Line)+" ("+f.Test+")")
			}
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
		t.Errorf("%s: time.Sleep followed by an assertion — wait for the state "+
			"(testutil.Eventually, a channel, an event), not for a duration", b)
	}
	for _, k := range staleFixedWaitEntries(moduleRoot, fixedWaitOK, seen) {
		t.Errorf("fixedWaitOK entry %s matches no fixed wait any more: remove it", k)
	}
	t.Logf("read %d _test.go files; %d fixed waits flagged outside fixedWaitOK, %d entries in fixedWaitOK", read, len(bad), len(fixedWaitOK))
}

func TestStaleFixedWaitEntriesSkipsAbsentFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "present_test.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok := map[string]string{
		"gone/hold_test.go:TestA":  "file absent from this tree (public export)",
		"present_test.go:TestB":    "file present, no fixed wait left: stale",
		"present_test.go:TestKept": "still flagged",
	}
	got := staleFixedWaitEntries(root, ok, map[string]bool{"present_test.go:TestKept": true})
	want := []string{"present_test.go:TestB"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stale = %v, want %v", got, want)
	}
}
