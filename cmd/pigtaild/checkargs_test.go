package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/setup"
)

// A3 (plan 2026-10-18-install-guard, D2): `pigtaild -check-args <flags…>`
// parses a start's flags with the real start's flag set and post-parse
// validations, exits 0 or 2, and starts nothing.

func checkArgsOut(args []string) (int, string, string) {
	var out, errb bytes.Buffer
	code := checkArgs(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCheckArgsAcceptsPlistArgs(t *testing.T) {
	tmp := t.TempDir()
	code, out, errOut := checkArgsOut([]string{
		"--socket", filepath.Join(tmp, "p.sock"), "--db", filepath.Join(tmp, "p.db"),
		"--cmux-sidebar", "--cmux-folders", "--cmux-folder-sort=false",
		"--max-servers=18", "--admit-class-limit", "vitest-browser:1",
	})
	if code != 0 || out != "pigtaild -check-args: ok\n" || errOut != "" {
		t.Fatalf("check-args = %d, stdout %q, stderr %q", code, out, errOut)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("check-args created %d entries in %s: %v", len(entries), tmp, entries)
	}
}

func TestCheckArgsNamesUnknownFlag(t *testing.T) {
	code, out, errOut := checkArgsOut([]string{"--socket", "x", "--cmux-bogus=false"})
	if code != 2 || out != "" {
		t.Fatalf("check-args = %d, stdout %q", code, out)
	}
	if want := "pigtaild -check-args: flag provided but not defined: -cmux-bogus\n"; errOut != want {
		t.Fatalf("stderr %q, want %q", errOut, want)
	}
}

func TestCheckArgsRunsPostParseValidations(t *testing.T) {
	for _, c := range []struct {
		arg, name, reason string
		code              int
	}{
		{"-cmux-check-after-relaunch=bogus", "cmux-check-after-relaunch", "", 2},
		{"-cmux-check-ctrl-c=x", "cmux-check-ctrl-c", "", 2},
		{"-static-port-range=9-1", "static-port-range", "", 2},
		{"-idle-session-pause-seconds=-1", "idle-session-pause-seconds", "must be >= 0 (0 = off)", 2},
		{"-idle-session-pause-seconds=0", "", "", 0},
		{"-max-servers=many", "max-servers", "", 2}, // a parse error, for good measure
	} {
		code, out, errOut := checkArgsOut([]string{c.arg})
		if c.code == 0 {
			if code != 0 || errOut != "" {
				t.Errorf("%s: code %d, stderr %q; want accepted", c.arg, code, errOut)
			}
			continue
		}
		if code != 2 || out != "" || !strings.HasPrefix(errOut, "pigtaild -check-args: ") || !strings.Contains(errOut, c.name) || !strings.Contains(errOut, c.reason) {
			t.Errorf("%s: code %d, stdout %q, stderr %q; want 2 naming %s (%s)", c.arg, code, out, errOut, c.name, c.reason)
		}
	}
}

// fullSetupPlist is the plist `pigtail setup` writes with every optional
// field set: every flag setup can ever write is in it.
func fullSetupPlist(t *testing.T) []byte {
	t.Helper()
	yes, no := true, false
	http := "127.0.0.1:7800"
	p := setup.PlistParams{
		PigtaildPath: "/usr/local/bin/pigtaild",
		ConfigDir:    "/tmp/pigtail-cfg",
		Path:         "/usr/bin:/bin",

		RAMFloorMB:              setup.IntPtr(4096),
		RAMGraceSeconds:         setup.IntPtr(300),
		IdlePurgeGraceSeconds:   setup.IntPtr(3600),
		IdleSessionPauseSeconds: setup.IntPtr(3600),
		MaxServers:              setup.IntPtr(18),
		CPUSlots:                setup.IntPtr(-1),
		RAMConfirmWindows:       setup.IntPtr(3),
		BatteryGraceSeconds:     setup.IntPtr(600),

		AdmitMaxJobs:          setup.IntPtr(4),
		AdmitFloorMB:          setup.IntPtr(6144),
		AdmitPressureLevel:    setup.IntPtr(2),
		AdmitSwapoutKBPerSec:  setup.IntPtr(4096),
		AdmitMaxHoldSeconds:   setup.IntPtr(1800),
		AdmitMaxStarveSeconds: setup.IntPtr(180),
		AdmitClassLimits:      []string{"vitest-browser:1", "tsc:2"},

		AdmitCompressCalmSeconds:          setup.IntPtr(60),
		AdmitCompressCalmMaxStarveSeconds: setup.IntPtr(600),

		DBOffsiteDir:          "/tmp/pigtail-offsite",
		DBOffsiteFallbackDest: "nas-pinned:/backups/pigtail",
		GateCmd:               "gate",
		SDKHost:               "/opt/pigtail/clients/sdkhost",
		SDKNode:               "/usr/local/bin/node",
		SDKLaunch:             true,

		CmuxSidebar:            &yes,
		CmuxSidebarWindows:     []string{"00000000-0000-4000-8000-000000000000"},
		CmuxClaudeCodeFix:      &no,
		CmuxFolders:            &yes,
		CmuxFolderSort:         &no,
		CmuxCheckAfterRelaunch: "report",
		CmuxCheckCtrlC:         "unread",

		HTTP: &http,
	}
	s, err := setup.GeneratePlist(p)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(s)
}

// Every flag `pigtail setup` writes is one pigtaild accepts: a flag added to
// the template and not to pigtaild (or renamed in one only) fails here.
func TestCheckArgsAcceptsEverySetupFlag(t *testing.T) {
	args, err := setup.ProgramArguments(fullSetupPlist(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(args) < 2 || args[0] != "/usr/local/bin/pigtaild" {
		t.Fatalf("ProgramArguments = %q", args)
	}
	for _, want := range []string{"--cmux-folder-sort=false", "--admit-class-limit", "--admit-compress-calm-seconds", "--cmux-check-ctrl-c", "--gate-cmd", "--sdk-host", "--sdk-node", "--sdk-launch"} {
		found := false
		for _, a := range args {
			found = found || a == want
		}
		if !found {
			t.Fatalf("positive control: %s not in the generated plist: %q", want, args)
		}
	}
	if code, out, errOut := checkArgsOut(args[1:]); code != 0 {
		t.Fatalf("check-args on setup's plist = %d, stdout %q, stderr %q", code, out, errOut)
	}
}
