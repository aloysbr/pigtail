package setup

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// --- cmux sidebar ------------------------------------------------------------
//
// #147 gave pigtaild -cmux-sidebar (a bool, off by default) and
// -cmux-sidebar-window <UUID> (repeatable). The sidebar was then switched on
// for good by hand-editing ~/Library/LaunchAgents/sh.pigtail.daemon.plist —
// and tuningFromArgs refuses any flag it does not know, so the next
// `pigtail setup` would have aborted on it. These tests pin the fix.

func writePlistArgs(t *testing.T, args ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>ProgramArguments</key>
  <array>
    <string>/p/pigtaild</string>
`)
	for _, a := range args {
		b.WriteString("    <string>" + a + "</string>\n")
	}
	b.WriteString("  </array>\n</dict>\n</plist>\n")
	path := filepath.Join(t.TempDir(), "sh.pigtail.daemon.plist")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The exact shape of the live plist measured on 2026-10-01: a bare
// --cmux-sidebar, sitting between two other flags. A bool flag takes no
// separate value, so the flag after it must not be swallowed as its value.
func TestReadPlistTuning_bareCmuxSidebarAsHandEdited(t *testing.T) {
	path := writePlistArgs(t,
		"-max-servers=18",
		"-db-offsite-dir=backup-host:/srv/backups/pigtail-backups",
		"--cmux-sidebar",
		"-db-offsite-fallback-dest=backup-vpn:/srv/backups/pigtail-backups",
	)
	got, err := ReadPlistTuning(path)
	if err != nil {
		t.Fatalf("a plist carrying --cmux-sidebar must be readable: %v", err)
	}
	if got.CmuxSidebar == nil || !*got.CmuxSidebar {
		t.Fatalf("bare --cmux-sidebar must read back as on, got %v", got.CmuxSidebar)
	}
	if got.DBOffsiteFallbackDest != "backup-vpn:/srv/backups/pigtail-backups" {
		t.Errorf("the flag after --cmux-sidebar was swallowed: fallback dest = %q", got.DBOffsiteFallbackDest)
	}
	if got.MaxServers == nil || *got.MaxServers != 18 {
		t.Errorf("max-servers = %v, want 18", got.MaxServers)
	}
}

// --cmux-sidebar as the very last argument must not trip the
// "flag has no value" refusal meant for value flags.
func TestReadPlistTuning_bareCmuxSidebarLast(t *testing.T) {
	got, err := ReadPlistTuning(writePlistArgs(t, "--max-servers", "8", "-cmux-sidebar"))
	if err != nil {
		t.Fatal(err)
	}
	if got.CmuxSidebar == nil || !*got.CmuxSidebar {
		t.Fatalf("got %v, want on", got.CmuxSidebar)
	}
}

// Go's flag package accepts -flag=true|false (and the other ParseBool
// spellings) for a bool; an explicit off is a choice to carry, not drop.
func TestReadPlistTuning_cmuxSidebarAttachedValue(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want bool
	}{
		{"--cmux-sidebar=true", true},
		{"-cmux-sidebar=1", true},
		{"--cmux-sidebar=false", false},
		{"-cmux-sidebar=0", false},
	} {
		got, err := ReadPlistTuning(writePlistArgs(t, tc.arg))
		if err != nil {
			t.Fatalf("%s: %v", tc.arg, err)
		}
		if got.CmuxSidebar == nil || *got.CmuxSidebar != tc.want {
			t.Errorf("%s = %v, want %v", tc.arg, got.CmuxSidebar, tc.want)
		}
	}
}

func TestReadPlistTuning_cmuxSidebarBadValueFailsLoudly(t *testing.T) {
	_, err := ReadPlistTuning(writePlistArgs(t, "--cmux-sidebar=maybe"))
	if err == nil || !strings.Contains(err.Error(), "--cmux-sidebar") {
		t.Fatalf("a non-boolean value must be refused and named, got %v", err)
	}
}

func TestReadPlistTuning_cmuxSidebarWindowsRepeatable(t *testing.T) {
	got, err := ReadPlistTuning(writePlistArgs(t,
		"--cmux-sidebar-window", "AAAA-1",
		"-cmux-sidebar-window=BBBB-2",
	))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"AAAA-1", "BBBB-2"}; !reflect.DeepEqual(got.CmuxSidebarWindows, want) {
		t.Fatalf("windows = %v, want %v", got.CmuxSidebarWindows, want)
	}
}

func TestGeneratePlistOmitsUnsetCmuxFlags(t *testing.T) {
	plist, err := GeneratePlist(PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plist, "cmux") {
		t.Fatalf("unset cmux flags must be omitted so pigtaild's own default (off) applies:\n%s", plist)
	}
}

func TestGeneratePlistEmitsCmuxFlags(t *testing.T) {
	on := true
	plist, err := GeneratePlist(PlistParams{
		PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin",
		CmuxSidebar:        &on,
		CmuxSidebarWindows: []string{"AAAA-1", "BBBB-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<string>--cmux-sidebar</string>\n",
		"<string>--cmux-sidebar-window</string>\n    <string>AAAA-1</string>",
		"<string>--cmux-sidebar-window</string>\n    <string>BBBB-2</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist is missing %q:\n%s", want, plist)
		}
	}

	off := false
	plist, err = GeneratePlist(PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin", CmuxSidebar: &off})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plist, "<string>--cmux-sidebar=false</string>") {
		t.Errorf("an explicit off must be emitted as chosen:\n%s", plist)
	}
}

// generate → read → generate: a second `pigtail setup` reproduces the plist
// byte for byte, cmux settings included, and the first one starting from
// the hand-edited shape keeps the sidebar on.
func TestPreserveTuningRoundTripsCmuxSidebar(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sidebar bool
		windows []string
	}{
		{"on", true, nil},
		{"off", false, nil},
		{"on with windows", true, []string{"AAAA-1", "BBBB-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.sidebar
			fresh := PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"}
			first, err := GeneratePlist(PlistParams{
				PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin",
				CmuxSidebar: &s, CmuxSidebarWindows: tc.windows,
			})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "sh.pigtail.daemon.plist")
			if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
				t.Fatal(err)
			}
			params, err := PreserveTuning(path, fresh)
			if err != nil {
				t.Fatal(err)
			}
			second, err := GeneratePlist(params)
			if err != nil {
				t.Fatal(err)
			}
			if second != first {
				t.Fatalf("round trip changed the plist:\n--- first\n%s\n--- second\n%s", first, second)
			}
		})
	}

	// From the hand-edited shape: the regenerated plist keeps the sidebar on.
	path := writePlistArgs(t, "-max-servers=18", "--cmux-sidebar")
	params, err := PreserveTuning(path, PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"})
	if err != nil {
		t.Fatal(err)
	}
	plist, err := GeneratePlist(params)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plist, "<string>--cmux-sidebar</string>") {
		t.Fatalf("setup must keep a hand-enabled sidebar on:\n%s", plist)
	}
}
