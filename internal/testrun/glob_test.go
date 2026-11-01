package testrun

import (
	"path/filepath"
	"strings"
	"testing"
)

// The tree this monorepo actually has, reduced to what the globs see: a workspace
// file at the root, eleven-ish project configs at two depths, one directory
// holding *two* matching configs, and the directories a monorepo is full of
// that must never be walked into.
func workspaceTree(t *testing.T) string {
	t.Helper()
	return tree(t, map[string]string{
		"vitest.workspace.ts":                    "export default ['**/*/vite.config.ts', '**/*/vitest.config.ts'];\n",
		"apps/web/vite.config.ts":                "export default {};\n",
		"apps/web/vitest.config.ts":              "export default {};\n",
		"apps/keycloak-theme/vite.config.ts":     "export default {};\n",
		"libs/shared-utils/vitest.config.ts":     "export default {};\n",
		"libs/ui/vite.config.ts":                 "export default {};\n",
		"node_modules/pkg/vite.config.ts":        "export default {};\n",
		"dist/apps/web/vite.config.ts":           "export default {};\n",
		"coverage/x/vite.config.ts":              "export default {};\n",
		".git/hooks/vite.config.ts":              "export default {};\n",
		"libs/ui/dist/inner/vitest.config.ts":    "export default {};\n",
		"libs/ui/node_modules/p/vite.config.ts":  "export default {};\n",
		"apps/web/src/components/Button.test.ts": "",
	})
}

func TestExpandGlobsFindsEveryProjectConfig(t *testing.T) {
	root := workspaceTree(t)

	got, err := ExpandGlobs(root, []string{"**/*/vite.config.ts", "**/*/vitest.config.ts"})
	if err != nil {
		t.Fatalf("ExpandGlobs: %v", err)
	}

	want := []string{
		"apps/keycloak-theme/vite.config.ts",
		"apps/web/vite.config.ts",
		"apps/web/vitest.config.ts",
		"libs/shared-utils/vitest.config.ts",
		"libs/ui/vite.config.ts",
	}
	assertRelPaths(t, root, got, want)
}

// node_modules, dist, coverage and .git hold thousands of files and no
// project: walking them is the difference between a glob that answers in
// milliseconds and one that stats a monorepo's whole dependency tree. A
// vendored config matching the pattern in there would also add a project the
// repo never asked for.
func TestExpandGlobsSkipsVendorAndBuildDirectories(t *testing.T) {
	root := workspaceTree(t)

	got, err := ExpandGlobs(root, []string{"**/*/vite.config.ts", "**/*/vitest.config.ts"})
	if err != nil {
		t.Fatalf("ExpandGlobs: %v", err)
	}

	for _, p := range got {
		for _, bad := range []string{"node_modules", "dist", "coverage", ".git"} {
			if strings.Contains(p, string(filepath.Separator)+bad+string(filepath.Separator)) {
				t.Errorf("%s was walked into, and it must not be", bad)
			}
		}
	}
}

// Two patterns can match the same file (a repo listing both a broad and a
// narrow glob). Vitest would run that project once; so must the list pigtail
// reports and wraps.
func TestExpandGlobsDeduplicatesAndSorts(t *testing.T) {
	root := workspaceTree(t)

	got, err := ExpandGlobs(root, []string{"**/*/vite.config.ts", "apps/*/vite.config.ts", "**/*/vite.config.ts"})
	if err != nil {
		t.Fatalf("ExpandGlobs: %v", err)
	}

	assertRelPaths(t, root, got, []string{
		"apps/keycloak-theme/vite.config.ts",
		"apps/web/vite.config.ts",
		"libs/ui/vite.config.ts",
	})
}

// A workspace entry may name a directory rather than a config file — vitest
// then looks for a config inside it. Reading that as "no project here" would
// silently drop a project.
func TestExpandGlobsResolvesDirectoryEntries(t *testing.T) {
	root := tree(t, map[string]string{
		"packages/a/vitest.config.ts": "export default {};\n",
		"packages/b/vite.config.ts":   "export default {};\n",
		"packages/c/readme.md":        "",
	})

	got, err := ExpandGlobs(root, []string{"packages/*"})
	if err != nil {
		t.Fatalf("ExpandGlobs: %v", err)
	}

	assertRelPaths(t, root, got, []string{
		"packages/a/vitest.config.ts",
		"packages/b/vite.config.ts",
	})
}

// "**" must match zero segments too, or a config sitting directly under the
// root would be invisible to the very pattern meant to catch everything.
func TestExpandGlobsDoubleStarMatchesZeroSegments(t *testing.T) {
	root := tree(t, map[string]string{
		"vitest.config.ts":         "export default {};\n",
		"deep/er/vitest.config.ts": "export default {};\n",
	})

	got, err := ExpandGlobs(root, []string{"**/vitest.config.ts"})
	if err != nil {
		t.Fatalf("ExpandGlobs: %v", err)
	}

	assertRelPaths(t, root, got, []string{
		"deep/er/vitest.config.ts",
		"vitest.config.ts",
	})
}

// A glob that matches nothing is not an error — it is a repo whose workspace
// file points somewhere else, and the caller turns it into a named skip
// rather than a crash.
func TestExpandGlobsMatchingNothingReturnsEmpty(t *testing.T) {
	root := workspaceTree(t)

	got, err := ExpandGlobs(root, []string{"packages/*/vitest.config.ts"})
	if err != nil {
		t.Fatalf("ExpandGlobs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want no matches", got)
	}
}

func assertRelPaths(t *testing.T, root string, got, want []string) {
	t.Helper()
	rel := make([]string, 0, len(got))
	for _, p := range got {
		if !filepath.IsAbs(p) {
			t.Fatalf("ExpandGlobs returned a relative path %q — wrappers need absolute ones", p)
		}
		r, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatalf("Rel(%s, %s): %v", root, p, err)
		}
		rel = append(rel, filepath.ToSlash(r))
	}
	if strings.Join(rel, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n  %s\nwant:\n  %s", strings.Join(rel, "\n  "), strings.Join(want, "\n  "))
	}
}
