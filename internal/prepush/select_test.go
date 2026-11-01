package prepush

import (
	"reflect"
	"strings"
	"testing"
)

const m = "github.com/aloysbr/pigtail"

// fixture is a fixed module graph, shaped like `go list -e -test -json`.
func fixture() Graph {
	ip := func(rel string) string { return m + "/" + rel }
	return Graph{Module: m, Pkgs: []Pkg{
		{ImportPath: ip("internal/leaf"), Dir: "internal/leaf"},
		{ImportPath: ip("cmd/pigtaild"), Dir: "cmd/pigtaild", Deps: []string{ip("internal/leaf"), "fmt"}},
		{ImportPath: ip("pkg/pigtail"), Dir: "pkg/pigtail"},
		// tleaf imports pkg/pigtail in its tests only.
		{ImportPath: ip("internal/tleaf"), Dir: "internal/tleaf"},
		{ImportPath: ip("internal/tleaf_test") + " [" + ip("internal/tleaf.test") + "]", ForTest: ip("internal/tleaf"),
			Dir: "internal/tleaf", Deps: []string{ip("pkg/pigtail"), ip("internal/tleaf") + " [" + ip("internal/tleaf.test") + "]"}},
		{ImportPath: ip("internal/tleaf.test"), Dir: "internal/tleaf", Deps: []string{ip("pkg/pigtail")}},
		{ImportPath: ip("x"), Dir: "x"},
		// q is recompiled for p's test and depends on r.
		{ImportPath: ip("p"), Dir: "p"},
		{ImportPath: ip("q"), Dir: "q"},
		{ImportPath: ip("r"), Dir: "r"},
		{ImportPath: ip("p_test") + " [" + ip("p.test") + "]", ForTest: ip("p"), Dir: "p",
			Deps: []string{ip("q") + " [" + ip("p.test") + "]", ip("r")}},
		// A generated test main that lists r: it must be ignored.
		{ImportPath: ip("s"), Dir: "s"},
		{ImportPath: ip("s.test"), Dir: "s", Deps: []string{ip("r")}},
		{ImportPath: ip("clients/node"), Dir: "clients/node", Embeds: []string{"client.cjs", "client.mjs", "slots.mjs", "jest-slots.cjs"}},
		{ImportPath: ip("internal/emb"), Dir: "internal/emb", Embeds: []string{"static/a.css"}},
		{ImportPath: ip("internal/daemon"), Dir: "internal/daemon"},
		{ImportPath: ip("internal/daemon/cmuxsync"), Dir: "internal/daemon/cmuxsync"},
		{ImportPath: ip("internal/space"), Dir: "internal/space"},
		{ImportPath: ip("cmd/pigtail"), Dir: "cmd/pigtail"},
	}}
}

// goWalks is every go-test-run line a non-test .go file outside the standby
// dirs brings.
var goWalks = []string{
	"go-test-run " + m + "/internal/claudeusage ^(TestNoAccountLetterInCode)$",
	"go-test-run " + m + "/internal/cmux ^(TestOnlyFoldersReorderOrGroup|TestOnlyFoldersPin|TestNeverNotifies)$",
	"go-test-run " + m + "/internal/envlocal ^(TestNoManualEnvLocalParsing)$",
	"go-test-run " + m + "/internal/inputprobe ^(" + writeTTYGuard + ")$",
	"go-test-run " + m + "/internal/scope ^(TestNoHardcodedScopeNames)$",
	"go-test-run " + m + "/internal/setup ^(TestLaunchctlOnlyInSetup)$",
	"go-test-run " + m + "/internal/space ^(TestNoStaleNoSubjectLabel)$",
	"go-test-run " + m + "/internal/spawnreg ^(TestReserveOnlyThroughWithReservation)$",
	vocabRun,
}

// vocabRun is internal/vocab's guard: it reads every tracked file but its own
// skips (CHANGELOG.md, docs/superpowers …), so nearly every push runs it.
var vocabRun = "go-test-run " + m + "/internal/vocab ^(TestNoRetiredProductNames)$"

