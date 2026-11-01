package vocab

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var retired = regexp.MustCompile(`(?i)cockpit|pigtail[-_ ]?bar(\b|_)|PigtailBar`)

// allow: path → reason. Each entry is a legacy-migration point, a dated
// provenance or a foreign product name.
var allow = map[string]string{
	"web/src/theme/boot.js":                                        "migrates the legacy cockpit-* keys once",
	"web/src/theme/boot-migrate.test.ts":                           "proves that migration",
	"web/src/theme/palette.ref.cjs":                                "frozen copy, its header names its dated source (_outputs/cockpit-ds-et-sessions): do not edit",
	"internal/websession/session.go":                               "adopts the legacy cockpit-session file",
	"internal/websession/session_test.go":                          "proves that adoption",
	"internal/daemon/web_test.go":                                  "proves the old cookie/route lead to the expired page / the 401",
	"internal/daemon/store/migrations/0028_theme_kind_ongoing.sql": "applied SQL migration, never edited (Global Constraints)",
	"cmd/pigtail/cockpit_stub.go":                                  "hidden refusing stub, removal gate 2026-12-01",
	"cmd/pigtail/cockpit_stub_test.go":                             "proves the stub",
	"macapp/Sources/AppCore/LegacyDefaultsMigration.swift":         "reads the legacy defaults domains once, names the retired bundle ids",
	"macapp/Tests/AppCoreTests/LegacyDefaultsMigrationTests.swift": "proves that read",
	"macapp/Sources/MenuBarCore/StatusItemGlyph.swift":             "path of its generator in _outputs (real path)",
	"macapp/Tests/WindowCoreTests/InfoPlistTests.swift":            "asserts the legacy CockpitInfo.plist is gone",
	"scripts/app-install.sh":                                       "trashes the two legacy bundles",
	"scripts/app-install.test.sh":                                  "proves that",
	"internal/backend/git/git_test.go":                             "Globex branch name feat/cockpit-redesign (foreign product)",
	"cmd/pigtail/migrate.go":                                       "Globex worktree name (foreign product)",
	"web/src/theme/adr.test.ts":                                    "reads the dated ADR docs/specs/2026-10-26-cockpit-ds-hybride-themes-adr.md and asserts it cites its dated spec: history file names",
	"internal/vocab/vocab_test.go":                                 "this guard",
}

// allowLines: files that stay scanned, where only the lines matching their
// pattern are allowed (never the whole file).
var allowLines = map[string]*regexp.Regexp{
	// make app removes the stale build outputs (else LaunchServices keeps two more copies).
	"Makefile": regexp.MustCompile(`^\s*rm -rf "(\$\(MACAPP_DIR\)|macapp)/(PigtailBar|Pigtail Cockpit)\.app"`),
	// asserts the retired targets are gone.
	// the retired /cockpit/login route must stay non-public (the daemon no longer serves it).
	"internal/daemon/server_test.go":  regexp.MustCompile(`"/cockpit/login", "/web",`),
	"scripts/macapp-makefile.test.sh": regexp.MustCompile(`make -n (pigtail-bar|pigtail-cockpit|macapps)\b`),
}

// History kept as written (Global Constraints).
var skipPrefixes = []string{"internal/webui/dist/", "docs/superpowers/", "docs/plans/", "docs/specs/"}

// absentAllowed: whether an absent allowlist entry is expected. In the forge only the paths listed in
// scripts/forge/export-exclude.txt may be absent; an export carries no such file (scripts/forge/ is
// left out) and so lacks the forge-only entries.
func absentAllowed(root, rel string) bool {
	b, err := os.ReadFile(filepath.Join(root, "scripts", "forge", "export-exclude.txt"))
	if err != nil {
		return true
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == rel {
			return true
		}
	}
	return false
}

func TestNoRetiredProductNames(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	files := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	scanned := 0
	for _, rel := range files {
		if rel == "" || rel == "CHANGELOG.md" {
			continue
		}
		skip := false
		for _, p := range skipPrefixes {
			if strings.HasPrefix(rel, p) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			if os.IsNotExist(err) {
				continue // deleted in the work tree, not yet staged
			}
			t.Fatal(err)
		}
		if bytes.IndexByte(b, 0) >= 0 {
			continue // binary (.icns, .png, .woff2)
		}
		scanned++
		if _, ok := allow[rel]; ok {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			if re, ok := allowLines[rel]; ok && re.MatchString(line) {
				continue
			}
			if retired.MatchString(line) {
				t.Errorf("%s:%d still names a retired product: %s", rel, i+1, strings.TrimSpace(line))
			}
		}
	}
	if len(files) < 200 || scanned < 200 {
		t.Fatalf("listed %d files, scanned %d: git ls-files read nothing useful", len(files), scanned) // positive control
	}
	for rel := range allow {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil && !absentAllowed(root, rel) {
			t.Errorf("allowlist entry %s no longer exists: drop it", rel)
		}
	}
	for rel := range allowLines {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("allowLines entry %s no longer exists: drop it", rel)
		}
	}
}

// TestAbsentAllowEntry: an allowlist entry whose file is gone is stale in the forge, unless the
// export leaves that very file out; in an export (no scripts/forge/export-exclude.txt) the
// forge-only entries are simply absent.
func TestAbsentAllowEntry(t *testing.T) {
	forge := t.TempDir()
	if err := os.MkdirAll(filepath.Join(forge, "scripts", "forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(forge, "scripts", "forge", "export-exclude.txt"), []byte("# c\nweb/a.test.ts\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !absentAllowed(forge, "web/a.test.ts") {
		t.Error("an entry the export excludes may be absent from the forge")
	}
	if absentAllowed(forge, "web/other.ts") {
		t.Error("any other absent entry is stale in the forge")
	}
	if !absentAllowed(t.TempDir(), "web/a.test.ts") {
		t.Error("in an export (no exclude file) forge-only entries are absent by design")
	}
}
