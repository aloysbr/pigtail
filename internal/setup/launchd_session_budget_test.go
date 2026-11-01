package setup

import (
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
)

// What `setup --session-budget` writes is the ceiling and the thresholds the
// orchestrator chose; the rest of the values are pigtaild's own defaults, and
// must not drift from them.
func TestSessionBudgetValuesMatchDaemonDefaults(t *testing.T) {
	d := sessbudget.DefaultConfig()
	if SessionBudgetFloor != d.Floor || SessionBudgetWindow != d.Window || SessionBudgetMinGap != d.MinGap ||
		float64(SessionBudgetCompressKBPerSec)*1024 != d.CompressBytesPerSec || uint64(SessionBudgetMinAvailableMB)<<20 != d.MinAvailable {
		t.Fatalf("setup constants %d %v %v %d %d drifted from sessbudget.DefaultConfig %+v",
			SessionBudgetFloor, SessionBudgetWindow, SessionBudgetMinGap, SessionBudgetCompressKBPerSec, SessionBudgetMinAvailableMB, d)
	}
	if SessionBudgetCeiling <= SessionBudgetFloor {
		t.Fatalf("ceiling %d must exceed the floor %d", SessionBudgetCeiling, SessionBudgetFloor)
	}
}

// The six session flags are emitted when set, read back from the plist, and a
// plist that does not carry them carries none.
func TestSessionBudgetFlagsRoundTrip(t *testing.T) {
	empty, err := GeneratePlist(PlistParams{PigtaildPath: "/bin/pigtaild", ConfigDir: "/c", Path: "/p"})
	if err != nil || strings.Contains(empty, "--session-") {
		t.Fatalf("unset session flags must be omitted: %v\n%s", err, empty)
	}
	p := SessionBudgetParams()
	p.PigtaildPath, p.ConfigDir, p.Path = "/bin/pigtaild", "/c", "/p"
	out, err := GeneratePlist(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--session-floor", "--session-ceiling", "--session-window", "--session-min-gap", "--session-compress-kb-per-sec", "--session-min-available"} {
		if !strings.Contains(out, "<string>"+want+"</string>") {
			t.Fatalf("plist lacks %s:\n%s", want, out)
		}
	}
	args, err := parseProgramArguments([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	got, err := tuningFromArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionFloor == nil || *got.SessionFloor != SessionBudgetFloor || *got.SessionCeiling != SessionBudgetCeiling ||
		got.SessionWindow != SessionBudgetWindow || got.SessionMinGap != SessionBudgetMinGap ||
		*got.SessionCompressKBPerSec != SessionBudgetCompressKBPerSec || *got.SessionMinAvailableMB != SessionBudgetMinAvailableMB {
		t.Fatalf("read back %+v", got)
	}
	// pigtaild refuses a duration it cannot parse: so does the read.
	bad := append(append([]string{}, args...), "--session-window", "soon")
	if _, err := tuningFromArgs(bad); err == nil {
		t.Fatal("an unparseable --session-window must be refused")
	}
}
