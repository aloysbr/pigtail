package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/spf13/cobra"
)

// installLaunchdPlist is the exact function `pigtail setup` calls to write
// the launchd plist. This is the regression the 2026-09-09 fix round
// existed to close: restoring flags to the plist *template* is not enough
// on its own — `pigtail setup` regenerates the plist from scratch on every
// run, so unless the old plist's values are read back before regenerating,
// every hand-tuned flag (RAM/battery/eviction thresholds, and above all the
// NAS off-site database backup path) is silently reset or dropped on a
// second run. A parser-only test cannot catch that: the bug was in the
// wiring (nothing in this package populated PlistParams from the old
// plist), not in the parsing. This test drives the actual function
// `pigtail setup` calls, end to end, against a real file on disk.
func TestInstallLaunchdPlist_preservesHandTunedFlagsAcrossReruns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".config", "pigtail")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// First run: nothing tuned yet, the deployment defaults apply.
	if err := installLaunchdPlist(configDir, nil, nil, nil, nil); err != nil {
		t.Fatalf("first install: %v", err)
	}

	// Simulate an operator hand-tuning the plist afterwards — exactly what
	// happened on the live machine before this fix, down to the actual
	// values reported by `ps -Awwo args=`.
	tuned, err := setup.GeneratePlist(setup.PlistParams{
		PigtaildPath:          setup.ResolvePigtaildPath(),
		ConfigDir:             configDir,
		Path:                  os.Getenv("PATH"),
		RAMFloorMB:            setup.IntPtr(3072),
		RAMGraceSeconds:       setup.IntPtr(1800),
		IdlePurgeGraceSeconds: setup.IntPtr(3600),
		MaxServers:            setup.IntPtr(18),
		CPUSlots:              setup.IntPtr(-1),
		RAMConfirmWindows:     setup.IntPtr(4),
		BatteryGraceSeconds:   setup.IntPtr(1800),
		DBOffsiteDir:          "backup:/srv/backups",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(setup.PlistPath(), []byte(tuned), 0o644); err != nil {
		t.Fatal(err)
	}

	// Second run: `pigtail setup` re-run, the case the regression report
	// was about. Before this fix, installLaunchdPlist rewrote the plist
	// from bare PigtaildPath/ConfigDir/Path and every hand-tuned flag
	// above — including -db-offsite-dir, which is what keeps the daemon's
	// database backed up to the NAS — was silently lost.
	if err := installLaunchdPlist(configDir, nil, nil, nil, nil); err != nil {
		t.Fatalf("second install: %v", err)
	}

	regenerated, err := os.ReadFile(setup.PlistPath())
	if err != nil {
		t.Fatal(err)
	}
	got := string(regenerated)
	for _, want := range []string{
		"--ram-floor-mb", "<string>3072</string>",
		"--ram-grace-seconds", "<string>1800</string>",
		"--idle-purge-grace-seconds", "<string>3600</string>",
		"--max-servers", "<string>18</string>",
		"--ram-confirm-windows", "<string>4</string>",
		"--battery-grace-seconds", "<string>1800</string>",
		"--db-offsite-dir", "<string>backup:/srv/backups</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("second `pigtail setup` lost hand-tuned flag %q:\n%s", want, got)
		}
	}
}

