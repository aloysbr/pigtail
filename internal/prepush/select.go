// Package prepush chooses what the pre-push hook tests: from the files a push
// touches, the Go packages they belong to, every package whose build or test
// depends on those, the test functions that read the touched files at run
// time (WalksModule), and the non-Go make targets.
//
// Why a table and not just the import graph: some tests read files of the
// module at run time (a guard that walks every .go file, a doc test that
// greps CLAUDE.md). The graph cannot see those reads, so WalksModule names
// them, and TestWalksModuleIsComplete fails when a new test starts walking
// the module without a row.
//
// Safe by default (D11): any doubt — a path outside the table, a go list
// error, an unknown diff base, the build files, this package itself — makes
// the plan "full", which runs every check but -race (make check-push-steps).
// The full make check (with -race) before a merge catches what a targeted
// push could miss.
//
// Git stays in the hook: this package never runs git (CLAUDE.md,
// Architecture discipline). The hook computes the pushed files and hands
// them over; Load only runs `go list`.
package prepush

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Walk is one test-time read of module files: Pkg's tests named in Tests
// read the files matched by Paths. An empty Tests means the whole package.
//
// Paths are slash globs relative to the module root: `*` matches inside one
// segment, `**` any number of segments, and a leading `!` excludes.
type Walk struct {
	Pkg   string // module-relative directory, e.g. "internal/scope"
	Paths []string
	Tests []string
}

// writeTTYGuard is internal/inputprobe's guard test. Its name is split
// because that guard fails on any .go file outside the probe that spells
// the probe's write function.
const writeTTYGuard = "TestWrite" + "TTYOnlyCallers"

// nonTestGo is every non-test .go file of the module.
var nonTestGo = []string{"**/*.go", "!**/*_test.go"}

// WalksModule is the inventory of tests that read module files at run time
// (D10, read on c6aaaa25 with the six patterns of TestWalksModuleIsComplete).
// A row runs only when one of its paths changes, and only its tests.
var WalksModule = []Walk{
	{Pkg: "internal/heavycmd", Paths: []string{"Makefile"}, Tests: []string{"TestClassify"}},
	{Pkg: "internal/scope", Paths: nonTestGo, Tests: []string{"TestNoHardcodedScopeNames"}},
	{Pkg: "internal/envlocal", Paths: nonTestGo, Tests: []string{"TestNoManualEnvLocalParsing"}},
	{Pkg: "internal/inputprobe", Paths: []string{"**/*.go"}, Tests: []string{writeTTYGuard}},
	{Pkg: "internal/claudeusage", Paths: nonTestGo, Tests: []string{"TestNoAccountLetterInCode"}},
	{Pkg: "internal/spawnreg", Paths: nonTestGo, Tests: []string{"TestReserveOnlyThroughWithReservation"}},
	{Pkg: "internal/space", Paths: []string{"**/*.go", "**/*.md", "**/*.yaml",
		"!**/*_test.go", "!docs/superpowers/**", "!macapp/**", "!**/.*/**"},
		Tests: []string{"TestNoStaleNoSubjectLabel"}},
	{Pkg: "internal/setup", Paths: nonTestGo, Tests: []string{"TestLaunchctlOnlyInSetup"}},
	{Pkg: "internal/cmux", Paths: nonTestGo,
		Tests: []string{"TestOnlyFoldersReorderOrGroup", "TestOnlyFoldersPin", "TestNeverNotifies"}},
	{Pkg: "cmd/pigtail", Paths: []string{"cmd/pigtail/*.go", "internal/daemon/*.go",
		"internal/daemon/handlers/*.go", "internal/daemon/cmuxsync/*.go", "!**/*_test.go"},
		Tests: []string{"TestStandbyReadOnlyThroughPredicate"}},
	{Pkg: "internal/daemon", Paths: []string{"docs/wire-protocol.md"},
		Tests: []string{"TestDocsCmuxFolders", "TestDocsSessionLaunch", "TestWebWritesDocumented",
			"TestDocsCmuxFolderSort", "TestDocsNoSortMovesOneTab"}},
	{Pkg: "internal/daemon", Paths: []string{"docs/architecture.md"},
		Tests: []string{"TestDocsCmuxFolders", "TestDocsSessionLaunch", "TestDocsCmuxFolderSort", "TestDocsNoSortMovesOneTab"}},
	{Pkg: "internal/webui", Paths: []string{"web/**"}, Tests: []string{"TestDistStampMatchesSources", "TestGoModIgnoresWeb"}},
	{Pkg: "internal/sdksession", Paths: []string{"clients/sdkhost/**", "!clients/sdkhost/node_modules/**"},
		Tests: []string{"TestHostFileContract", "TestFakeClaudeSeesCleanEnv"}},
	{Pkg: "internal/livepass", Paths: []string{"scripts/live-pass.sh"}},
	{Pkg: "cmd/pigtaild", Paths: []string{"scripts/live-pass.sh"}, Tests: []string{"TestLivePassTurnsOrphanReaperOff", "TestLivePassTurnsTmpSweepOff"}},
	{Pkg: "internal/testlint", Paths: []string{"**/*_test.go"}, Tests: []string{"TestNoFixedWaitThenAssert"}},
	{Pkg: "internal/vocab", Paths: []string{"**", "!CHANGELOG.md", "!internal/webui/dist/**",
		"!docs/superpowers/**", "!docs/plans/**", "!docs/specs/**"},
		Tests: []string{"TestNoRetiredProductNames"}},
}

