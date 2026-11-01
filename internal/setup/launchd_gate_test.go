package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -gate-cmd names a path specific to one machine: no deployment default, an
// empty value omits the flag, a set value survives `pigtail setup` — and a
// plist carrying it must not make setup abort as an unknown flag.
func TestGateCmdIsCarried(t *testing.T) {
	got, err := ReadPlistTuning(writePlistArgs(t, "--gate-cmd", "/Users/x/.local/bin/gate", "--max-servers", "8"))
	if err != nil {
		t.Fatalf("a plist with --gate-cmd was refused: %v", err)
	}
	if got.GateCmd != "/Users/x/.local/bin/gate" || got.MaxServers == nil || *got.MaxServers != 8 {
		t.Fatalf("got GateCmd=%q MaxServers=%v", got.GateCmd, got.MaxServers)
	}
	if got, err := ReadPlistTuning(writePlistArgs(t, "-gate-cmd=/g")); err != nil || got.GateCmd != "/g" {
		t.Fatalf("attached form: %q %v", got.GateCmd, err)
	}
	base := PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"}
	plain, err := GeneratePlist(base)
	if err != nil || strings.Contains(plain, "gate-cmd") {
		t.Fatalf("unset must omit the flag (%v):\n%s", err, plain)
	}
	with := base
	with.GateCmd = "/Users/x/.local/bin/gate"
	first, err := GeneratePlist(with)
	if err != nil || !strings.Contains(first, "<string>--gate-cmd</string>\n    <string>/Users/x/.local/bin/gate</string>") {
		t.Fatalf("set must be emitted (%v):\n%s", err, first)
	}
	path := filepath.Join(t.TempDir(), "sh.pigtail.daemon.plist")
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	params, err := PreserveTuning(path, base)
	if err != nil || params.GateCmd != "/Users/x/.local/bin/gate" {
		t.Fatalf("setup dropped --gate-cmd: %q %v", params.GateCmd, err)
	}
}
