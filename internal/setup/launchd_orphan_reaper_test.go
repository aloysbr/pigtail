package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// -orphan-reaper (on|observe|off) and -orphan-reap-after written in the plist
// by hand — turning the reaper off is the safety valve of a feature that
// kills — must survive `pigtail setup`: never an unknown flag that aborts it,
// never dropped; a value pigtaild would refuse to start with is refused here.
func TestOrphanReaperRoundTrip(t *testing.T) {
	got, err := ReadPlistTuning(writePlistArgs(t, "--orphan-reaper", "off", "-orphan-reap-after=20m", "--max-servers", "8"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.OrphanReaper != "off" || got.OrphanReapAfter != 20*time.Minute || got.MaxServers == nil || *got.MaxServers != 8 {
		t.Fatalf("read = %q %v, max-servers %v", got.OrphanReaper, got.OrphanReapAfter, got.MaxServers)
	}
	for _, bad := range [][]string{{"-orphan-reaper=yes"}, {"--orphan-reap-after", "0s"}, {"--orphan-reap-after", "soon"}} {
		if _, err := ReadPlistTuning(writePlistArgs(t, bad...)); err == nil {
			t.Errorf("%v: a value pigtaild refuses must be refused", bad)
		}
	}
	base := PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"}
	unset, err := GeneratePlist(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(unset, "orphan-rea") {
		t.Fatalf("unset: the flags must be omitted so pigtaild's defaults apply:\n%s", unset)
	}
	p := base
	p.OrphanReaper = "observe"
	p.OrphanReapAfter = 30 * time.Minute
	first, err := GeneratePlist(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "<string>--orphan-reaper</string>\n    <string>observe</string>") ||
		!strings.Contains(first, "<string>--orphan-reap-after</string>\n    <string>30m0s</string>") {
		t.Fatalf("explicit values must be emitted:\n%s", first)
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

// -tmp-sweep (on|observe|off) written by hand survives `pigtail setup`, like
// the orphan reaper's; unset omits the flag; a value pigtaild refuses is refused.
func TestTmpSweepRoundTrip(t *testing.T) {
	got, err := ReadPlistTuning(writePlistArgs(t, "--tmp-sweep", "observe"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.TmpSweep != "observe" {
		t.Fatalf("read = %q", got.TmpSweep)
	}
	if _, err := ReadPlistTuning(writePlistArgs(t, "-tmp-sweep=yes")); err == nil {
		t.Error("a value pigtaild refuses must be refused")
	}
	base := PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"}
	unset, err := GeneratePlist(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(unset, "tmp-sweep") {
		t.Fatalf("unset: the flag must be omitted:\n%s", unset)
	}
	p := base
	p.TmpSweep = "off"
	first, err := GeneratePlist(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "<string>--tmp-sweep</string>\n    <string>off</string>") {
		t.Fatalf("explicit value must be emitted:\n%s", first)
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
