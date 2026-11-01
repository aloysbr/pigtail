package prepush

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"testing"
)

// TestSelectionCoversRecentCommits is the proof of D11: replayed on the last
// commits of origin/main, Plan must select every package one of their files
// belongs to and every package depending on those — recomputed here by a
// second, independent go list — or say full.
//
// The commit list comes from scripts/prepush-differential.sh (git stays in
// sh): PREPUSH_DIFF_INPUT names a file of `<sha>\t<path>` lines. Without it
// the test skips; it is not part of make check, verify replays it.
func TestSelectionCoversRecentCommits(t *testing.T) {
	input := os.Getenv("PREPUSH_DIFF_INPUT")
	if input == "" {
		t.Skip("PREPUSH_DIFF_INPUT unset: run sh scripts/prepush-differential.sh")
	}
	commits, order := readCommits(t, input)
	if len(order) == 0 {
		t.Fatal("PREPUSH_DIFF_INPUT holds no commit: the replay read nothing")
	}
	g, err := Load(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	rdeps := independentReverseDeps(t, g.Module)

	misses := differentialMisses(g, rdeps, commits, order, Plan)
	full := 0
	for _, sha := range order {
		if Plan(commits[sha], g).Full {
			full++
		}
	}
	t.Logf("%d commits replayed, %d full, %d targeted", len(order), full, len(order)-full)
	for _, miss := range misses {
		t.Errorf("miss: %s", miss)
	}

	// Positive control: a plan that drops one dependent is caught.
	dropper := func(files []string, g Graph) Result {
		r := Plan(files, g)
		var kept []string
		for _, p := range r.Packages {
			if p != m+"/cmd/pigtail" {
				kept = append(kept, p)
			}
		}
		r.Packages = kept
		return r
	}
	control := map[string][]string{"control": {"internal/scope/scope.go"}}
	if len(differentialMisses(g, rdeps, control, []string{"control"}, dropper)) == 0 {
		t.Fatal("positive control: a plan omitting cmd/pigtail produced no miss")
	}
}

func readCommits(t *testing.T, name string) (map[string][]string, []string) {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	commits := map[string][]string{}
	var order []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		sha, p, ok := strings.Cut(sc.Text(), "\t")
		if !ok {
			continue
		}
		if _, seen := commits[sha]; !seen {
			order = append(order, sha)
		}
		commits[sha] = append(commits[sha], p)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return commits, order
}

// independentReverseDeps inverts `go list -deps -test -f` output: for each
// module package, every module package whose build or test depends on it.
// It reads ImportPath and Deps only, and maps a variant to its package by
// name ("p_test [p.test]" and "p.test" → p), not by ForTest as Plan does.
func independentReverseDeps(t *testing.T, module string) map[string]map[string]bool {
	t.Helper()
	cmd := exec.Command("go", "list", "-e", "-deps", "-test", "-f", `{{.ImportPath}} {{join .Deps " "}}`, "./...")
	cmd.Dir = moduleRoot
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	owner := func(ip string) string {
		ip = stripVariant(ip)
		ip = strings.TrimSuffix(ip, ".test")
		return strings.TrimSuffix(ip, "_test")
	}
	rdeps := map[string]map[string]bool{}
	for _, line := range strings.Split(string(bytes.TrimSpace(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], module) {
			continue
		}
		importer := owner(fields[0])
		for _, d := range fields[1:] {
			if !strings.HasPrefix(d, module) {
				continue
			}
			dep := stripVariant(d)
			if rdeps[dep] == nil {
				rdeps[dep] = map[string]bool{}
			}
			rdeps[dep][importer] = true
		}
	}
	if len(rdeps) == 0 {
		t.Fatal("go list -deps read no module dependency")
	}
	return rdeps
}

// differentialMisses lists, per commit, the packages the plan should have
// tested whole and did not: `<sha> <path> <missing package>`.
func differentialMisses(g Graph, rdeps map[string]map[string]bool, commits map[string][]string, order []string,
	plan func([]string, Graph) Result) []string {
	byDir := map[string]string{}
	embeds := map[string]string{}
	for _, p := range g.Pkgs {
		if p.ForTest == "" && !isTestMain(p) {
			byDir[p.Dir] = p.ImportPath
			for _, e := range p.Embeds {
				embeds[path.Join(p.Dir, e)] = p.ImportPath
			}
		}
	}
	var misses []string
	for _, sha := range order {
		r := plan(commits[sha], g)
		if r.Full {
			continue
		}
		selected := map[string]bool{}
		for _, p := range r.Packages {
			selected[p] = true
		}
		for _, f := range commits[sha] {
			var owners []string
			if ip, ok := embeds[f]; ok {
				owners = append(owners, ip)
			}
			dir := path.Dir(f)
			if i := strings.Index("/"+f, "/testdata/"); i >= 0 {
				dir = strings.TrimSuffix(f[:i], "/")
			}
			if ip, ok := byDir[dir]; ok {
				owners = append(owners, ip)
			}
			want := map[string]bool{}
			for _, o := range owners {
				want[o] = true
				for d := range rdeps[o] {
					want[d] = true
				}
			}
			var missing []string
			for p := range want {
				if !selected[p] {
					missing = append(missing, p)
				}
			}
			sort.Strings(missing)
			for _, p := range missing {
				misses = append(misses, sha+" "+f+" "+p)
			}
		}
	}
	return misses
}
