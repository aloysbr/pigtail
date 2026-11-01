package cmuxsync

import (
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
)

// The auto-resume warning reads cmux's UserDefaults when the file lacks the
// key: never the real Mac's.
func TestMain(m *testing.M) {
	// Written against the former builtin scope table (perso master,
	// globex default): every test runs on it unless it installs another.
	scopetest.MainTwo()
	cmux.ReadDefaults = func(string) (string, bool, error) { return "", false, nil }
	os.Exit(m.Run())
}

// P1 (sub_x000000000000040): in unsorted mode a ⌘N session is born right
// under the selected folder member only with cmux's
// workspaceGroups.newWorkspacePlacement at "afterCurrent" (its default); any
// other value, or a file pigtail cannot read, is said in the state's warnings.
func TestSettingsWarningsPlacement(t *testing.T) {
	t.Parallel()
	const placementEnd = `{
  // the user 2026-10-15
  "workspaceGroups": {
    "newWorkspacePlacement": "end",
  },
}`
	for _, tc := range []struct {
		name     string
		body     string // "" = no file
		unsorted bool
		want     string // a substring of the one warning; "" = none
	}{
		{"end, unsorted", placementEnd, true, `newWorkspacePlacement is "end"`},
		{"top, unsorted", `{"workspaceGroups":{"newWorkspacePlacement":"top"}}`, true, `newWorkspacePlacement is "top"`},
		{"afterCurrent", `{"workspaceGroups":{"newWorkspacePlacement":"afterCurrent"}}`, true, ""},
		{"absent key: cmux default", `{"app":{"reorderOnNotification":false}}`, true, ""},
		{"no file: cmux defaults", "", true, ""},
		{"end, sorted: P1 is unsorted only", placementEnd, false, ""},
		{"unparseable, unsorted", `{"workspaceGroups": {"newWorkspacePlacement": "end" /* inline */}}`, true, "placement is not checked"},
		{"unparseable, sorted: as before", `{"workspaceGroups": /* inline */ {}}`, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "settings.json")
			if tc.body != "" {
				if err := os.WriteFile(p, []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got := SettingsWarnings(p, tc.unsorted, false)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("warnings = %q, want none", got)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0], tc.want) {
				t.Fatalf("warnings = %q, want one containing %q", got, tc.want)
			}
		})
	}
	// The sidebar warnings are unchanged, and come with the placement one.
	p := filepath.Join(t.TempDir(), "settings.json")
	body := `{"sidebar":{"showCustomMetadata":false},"workspaceGroups":{"newWorkspacePlacement":"end"}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := SettingsWarnings(p, true, false)
	if len(got) != 2 || !slices.ContainsFunc(got, func(w string) bool { return strings.Contains(w, "showCustomMetadata") }) {
		t.Fatalf("warnings = %q, want the pills one and the placement one", got)
	}
}

// An unreadable file (not a missing one) in unsorted mode is said too.
func TestSettingsWarningsUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // a directory: ReadFile fails with something else than ErrNotExist
	got := SettingsWarnings(dir, true, false)
	if len(got) != 1 || !strings.Contains(got[0], "placement is not checked") {
		t.Fatalf("warnings = %q", got)
	}
	if got := SettingsWarnings(dir, false, false); len(got) != 0 {
		t.Fatalf("sorted: warnings = %q, want none (as before)", got)
	}
}

func TestSettingsWarningsAutoResumeDrift(t *testing.T) {
	dir := t.TempDir()
	on := filepath.Join(dir, "on.json")
	off := filepath.Join(dir, "off.json")
	_ = os.WriteFile(on, []byte(`{}`), 0o600)
	_ = os.WriteFile(off, []byte(`{"terminal": {"autoResumeAgentSessions": false}}`), 0o600)
	if w := strings.Join(SettingsWarnings(on, false, true), "|"); !strings.Contains(w, "is on: cmux resumes") {
		t.Errorf("on + fix: %q", w)
	}
	if w := strings.Join(SettingsWarnings(off, false, false), "|"); !strings.Contains(w, "tabs stay at the shell") {
		t.Errorf("off + not fix: %q", w)
	}
	if w := SettingsWarnings(off, false, true); len(w) != 0 {
		t.Errorf("off + fix (D1): %q, want none", w)
	}
}

// Review round 1: SettingsWarnings runs on request paths (Runner.State under
// its mutex) and the auto-resume read may fork `defaults`: the result is
// cached per settings.json mtime for a short TTL.
func TestSettingsWarningsAutoResumeCached(t *testing.T) {
	calls := 0
	old := cmux.ReadDefaults
	cmux.ReadDefaults = func(string) (string, bool, error) { calls++; return "", false, nil }
	t.Cleanup(func() { cmux.ReadDefaults = old })
	now := time.Now()
	oldNow := autoResumeNow
	autoResumeNow = func() time.Time { return now }
	t.Cleanup(func() { autoResumeNow = oldNow })

	p := filepath.Join(t.TempDir(), "settings.json")
	_ = os.WriteFile(p, []byte(`{}`), 0o600)
	SettingsWarnings(p, false, true)
	SettingsWarnings(p, false, true)
	if calls != 1 {
		t.Fatalf("defaults read %d times in 2 calls, want 1 (cached)", calls)
	}
	now = now.Add(61 * time.Second)
	SettingsWarnings(p, false, true)
	if calls != 2 {
		t.Fatalf("defaults read %d times after the TTL, want 2", calls)
	}
	_ = os.WriteFile(p, []byte(`{"terminal":{"autoResumeAgentSessions":false}}`), 0o600)
	future := now.Add(time.Hour)
	_ = os.Chtimes(p, future, future)
	if w := SettingsWarnings(p, false, true); len(w) != 0 {
		t.Fatalf("warnings after the file changed = %q, want the new value read (off + fix = none)", w)
	}
}
