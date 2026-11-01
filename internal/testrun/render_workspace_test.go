package testrun

import (
	"path/filepath"
	"regexp"
	"testing"
)

// vitestProjectConfigRegexp is vitest 4's own check on a project config's
// filename, copied from node_modules/vitest/dist/chunks/cli-api.*.js
// (CONFIG_REGEXP) at 4.1.11:
//
//	/^vite(?:st)?(?:\.[\w-]+)?\.config\./
//
// Measured, not assumed: a wrapper named "vitest.pigtail.web.config.mjs"
// is *rejected* by vitest 4 with "must start with vitest.config/vite.config",
// because only ONE dotted segment is allowed between the prefix and "config".
// Vitest 3 does not check at all, so this constraint is invisible until a repo
// on 4 tries to run — a large monorepo is on 4.1.8.
var vitestProjectConfigRegexp = regexp.MustCompile(`^vite(?:st)?(?:\.[\w-]+)?\.config\.`)

func TestProjectWrapperNamesSatisfyVitest4(t *testing.T) {
	names := []string{
		projectWrapperName("web"),
		projectWrapperName("shared-utils"),
		projectWrapperName("keycloak_theme"),
		projectWrapperName("web-vite.config.ts"),
	}
	for _, n := range names {
		if !vitestProjectConfigRegexp.MatchString(n) {
			t.Errorf("%q would be rejected by vitest 4 as a project config filename", n)
		}
	}
}

// Slugs feed the filename, so anything outside [\w-] breaks the regexp above.
func TestProjectSlugIsFilenameSafe(t *testing.T) {
	cases := map[string]string{
		"apps/web":        "apps-web",
		"@app/ui":         "app-ui",
		"Shared Utils":    "Shared-Utils",
		"....":            "project",
		"keycloak.theme":  "keycloak-theme",
		"libs/ui/../deep": "libs-ui-deep",
	}
	for in, want := range cases {
		if got := projectSlug(in); got != want {
			t.Errorf("projectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// Two configs in one directory — apps/web has both vite.config.ts and
// vitest.config.ts, and a large monorepo's globs match both — would otherwise produce
// two projects with the same fallback name, which vitest refuses.
func TestProjectNamesAreUnique(t *testing.T) {
	root := tree(t, map[string]string{
		"apps/web/package.json":     `{"name":"@app/web"}`,
		"apps/web/vite.config.ts":   "export default {};\n",
		"apps/web/vitest.config.ts": "export default {};\n",
		"libs/ui/vite.config.ts":    "export default {};\n",
	})

	projects := describeProjects(root, []string{
		filepath.Join(root, "apps/web/vite.config.ts"),
		filepath.Join(root, "apps/web/vitest.config.ts"),
		filepath.Join(root, "libs/ui/vite.config.ts"),
	})

	seen := map[string]bool{}
	for _, p := range projects {
		if p.Name == "" {
			t.Fatalf("project %s has no fallback name", p.ConfigPath)
		}
		if seen[p.Name] {
			t.Errorf("duplicate project name %q — vitest refuses two projects with the same name", p.Name)
		}
		seen[p.Name] = true
	}

	// The package.json name is what vitest itself would call that project;
	// renaming it would break a --project=@app/web filter the user already
	// has in their fingers.
	if projects[0].Name != "@app/web" {
		t.Errorf("first project name = %q, want the package.json name @app/web", projects[0].Name)
	}
}

func TestProjectRootIsTheConfigDirectory(t *testing.T) {
	root := tree(t, map[string]string{
		"libs/ui/vite.config.ts": "export default {};\n",
	})

	projects := describeProjects(root, []string{filepath.Join(root, "libs/ui/vite.config.ts")})

	if want := filepath.Join(root, "libs", "ui"); projects[0].Root != want {
		t.Errorf("Root = %q, want %q", projects[0].Root, want)
	}
}