// Pkg is one entry of `go list -e -test -json`.
type Pkg struct {
	ImportPath string
	Dir        string   // relative to the module root, slash-separated ("." for the root)
	Deps       []string // as go list gives them: a test variant's deps carry their " [p.test]" suffix
	Embeds     []string // EmbedFiles, TestEmbedFiles, XTestEmbedFiles, relative to Dir
	ForTest    string
	Error      string // .Error and .DepsErrors, joined; empty when go list had none
}

// Graph is the module as go list saw it.
type Graph struct {
	Module string // module path, e.g. "github.com/aloysbr/pigtail"
	Pkgs   []Pkg
	// Exists reports whether a module-relative path is still on disk; nil
	// means every path exists. A removed file outside a package → full.
	Exists func(rel string) bool
}

// Run is one `go test -run` line: Regex of Pkg's tests.
type Run struct {
	Pkg   string // import path
	Regex string
}

// Result is the selection for one push.
type Result struct {
	Full     bool
	Reason   string
	Targets  []string // make targets, vet and lint first
	Packages []string // import paths tested whole
	Runs     []Run
}

// Lines renders the contract of cmd/affected.
func (r Result) Lines() []string {
	if r.Full {
		return []string{"full " + r.Reason}
	}
	out := []string{"make " + strings.Join(r.Targets, " ")}
	if len(r.Packages) > 0 {
		out = append(out, "go-test "+strings.Join(r.Packages, " "))
	}
	for _, run := range r.Runs {
		out = append(out, "go-test-run "+run.Pkg+" "+run.Regex)
	}
	return out
}

// unknownBase is the single line the hook writes when it found no diff base.
const unknownBase = "*"

// fullPaths are paths whose change falls back to `make check-push-steps`
// (make check minus -race and the stamp).
var fullPaths = []string{"go.mod", "go.sum", "Makefile", ".golangci.yml", ".githooks/**",
	"internal/prepush/**", "scripts/check-affected.sh", "scripts/check-stamp.sh", "scripts/prepush-differential.sh"}

// quietPaths add nothing beyond the WalksModule rows that name them.
var quietPaths = []string{"docs/**", "*.md", "LICENSE", ".github/**", ".gitignore",
	"release-please-config.json", ".release-please-manifest.json"}

// targetOrder fixes the order make targets are printed in.
var targetOrder = []string{"vet", "lint", "build-linux", "web-check", "test-web", "test-swift", "test-node", "test-hooks"}

func full(reason string) Result { return Result{Full: true, Reason: reason} }

// Plan selects what a push touching files must test. It is pure: the graph
// comes from Load (or a test fixture).
func Plan(files []string, g Graph) Result {
	for _, p := range g.Pkgs {
		if p.Error != "" {
			return full(fmt.Sprintf("go list reported an error in %s: %s", p.ImportPath, firstLine(p.Error)))
		}
	}
	idx := index(g)
	targets := map[string]bool{"vet": true, "lint": true}
	touched := map[string]bool{}
	for _, f := range files {
		f = strings.TrimPrefix(strings.TrimSpace(f), "./")
		if f == "" {
			continue
		}
		if f == unknownBase {
			return full("unknown diff base")
		}
		if matchAny(fullPaths, f) {
			return full("touches " + f)
		}
		additive := false
		if f == "scripts/live-pass.sh" {
			targets["test-hooks"], additive = true, true
		}
		if matchGlob("web/**", f) {
			targets["web-check"], targets["test-web"], additive = true, true, true
		}
		if matchGlob("clients/node/**", f) {
			targets["test-node"], additive = true, true
		}
		if pkg, ok := idx.owner(f, g.exists(f)); ok {
			touched[pkg] = true
			if strings.HasSuffix(f, ".go") {
				targets["build-linux"] = true
			}
			continue
		}
		switch {
		case !g.exists(f):
			return full("removes " + f + " outside a package")
		case matchGlob("macapp/**", f):
			targets["test-swift"] = true
		case matchGlob("scripts/**", f):
			targets["test-hooks"] = true
		case matchAny(quietPaths, f):
		case additive:
		default:
			return full("no rule for " + f)
		}
	}

	whole := idx.dependents(touched)
	for p := range touched {
		whole[p] = true
	}
	runs := map[string][]string{}
	for _, w := range WalksModule {
		ip := idx.importPath(w.Pkg)
		if !matchesAnyFile(w.Paths, files) {
			continue
		}
		if len(w.Tests) == 0 {
			whole[ip] = true
			continue
		}
		runs[ip] = append(runs[ip], w.Tests...)
	}

	r := Result{}
	for _, t := range targetOrder {
		if targets[t] {
			r.Targets = append(r.Targets, t)
		}
	}
	for p := range whole {
		r.Packages = append(r.Packages, p)
	}
	sort.Strings(r.Packages)
	for p, tests := range runs {
		if whole[p] {
			continue
		}
		r.Runs = append(r.Runs, Run{Pkg: p, Regex: "^(" + strings.Join(dedupe(tests), "|") + ")$"})
	}
	sort.Slice(r.Runs, func(i, j int) bool { return r.Runs[i].Pkg < r.Runs[j].Pkg })
	return r
}