func lines(xs ...any) []string {
	var out []string
	for _, x := range xs {
		switch v := x.(type) {
		case string:
			out = append(out, v)
		case []string:
			out = append(out, v...)
		}
	}
	return out
}

func TestPlan(t *testing.T) {
	daemonDocs := "go-test-run " + m + "/internal/daemon ^(TestDocsCmuxFolders|TestDocsSessionLaunch|TestWebWritesDocumented|TestDocsCmuxFolderSort|TestDocsNoSortMovesOneTab)$"
	archDocs := "go-test-run " + m + "/internal/daemon ^(TestDocsCmuxFolders|TestDocsSessionLaunch|TestDocsCmuxFolderSort|TestDocsNoSortMovesOneTab)$"
	spaceRun := "go-test-run " + m + "/internal/space ^(TestNoStaleNoSubjectLabel)$"
	webui := "go-test-run " + m + "/internal/webui ^(TestDistStampMatchesSources|TestGoModIgnoresWeb)$"
	standby := "go-test-run " + m + "/cmd/pigtail ^(TestStandbyReadOnlyThroughPredicate)$"
	cases := []struct {
		name  string
		files []string
		want  []string
	}{
		{"leaf go file", []string{"internal/leaf/a.go"}, lines(
			"make vet lint build-linux",
			"go-test "+m+"/cmd/pigtaild "+m+"/internal/leaf",
			goWalks)},
		{"CLAUDE.md", []string{"CLAUDE.md"}, lines("make vet lint", spaceRun, vocabRun)},
		{"architecture doc", []string{"docs/architecture.md"}, lines("make vet lint", archDocs, spaceRun, vocabRun)},
		{"wire protocol doc", []string{"docs/wire-protocol.md"}, lines("make vet lint", daemonDocs, spaceRun, vocabRun)},
		{"live-pass script", []string{"scripts/live-pass.sh"}, lines("make vet lint test-hooks", "go-test "+m+"/internal/livepass",
			"go-test-run "+m+"/cmd/pigtaild ^(TestLivePassTurnsOrphanReaperOff|TestLivePassTurnsTmpSweepOff)$", vocabRun)},
		{"test-only importer", []string{"pkg/pigtail/a.go"}, lines(
			"make vet lint build-linux",
			"go-test "+m+"/internal/tleaf "+m+"/pkg/pigtail",
			goWalks)},
		{"testdata", []string{"x/testdata/a.json"}, lines("make vet lint", "go-test "+m+"/x", vocabRun)},
		{"variant owner, test main ignored", []string{"r/r.go"}, lines(
			"make vet lint build-linux",
			"go-test "+m+"/p "+m+"/r",
			goWalks)},
		{"node test", []string{"clients/node/client.cjs.test.mjs"}, lines("make vet lint test-node", "go-test "+m+"/clients/node", vocabRun)},
		{"node embedded", []string{"clients/node/client.cjs"}, lines("make vet lint test-node", "go-test "+m+"/clients/node", vocabRun)},
		{"embedded file", []string{"internal/emb/static/a.css"}, lines("make vet lint", "go-test "+m+"/internal/emb", vocabRun)},
		{"README", []string{"README.md"}, lines("make vet lint", spaceRun, vocabRun)},
		{"plan doc", []string{"docs/superpowers/plans/x.md"}, lines("make vet lint")},
		{"cmuxsync go file", []string{"internal/daemon/cmuxsync/a.go"}, lines(
			"make vet lint build-linux",
			"go-test "+m+"/internal/daemon/cmuxsync",
			standby,
			goWalks)},
		{"web", []string{"web/src/a.ts"}, lines("make vet lint web-check test-web", webui, vocabRun)},
		{"macapp", []string{"macapp/Foo/a.swift"}, lines("make vet lint test-swift", vocabRun)},
		{"other script", []string{"scripts/web-size.sh"}, lines("make vet lint test-hooks", vocabRun)},
		{"go.mod", []string{"go.mod"}, lines("full touches go.mod")},
		{"unknown base", []string{"*"}, lines("full unknown diff base")},
		{"Makefile", []string{"Makefile"}, lines("full touches Makefile")},
		{"the selector", []string{"internal/prepush/select.go"}, lines("full touches internal/prepush/select.go")},
		{"hooks", []string{".githooks/pre-push"}, lines("full touches .githooks/pre-push")},
		{"go test file", []string{"internal/leaf/x_test.go"}, lines(
			"make vet lint build-linux",
			"go-test "+m+"/cmd/pigtaild "+m+"/internal/leaf",
			"go-test-run "+m+"/internal/inputprobe ^("+writeTTYGuard+")$",
			"go-test-run "+m+"/internal/testlint ^(TestNoFixedWaitThenAssert)$", vocabRun)},
		{"outside the table", []string{"foo/bar.txt"}, lines("full no rule for foo/bar.txt")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Plan(tc.files, fixture()).Lines()
			sortRuns(got)
			want := append([]string(nil), tc.want...)
			sortRuns(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Plan(%v):\n got  %q\n want %q", tc.files, got, want)
			}
		})
	}
}

