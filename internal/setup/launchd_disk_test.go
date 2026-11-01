package setup

import (
	"os"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/testutil"
)

// The disk flags follow the optional-flag rule: absent unless set, carried as
// chosen (an explicit 0 turns the gate or the alert off), and read back by a
// second `pigtail setup`.
func TestPlistDiskFlagsRoundTrip(t *testing.T) {
	bare, err := GeneratePlist(PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bare, "--admit-disk-floor-gb") {
		t.Fatalf("an unset disk floor must be omitted:\n%s", bare)
	}
	set, err := GeneratePlist(PlistParams{
		PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin",
		AdmitDiskFloorGB: IntPtr(0), DiskAlertGB: IntPtr(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "<string>--admit-disk-floor-gb</string>\n    <string>0</string>"; !strings.Contains(set, want) {
		t.Fatalf("plist is missing %q:\n%s", want, set)
	}
	if want := "<string>--disk-alert-gb</string>\n    <string>0</string>"; !strings.Contains(set, want) {
		t.Fatalf("plist is missing %q:\n%s", want, set)
	}
	path := testutil.MkdirTemp(t, "plist-") + "/sh.pigtail.daemon.plist"
	if err := os.WriteFile(path, []byte(set), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := PreserveTuning(path, PlistParams{PigtaildPath: "/new/pigtaild", ConfigDir: "/c", Path: "/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if got.DiskAlertGB == nil || *got.DiskAlertGB != 0 {
		t.Fatalf("an explicit -disk-alert-gb 0 must survive setup, got %v", got.DiskAlertGB)
	}
	if got.AdmitDiskFloorGB == nil || *got.AdmitDiskFloorGB != 0 {
		t.Fatalf("an explicit -admit-disk-floor-gb 0 must survive setup, got %v", got.AdmitDiskFloorGB)
	}
}