func (g Graph) exists(rel string) bool { return g.Exists == nil || g.Exists(rel) }

type graphIndex struct {
	module string
	byDir  map[string]string // module-relative dir → import path (plain packages only)
	embeds map[string]string // module-relative file → owning import path
	pkgs   []Pkg
}

// isTestMain reports the generated `p.test` main package, which go list
// -test also returns: its deps are p's test deps, already on p's variants.
func isTestMain(p Pkg) bool {
	return p.ForTest == "" && !strings.Contains(p.ImportPath, " ") && strings.HasSuffix(p.ImportPath, ".test")
}

// stripVariant drops the " [p.test]" suffix go list puts on test variants.
func stripVariant(ip string) string {
	if i := strings.Index(ip, " ["); i >= 0 {
		return ip[:i]
	}
	return ip
}

func index(g Graph) graphIndex {
	idx := graphIndex{module: g.Module, byDir: map[string]string{}, embeds: map[string]string{}, pkgs: g.Pkgs}
	for _, p := range g.Pkgs {
		if isTestMain(p) {
			continue
		}
		owner := p.ImportPath
		if p.ForTest != "" {
			owner = p.ForTest
		} else {
			idx.byDir[path.Clean(p.Dir)] = p.ImportPath
		}
		for _, e := range p.Embeds {
			idx.embeds[path.Join(p.Dir, e)] = owner
		}
	}
	return idx
}

func (idx graphIndex) importPath(rel string) string {
	if ip, ok := idx.byDir[rel]; ok {
		return ip
	}
	return idx.module + "/" + rel
}

// owner finds the package a file belongs to: an embedded file's package,
// else the package of its directory — the directory above a testdata
// segment for testdata files. A non-Go file still on disk may also belong to
// its nearest parent package; a .go file or a removed path never does (a
// .go file outside a package dir is a package go list did not see).
func (idx graphIndex) owner(f string, exists bool) (string, bool) {
	if ip, ok := idx.embeds[f]; ok {
		return ip, true
	}
	dir := path.Dir(f)
	if i := strings.Index("/"+f, "/testdata/"); i >= 0 {
		dir = path.Clean(strings.TrimSuffix(f[:i], "/"))
		if dir == "" {
			dir = "."
		}
	}
	if ip, ok := idx.byDir[dir]; ok {
		return ip, true
	}
	if !exists || strings.HasSuffix(f, ".go") {
		return "", false
	}
	for dir != "." && dir != "/" && dir != "" {
		dir = path.Dir(dir)
		if ip, ok := idx.byDir[dir]; ok {
			return ip, true
		}
	}
	return "", false
}

// dependents returns every package one of whose variants (the package itself
// or its tests) depends on a touched package. The owner of a variant is its
// ForTest, never its suffix: touching q, recompiled for p's test, selects p.
func (idx graphIndex) dependents(touched map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, p := range idx.pkgs {
		if isTestMain(p) {
			continue
		}
		owner := p.ImportPath
		if p.ForTest != "" {
			owner = p.ForTest
		}
		for _, d := range p.Deps {
			if touched[stripVariant(d)] {
				out[owner] = true
				break
			}
		}
	}
	return out
}

func matchesAnyFile(patterns, files []string) bool {
	for _, f := range files {
		if matchPaths(patterns, strings.TrimPrefix(strings.TrimSpace(f), "./")) {
			return true
		}
	}
	return false
}

// matchPaths applies a Walk's include and `!` exclude globs.
func matchPaths(patterns []string, f string) bool {
	in := false
	for _, p := range patterns {
		if strings.HasPrefix(p, "!") {
			if matchGlob(p[1:], f) {
				return false
			}
			continue
		}
		if matchGlob(p, f) {
			in = true
		}
	}
	return in
}

func matchAny(patterns []string, f string) bool {
	for _, p := range patterns {
		if matchGlob(p, f) {
			return true
		}
	}
	return false
}

// matchGlob matches a slash path against a glob where `**` spans any number
// of segments (zero included) and every other segment is a path.Match pattern.
func matchGlob(pattern, f string) bool {
	return matchSegs(strings.Split(pattern, "/"), strings.Split(f, "/"))
}

func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(segs); i++ {
				if matchSegs(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
