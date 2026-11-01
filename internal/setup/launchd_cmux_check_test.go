package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -cmux-check-after-relaunch (off|report|fix, pigtaild default report)
// written in the plist by hand must survive `pigtail setup` — never make it
// abort as an unknown flag, never be dropped; a value pigtaild would refuse
// to start with is refused here too.
func TestCmuxCheckAfterRelaunchRoundTrip(t *testing.T) {
	got, err := ReadPlistTuning(writePlistArgs(t, "--cmux-check-after-relaunch", "fix", "--max-servers", "8"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.CmuxCheckAfterRelaunch != "fix" || got.MaxServers == nil || *got.MaxServers != 8 {
		t.Fatalf("read = %q, max-servers %v", got.CmuxCheckAfterRelaunch, got.MaxServers)
	}
	if _, err := ReadPlistTuning(writePlistArgs(t, "-cmux-check-after-relaunch=repair")); err == nil {
		t.Fatal("a value pigtaild refuses must be refused")
	}
	base := PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"}
	unset, err := GeneratePlist(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(unset, "cmux-check-after-relaunch") {
		t.Fatalf("unset: the flag must be omitted so pigtaild's default applies:\n%s", unset)
	}
	p := base
	p.CmuxCheckAfterRelaunch = "off"
	first, err := GeneratePlist(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "<string>--cmux-check-after-relaunch</string>\n    <string>off</string>") {
		t.Fatalf("an explicit value must be emitted:\n%s", first)
	}
	path := filepath.Join(t.TempDir(), "sh.pigtail.daemon.plist")
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	params, err := PreserveTuning(path, base)
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
}

// Revue PR B I4: --cmux-check-ctrl-c (rescue|unread), added to the plist by
// hand after the acceptance, is carried like
// --cmux-check-after-relaunch: never refused as an unknown flag, never
// dropped; a value pigtaild would refuse to start with is refused here too.
func TestCmuxCheckCtrlCRoundTrip(t *testing.T) {
	got, err := ReadPlistTuning(writePlistArgs(t, "--cmux-check-after-relaunch", "pilot", "--cmux-check-ctrl-c", "unread"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.CmuxCheckCtrlC != "unread" || got.CmuxCheckAfterRelaunch != "pilot" {
		t.Fatalf("read = %q, %q", got.CmuxCheckCtrlC, got.CmuxCheckAfterRelaunch)
	}
	if _, err := ReadPlistTuning(writePlistArgs(t, "-cmux-check-ctrl-c=always")); err == nil {
		t.Fatal("a value pigtaild refuses must be refused")
	}
	base := PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"}
	unset, err := GeneratePlist(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(unset, "cmux-check-ctrl-c") {
		t.Fatalf("unset: the flag must be omitted so pigtaild's default applies:\n%s", unset)
	}
	p := base
	p.CmuxCheckCtrlC = "unread"
	first, err := GeneratePlist(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "<string>--cmux-check-ctrl-c</string>\n    <string>unread</string>") {
		t.Fatalf("an explicit value must be emitted:\n%s", first)
	}
	path := filepath.Join(t.TempDir(), "sh.pigtail.daemon.plist")
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	params, err := PreserveTuning(path, base)
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
}
