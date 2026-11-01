package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The memory-admission flags follow the optional-flag rule: absent unless
// set, carried as chosen when set — explicit zero included, since 0 is how
// admission is disabled or a gate turned off.
func TestGeneratePlistAdmissionFlags(t *testing.T) {
	bare, err := GeneratePlist(PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bare, "--admit-") {
		t.Fatalf("unset admission flags must be omitted so pigtaild's own defaults apply:\n%s", bare)
	}

	set, err := GeneratePlist(PlistParams{
		PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin",
		AdmitMaxJobs:             IntPtr(0),
		AdmitFloorMB:             IntPtr(8192),
		AdmitPressureLevel:       IntPtr(4),
		AdmitSwapoutKBPerSec:     IntPtr(0),
		AdmitCompressKBPerSec:    IntPtr(0),
		AdmitMaxHoldSeconds:      IntPtr(900),
		AdmitMaxStarveSeconds:    IntPtr(240),
		AdmitCompressCalmSeconds: IntPtr(0), AdmitCompressCalmMaxStarveSeconds: IntPtr(600),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<string>--admit-max-jobs</string>\n    <string>0</string>",
		"<string>--admit-floor-mb</string>\n    <string>8192</string>",
		"<string>--admit-pressure-level</string>\n    <string>4</string>",
		"<string>--admit-swapout-kb-per-sec</string>\n    <string>0</string>",
		"<string>--admit-compress-kb-per-sec</string>\n    <string>0</string>",
		"<string>--admit-max-hold-seconds</string>\n    <string>900</string>",
		"<string>--admit-max-starve-seconds</string>\n    <string>240</string>",
		"<string>--admit-compress-calm-seconds</string>\n    <string>0</string>",
		"<string>--admit-compress-calm-max-starve-seconds</string>\n    <string>600</string>",
	} {
		if !strings.Contains(set, want) {
			t.Errorf("plist is missing %q:\n%s", want, set)
		}
	}
}

// A second `pigtail setup` must carry every admission flag forward — the
// regression class the whole read-back mechanism exists for.
func TestPreserveTuningCarriesAdmissionFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sh.pigtail.daemon.plist")
	first, err := GeneratePlist(PlistParams{
		PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin",
		AdmitMaxJobs: IntPtr(0), AdmitFloorMB: IntPtr(8192), AdmitMaxHoldSeconds: IntPtr(900),
		AdmitMaxStarveSeconds: IntPtr(240), AdmitCompressKBPerSec: IntPtr(8192),
		AdmitCompressCalmSeconds: IntPtr(90), AdmitCompressCalmMaxStarveSeconds: IntPtr(180),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := PreserveTuning(path, PlistParams{PigtaildPath: "/new/pigtaild", ConfigDir: "/c", Path: "/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if got.AdmitMaxJobs == nil || *got.AdmitMaxJobs != 0 {
		t.Fatalf("an explicit -admit-max-jobs 0 (admission off) must survive setup, got %v", got.AdmitMaxJobs)
	}
	if got.AdmitFloorMB == nil || *got.AdmitFloorMB != 8192 || got.AdmitMaxHoldSeconds == nil || *got.AdmitMaxHoldSeconds != 900 {
		t.Fatalf("admission tuning lost: %+v", got)
	}
	if got.AdmitMaxStarveSeconds == nil || *got.AdmitMaxStarveSeconds != 240 {
		t.Fatalf("the starvation guarantee must survive setup too, got %v", got.AdmitMaxStarveSeconds)
	}
	if got.AdmitCompressKBPerSec == nil || *got.AdmitCompressKBPerSec != 8192 {
		t.Fatalf("the compressor gate must survive setup, got %v", got.AdmitCompressKBPerSec)
	}
	if got.AdmitCompressCalmSeconds == nil || *got.AdmitCompressCalmSeconds != 90 ||
		got.AdmitCompressCalmMaxStarveSeconds == nil || *got.AdmitCompressCalmMaxStarveSeconds != 180 {
		t.Fatalf("the calm rule must survive setup, got %v %v", got.AdmitCompressCalmSeconds, got.AdmitCompressCalmMaxStarveSeconds)
	}
	if got.AdmitPressureLevel != nil || got.AdmitSwapoutKBPerSec != nil {
		t.Fatal("flags absent from the old plist must stay absent")
	}
}