// sortRuns orders the go-test-run lines, which Plan sorts by import path.
func sortRuns(ls []string) {
	for i := range ls {
		for j := i + 1; j < len(ls); j++ {
			if strings.HasPrefix(ls[i], "go-test-run ") && strings.HasPrefix(ls[j], "go-test-run ") && ls[j] < ls[i] {
				ls[i], ls[j] = ls[j], ls[i]
			}
		}
	}
}

func TestPlanRemovedFileOutsideAPackageIsFull(t *testing.T) {
	g := fixture()
	g.Exists = func(rel string) bool { return rel != "internal/gone/a.go" && rel != "internal/leaf/sub/a.json" }
	if r := Plan([]string{"internal/gone/a.go"}, g); !r.Full {
		t.Fatalf("a removed .go outside any package must be full, got %q", r.Lines())
	}
	// A removed non-Go file never borrows its nearest parent package.
	if r := Plan([]string{"internal/leaf/sub/a.json"}, g); !r.Full {
		t.Fatalf("a removed file under a non-package dir must be full, got %q", r.Lines())
	}
	// Still on disk, it does.
	g.Exists = nil
	if got := Plan([]string{"internal/leaf/sub/a.json"}, g).Lines(); !reflect.DeepEqual(got, []string{"make vet lint", "go-test " + m + "/cmd/pigtaild " + m + "/internal/leaf", vocabRun}) {
		t.Fatalf("got %q", got)
	}
}

func TestPlanGoListErrorIsFull(t *testing.T) {
	for _, mut := range []func(*Pkg){
		func(p *Pkg) { p.Error = "no Go files" },
		func(p *Pkg) { p.Error = "dependency error: cannot find package" },
	} {
		g := fixture()
		mut(&g.Pkgs[3])
		r := Plan([]string{"docs/superpowers/plans/x.md"}, g)
		if !r.Full || !strings.Contains(r.Reason, "go list reported an error") {
			t.Fatalf("an error in the graph must be full, got %q", r.Lines())
		}
	}
}

func TestMatchGlob(t *testing.T) {
	for _, tc := range []struct {
		pat, f string
		want   bool
	}{
		{"**/*.go", "a.go", true},
		{"**/*.go", "a/b/c.go", true},
		{"web/**", "web/src/a.ts", true},
		{"web/**", "webx/a", false},
		{"*.md", "README.md", true},
		{"*.md", "docs/a.md", false},
		{"cmd/pigtail/*.go", "cmd/pigtail/sub/a.go", false},
		{"**/.*/**", ".github/x.yml", true},
	} {
		if got := matchGlob(tc.pat, tc.f); got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v", tc.pat, tc.f, got)
		}
	}
}