// A plist installLaunchdPlist cannot fully parse must abort the install
// rather than silently overwrite it with a fresh, un-tuned one.
func TestInstallLaunchdPlist_refusesAnUnparseableExistingPlist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := os.MkdirAll(filepath.Dir(setup.PlistPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(setup.PlistPath(), []byte("not a plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(setup.PlistPath())
	if err != nil {
		t.Fatal(err)
	}

	if err := installLaunchdPlist(filepath.Join(home, ".config", "pigtail"), nil, nil, nil, nil); err == nil {
		t.Fatal("expected an error for an unparseable existing plist")
	}

	after, err := os.ReadFile(setup.PlistPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("a refused install must not touch the existing plist on disk")
	}
}

// `-cpu-slots 0` is a kill switch, not an unset field: it disables the CPU
// slot arbiter outright (design §6). While PlistParams's tuning fields were
// plain ints, an explicit 0 was indistinguishable from "the plist never
// mentioned this flag", so `pigtail setup` rewrote it to the deployment
// default -1 — silently re-enabling, on the next setup run, a feature the
// operator had deliberately turned off. This drives the real function
// `pigtail setup` calls, against a real file on disk.
func TestInstallLaunchdPlist_preservesAHandDisabledArbiter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".config", "pigtail")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// The operator disables the arbiter (and the server cap, and idle
	// purging — all three document 0 as "off") by hand.
	disabled, err := setup.GeneratePlist(setup.PlistParams{
		PigtaildPath:          setup.ResolvePigtaildPath(),
		ConfigDir:             configDir,
		Path:                  os.Getenv("PATH"),
		CPUSlots:              setup.IntPtr(0),
		MaxServers:            setup.IntPtr(0),
		IdlePurgeGraceSeconds: setup.IntPtr(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(setup.PlistPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(setup.PlistPath(), []byte(disabled), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := installLaunchdPlist(configDir, nil, nil, nil, nil); err != nil {
		t.Fatalf("re-run of setup: %v", err)
	}
	regenerated, err := os.ReadFile(setup.PlistPath())
	if err != nil {
		t.Fatal(err)
	}
	got := string(regenerated)
	for _, want := range []string{
		"    <string>--cpu-slots</string>\n    <string>0</string>",
		"    <string>--max-servers</string>\n    <string>0</string>",
		"    <string>--idle-purge-grace-seconds</string>\n    <string>0</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("`pigtail setup` re-enabled a hand-disabled feature; missing:\n%s\ngot:\n%s", want, got)
		}
	}
}

// `pigtail setup --cmux-folders[=false]` writes the flag; a re-run without
// it keeps the value the plist carries.
func TestInstallLaunchdPlist_cmuxFolders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "pigtail")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		t.Helper()
		b, err := os.ReadFile(setup.PlistPath())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	on, off := true, false
	if err := installLaunchdPlist(configDir, &on, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "<string>--cmux-folders</string>") {
		t.Fatalf("--cmux-folders not written:\n%s", got)
	}
	if err := installLaunchdPlist(configDir, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "<string>--cmux-folders</string>") {
		t.Fatalf("a re-run without the option lost it:\n%s", got)
	}
	if err := installLaunchdPlist(configDir, &off, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "<string>--cmux-folders=false</string>") {
		t.Fatalf("--cmux-folders=false not written:\n%s", got)
	}
}

// `pigtail setup --cmux-folder-sort=false` writes the flag; a re-run without
// it keeps the value; --cmux-folders and it are independent.
func TestInstallLaunchdPlist_cmuxFolderSort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "pigtail")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		t.Helper()
		b, err := os.ReadFile(setup.PlistPath())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	on, off := true, false
	if err := installLaunchdPlist(configDir, &on, &off, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "<string>--cmux-folder-sort=false</string>") || !strings.Contains(got, "<string>--cmux-folders</string>") {
		t.Fatalf("flags not written:\n%s", got)
	}
	if err := installLaunchdPlist(configDir, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "<string>--cmux-folder-sort=false</string>") {
		t.Fatalf("a re-run without the option lost it:\n%s", got)
	}
	if err := installLaunchdPlist(configDir, nil, &on, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "<string>--cmux-folder-sort</string>") || !strings.Contains(got, "<string>--cmux-folders</string>") {
		t.Fatalf("--cmux-folder-sort not switched back, or --cmux-folders lost:\n%s", got)
	}
}

// `pigtail setup --cmux-folder-sort=false`, run as a command: the flag reaches
// the plist (cobra flag → Changed → runSetup's arguments → installLaunchdPlist).
// runSetupFn is swapped only to skip the prerequisites, launchctl and the
// daemon wait; the plist is written by the real installLaunchdPlist.
func TestSetupCmd_cmuxFolderSort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "pigtail")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	prev := runSetupFn
	t.Cleanup(func() { runSetupFn = prev })
	runSetupFn = func(_ *cobra.Command, o setupOptions) error {
		return installLaunchdPlist(configDir, o.folders, o.folderSort, o.idlePause, o.sessionBudget)
	}
	run := func(args ...string) string {
		t.Helper()
		root := &cobra.Command{Use: "pigtail", SilenceUsage: true}
		root.AddCommand(setupCmd(root))
		root.SetArgs(append([]string{"setup"}, args...))
		if err := root.Execute(); err != nil {
			t.Fatalf("setup %v: %v", args, err)
		}
		b, err := os.ReadFile(setup.PlistPath())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := run("--cmux-folder-sort=false"); !strings.Contains(got, "<string>--cmux-folder-sort=false</string>") {
		t.Fatalf("setup --cmux-folder-sort=false: flag not in the plist:\n%s", got)
	}
	if got := run(); !strings.Contains(got, "<string>--cmux-folder-sort=false</string>") {
		t.Fatalf("setup without the option (not Changed) lost it:\n%s", got)
	}
	if got := run("--cmux-folder-sort"); !strings.Contains(got, "<string>--cmux-folder-sort</string>") {
		t.Fatalf("setup --cmux-folder-sort: not switched back on:\n%s", got)
	}
}

