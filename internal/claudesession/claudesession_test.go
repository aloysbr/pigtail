package claudesession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seed writes a transcript (and optionally its sidecar) inside a fake config dir.
func seed(t *testing.T, configDir, slug, id string, sidecar bool) {
	t.Helper()
	dir := filepath.Join(configDir, "projects", slug)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if sidecar {
		if err := os.MkdirAll(filepath.Join(dir, id, "subagents"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFindLocatesHostingConfigDir(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	seed(t, b, "-Users-a-Developer", id, false)

	tr, ok, err := Find(id, []string{a, b})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if !ok {
		t.Fatal("Find did not locate the transcript")
	}
	if tr.ConfigDir != b {
		t.Fatalf("ConfigDir = %q, want %q", tr.ConfigDir, b)
	}
	if tr.Slug != "-Users-a-Developer" {
		t.Fatalf("Slug = %q", tr.Slug)
	}
}

func TestFindAbsent(t *testing.T) {
	_, ok, err := Find("nope", []string{t.TempDir()})
	if err != nil {
		t.Fatalf("an absent session is not an error: %v", err)
	}
	if ok {
		t.Fatal("expected not found")
	}
}

// TestFindReportsASessionHostedByTwoAccounts is the live state this fix
// exists for: eight sessions hand-migrated on 2026-08-12 exist in BOTH
// ~/.claude-two and ~/.claude-two-max. Find must not silently pick one —
// there is no correct answer, only a decision the user has to make — so it
// reports the ambiguity and names every hosting file.
func TestFindReportsASessionHostedByTwoAccounts(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	seed(t, a, "-w", id, false)
	seed(t, b, "-w", id, false)

	_, ok, err := Find(id, []string{a, b})
	if err == nil {
		t.Fatal("expected an ambiguity error when two config dirs host the same session")
	}
	if ok {
		t.Fatal("an ambiguous session must not be reported as resolved")
	}
	for _, dir := range []string{a, b} {
		if !strings.Contains(err.Error(), dir) {
			t.Fatalf("error %q should name %q", err, dir)
		}
	}
}

// TestFindAllReturnsEveryHostingConfigDirInOrder covers the primitive Find
// rests on, including the ordering it inherits from the caller (which is what
// claudeprofile.Table.ConfigDirs makes deterministic).
func TestFindAllReturnsEveryHostingConfigDirInOrder(t *testing.T) {
	a, b, c := t.TempDir(), t.TempDir(), t.TempDir()
	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	seed(t, a, "-w", id, false)
	seed(t, c, "-w", id, false)

	found, err := FindAll(id, []string{a, b, c})
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("got %d transcripts, want 2", len(found))
	}
	if found[0].ConfigDir != a || found[1].ConfigDir != c {
		t.Fatalf("got %q then %q, want %q then %q", found[0].ConfigDir, found[1].ConfigDir, a, c)
	}
}

func TestPaths(t *testing.T) {
	tr := Transcript{ID: "abc", ConfigDir: "/cfg", Slug: "-x"}
	if got, want := tr.JSONLPath(), "/cfg/projects/-x/abc.jsonl"; got != want {
		t.Fatalf("JSONLPath = %q, want %q", got, want)
	}
	if got, want := tr.SidecarDir(), "/cfg/projects/-x/abc"; got != want {
		t.Fatalf("SidecarDir = %q, want %q", got, want)
	}
}

// The screen Claude Code shows when /exit meets background work, as read by
// `cmux read-screen` (2026-09-24, Claude Code 2.1.281).
const exitDialogScreen = `
 Background work is running

 The following will stop when you exit:
   · 2 subagents running

 ❯ 1. Exit and stop tasks
   2. Move to background and exit
   3. Stay
`

func TestExitDialogShown(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   bool
	}{
		{"background-work dialog", exitDialogScreen, true},
		{"idle prompt", "\n > \n  ? for shortcuts\n", false},
		{"busy turn", "\n ✻ Cogitating… (12s · esc to interrupt)\n > /exit\n", false},
		{"empty screen", "", false},
		// The title alone could be quoted in the conversation above the
		// prompt; the dialog is only open when its options are on screen too.
		{"title quoted in conversation", "⏺ the 'Background work is running' dialog\n > \n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExitDialogShown(tc.screen); got != tc.want {
				t.Fatalf("ExitDialogShown = %v, want %v", got, tc.want)
			}
		})
	}
}
