package prepush

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// moduleRoot is this package's module root: internal/prepush is two levels down.
const moduleRoot = "../.."

// walkPatterns are the six marks of a test that reads module files by a
// relative path: TestWalksModuleIsComplete demands a WalksModule row (or an
// exclusion) for every package whose tests carry one.
var walkPatterns = regexp.MustCompile(`"\.\./\.\."|"\.\.", *"\.\."|moduleRoot|repoRoot\(|runtime\.Caller|"\.\./\.\./`)

// walkExclusions are packages the patterns catch that need no row, each with
// its reason.
var walkExclusions = map[string]string{
	"internal/buildinfo":       "reads ../../Makefile; Makefile → full by the path table",
	"internal/daemon/handlers": "relay_wake_test reads internal/claudesession/testdata, already in its test graph; dispatch_guard_test's match is a literal",
	"internal/daemon/cmuxsync": "folders_test reads internal/cmux/testdata; internal/cmux is in its test graph",
	"internal/devserver":       `statedir_test's "../../evil" is a text fixture, not a read`,
	"internal/prepush":         "its own tests hold the patterns; internal/prepush/** → full by the path table",
}

func TestWalksModuleIsComplete(t *testing.T) {
	inTable := map[string]bool{}
	for _, w := range WalksModule {
		inTable[w.Pkg] = true
	}
	for p := range walkExclusions {
		if inTable[p] {
			t.Errorf("%s is both a WalksModule row and an exclusion", p)
		}
	}

	read := 0
	caught := map[string]bool{}
	testFuncs := map[string]string{} // dir → concatenated _test.go text
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
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		read++
		dir := filepath.ToSlash(filepath.Dir(rel))
		testFuncs[dir] += string(b)
		if walkPatterns.Match(b) {
			caught[dir] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if read <= 500 {
		t.Fatalf("read %d _test.go files, want more than 500: the walk read nothing", read)
	}
	for dir := range caught {
		if !inTable[dir] && walkExclusions[dir] == "" {
			t.Errorf("%s's tests read module files by a relative path: add a WalksModule row (or an exclusion with its reason)", dir)
		}
	}
	for p := range inTable {
		if !caught[p] {
			t.Errorf("WalksModule row %s matches no test file: stale row", p)
		}
	}
	for p := range walkExclusions {
		if !caught[p] {
			t.Errorf("exclusion %s matches no test file: stale exclusion", p)
		}
	}
	for _, w := range WalksModule {
		for _, name := range w.Tests {
			if !strings.Contains(testFuncs[w.Pkg], "func "+name+"(") {
				t.Errorf("WalksModule runs %s in %s, which has no such test", name, w.Pkg)
			}
		}
	}
	t.Logf("read %d _test.go files; %d packages caught = %d rows + %d exclusions", read, len(caught), len(inTable), len(walkExclusions))
}

// Positive control on the real module: go list runs once.
func TestPlanOnTheRealModule(t *testing.T) {
	g, err := Load(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	if g.Module != m {
		t.Fatalf("module = %q", g.Module)
	}
	has := func(r Result, rel string) bool {
		for _, p := range r.Packages {
			if p == m+"/"+rel {
				return true
			}
		}
		return false
	}

	r := Plan([]string{"internal/scope/scope.go"}, g)
	if r.Full {
		t.Fatalf("scope.go: unexpected full: %s", r.Reason)
	}
	for _, want := range []string{"internal/scope", "internal/daemon/handlers", "cmd/pigtail"} {
		if !has(r, want) {
			t.Errorf("touching internal/scope must select %s, got %v", want, r.Packages)
		}
	}

	r = Plan([]string{"internal/daemon/clamshell/clamshell.go"}, g)
	if r.Full {
		t.Fatalf("clamshell.go: unexpected full: %s", r.Reason)
	}
	want := []string{m + "/cmd/pigtaild", m + "/internal/daemon/clamshell"}
	if strings.Join(r.Packages, " ") != strings.Join(want, " ") {
		t.Errorf("touching clamshell must select exactly %v, got %v", want, r.Packages)
	}
	for _, run := range r.Runs {
		if run.Pkg == m+"/cmd/pigtail" || run.Pkg == m+"/internal/daemon/handlers" {
			t.Errorf("clamshell must not run %s (got %s)", run.Pkg, run.Regex)
		}
	}
	if len(r.Runs) != len(goWalks) {
		t.Errorf("clamshell runs = %v, want the %d go-file walks", r.Runs, len(goWalks))
	}

	// R1: a test-only helper package selects the package whose tests use it.
	r = Plan([]string{"internal/daemon/sessbudget/sessbudgettest/sessbudgettest.go"}, g)
	if r.Full || !has(r, "internal/daemon/sessbudget") {
		t.Errorf("touching sessbudgettest must select internal/daemon/sessbudget, got %q", r.Lines())
	}
}

func TestLoadRefusesANearEmptyListing(t *testing.T) {
	_, err := decode(strings.NewReader(`{"ImportPath":"x","Dir":"`+t.TempDir()+`"}`), t.TempDir())
	if err == nil {
		t.Fatal("a listing outside the root must fail")
	}
	root := t.TempDir()
	_, err = decode(strings.NewReader(`{"ImportPath":"x","Dir":"`+root+`"}`), root)
	if err == nil || !strings.Contains(err.Error(), "go list read nothing") {
		t.Fatalf("one package must fail as a broken listing, got %v", err)
	}
}