// `pigtail setup --idle-session-pause-seconds 0` writes the threshold (0 =
// off) into the plist, which an agent cannot edit by hand; a re-run without
// the option keeps it; a negative value is refused before anything is written.
func TestSetupCmd_idleSessionPause(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "pigtail")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	prev := runSetupFn
	t.Cleanup(func() { runSetupFn = prev })
	runSetupFn = func(_ *cobra.Command, o setupOptions) error {
		return installLaunchdPlist(configDir, o.folders, o.folderSort, o.idlePause, o.sessionBudget)
	}
	run := func(args ...string) (string, error) {
		t.Helper()
		root := &cobra.Command{Use: "pigtail", SilenceUsage: true, SilenceErrors: true}
		root.AddCommand(setupCmd(root))
		root.SetArgs(append([]string{"setup"}, args...))
		err := root.Execute()
		b, _ := os.ReadFile(setup.PlistPath())
		return string(b), err
	}
	off := "    <string>--idle-session-pause-seconds</string>\n    <string>0</string>"
	if got, err := run("--idle-session-pause-seconds", "0"); err != nil || !strings.Contains(got, off) {
		t.Fatalf("setup --idle-session-pause-seconds 0: err=%v, plist:\n%s", err, got)
	}
	if got, err := run(); err != nil || !strings.Contains(got, off) {
		t.Fatalf("setup without the option (not Changed) lost the 0: err=%v, plist:\n%s", err, got)
	}
	if got, err := run("--idle-session-pause-seconds", "7200"); err != nil || !strings.Contains(got, "<string>--idle-session-pause-seconds</string>\n    <string>7200</string>") {
		t.Fatalf("setup --idle-session-pause-seconds 7200: err=%v, plist:\n%s", err, got)
	}
	if got, err := run("--idle-session-pause-seconds", "-1"); err == nil || !strings.Contains(got, "<string>7200</string>") {
		t.Fatalf("setup --idle-session-pause-seconds -1: want an error and the plist untouched, err=%v, plist:\n%s", err, got)
	}
	// The old CLI flag, hidden, still writes the new one.
	if got, err := run("--idle-session-close-seconds", "5400"); err != nil || !strings.Contains(got, "<string>--idle-session-pause-seconds</string>\n    <string>5400</string>") {
		t.Fatalf("setup --idle-session-close-seconds 5400: err=%v, plist:\n%s", err, got)
	}
}

// sessionBudgetPlist is what `pigtail setup --session-budget` writes
// (orchestrator, tier A technical, 2026-10-22): floor 6, ceiling 20, window
// 5 min, min gap 40 s, compress threshold the admission's 16 MiB/s, and the
// available-memory floor the admission's 6 GiB.
const sessionBudgetPlist = `    <string>--session-floor</string>
    <string>6</string>
    <string>--session-ceiling</string>
    <string>20</string>
    <string>--session-window</string>
    <string>5m0s</string>
    <string>--session-min-gap</string>
    <string>40s</string>
    <string>--session-compress-kb-per-sec</string>
    <string>16384</string>
    <string>--session-min-available</string>
    <string>6144</string>
`

func sessionSetupRunner(t *testing.T) func(args ...string) (string, error) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "pigtail")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	prev := runSetupFn
	t.Cleanup(func() { runSetupFn = prev })
	runSetupFn = func(_ *cobra.Command, o setupOptions) error {
		return installLaunchdPlist(configDir, o.folders, o.folderSort, o.idlePause, o.sessionBudget)
	}
	return func(args ...string) (string, error) {
		t.Helper()
		root := &cobra.Command{Use: "pigtail", SilenceUsage: true, SilenceErrors: true}
		root.AddCommand(setupCmd(root))
		root.SetArgs(append([]string{"setup"}, args...))
		err := root.Execute()
		b, _ := os.ReadFile(setup.PlistPath())
		return string(b), err
	}
}

// Without --session-budget the plist carries no session flag (the sampler runs
// from install, the gate stays off); with it, the six values, including
// -session-min-available.
func TestSetupWritesSessionBudgetFlags(t *testing.T) {
	run := sessionSetupRunner(t)
	got, err := run()
	if err != nil || strings.Contains(got, "--session-") {
		t.Fatalf("setup without the option wrote a session flag: err=%v, plist:\n%s", err, got)
	}
	got, err = run("--session-budget")
	if err != nil || !strings.Contains(got, sessionBudgetPlist) {
		t.Fatalf("setup --session-budget: err=%v, plist:\n%s", err, got)
	}
}

// A re-run without the option keeps the flags (hand-tuned values included);
// --session-budget=false turns the gate off (ceiling 0) and keeps the rest.
func TestSetupCarriesSessionFlagsForward(t *testing.T) {
	run := sessionSetupRunner(t)
	if _, err := run("--session-budget"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(setup.PlistPath())
	tuned := strings.Replace(string(b), "<string>20</string>", "<string>14</string>", 1)
	if tuned == string(b) {
		t.Fatal("fixture: ceiling not found")
	}
	if err := os.WriteFile(setup.PlistPath(), []byte(tuned), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := run()
	if err != nil || !strings.Contains(got, strings.Replace(sessionBudgetPlist, "<string>20</string>", "<string>14</string>", 1)) {
		t.Fatalf("a re-run lost the session flags: err=%v, plist:\n%s", err, got)
	}
	got, err = run("--session-budget=false")
	if err != nil || !strings.Contains(got, "<string>--session-ceiling</string>\n    <string>0</string>") || !strings.Contains(got, "<string>--session-floor</string>\n    <string>6</string>") {
		t.Fatalf("setup --session-budget=false: err=%v, plist:\n%s", err, got)
	}
	got, err = run()
	if err != nil || !strings.Contains(got, "<string>--session-ceiling</string>\n    <string>0</string>") {
		t.Fatalf("the gate-off value was not carried: err=%v, plist:\n%s", err, got)
	}
}
