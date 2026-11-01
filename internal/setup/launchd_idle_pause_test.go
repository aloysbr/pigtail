package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// (T-b): the plist carries -idle-session-pause-seconds;
// a plist that still carries the old -idle-session-close-seconds keeps its
// value, written back under the new name.
func TestIdleSessionPauseIsCarried(t *testing.T) {
	base := PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"}
	def, err := GeneratePlist(base)
	if err != nil {
		t.Fatal(err)
	}
	if want := "<string>--idle-session-pause-seconds</string>\n    <string>3600</string>"; !strings.Contains(def, want) {
		t.Fatalf("default plist lacks %q:\n%s", want, def)
	}
	if strings.Contains(def, "idle-session-close-seconds") {
		t.Fatalf("the template still emits the old flag:\n%s", def)
	}
	old, err := ReadPlistTuning(writePlistArgs(t, "--idle-session-close-seconds", "0"))
	if err != nil {
		t.Fatal(err)
	}
	if old.IdleSessionPauseSeconds == nil || *old.IdleSessionPauseSeconds != 0 {
		t.Fatalf("old flag read as %v, want 0", old.IdleSessionPauseSeconds)
	}
	base.IdleSessionPauseSeconds = old.IdleSessionPauseSeconds
	got, err := GeneratePlist(base)
	if err != nil {
		t.Fatal(err)
	}
	if want := "<string>--idle-session-pause-seconds</string>\n    <string>0</string>"; !strings.Contains(got, want) || strings.Contains(got, "idle-session-close-seconds") {
		t.Fatalf("regenerated plist:\n%s", got)
	}
}

func TestPlistTuningRoundTripPause(t *testing.T) {
	base := PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin", IdleSessionPauseSeconds: IntPtr(5400)}
	first, err := GeneratePlist(base)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sh.pigtail.daemon.plist")
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := PreserveTuning(path, PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := GeneratePlist(again)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("round trip changed the plist:\n%s\n---\n%s", first, second)
	}
	// A plist with only the old flag is no refusal for the next setup.
	oldPath := writePlistArgs(t, "--idle-session-close-seconds", "7200")
	kept, err := PreserveTuning(oldPath, PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"})
	if err != nil {
		t.Fatalf("setup refused a plist with the old flag: %v", err)
	}
	if kept.IdleSessionPauseSeconds == nil || *kept.IdleSessionPauseSeconds != 7200 {
		t.Fatalf("old value = %v, want 7200", kept.IdleSessionPauseSeconds)
	}
}
