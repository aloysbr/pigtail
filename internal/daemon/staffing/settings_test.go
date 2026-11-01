package staffing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestLoadSettingsAbsentIsAdviseThree(t *testing.T) {
	s, err := LoadSettings(filepath.Join(t.TempDir(), "staffing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != pigtail.StaffingAdvise || s.Ceiling != 3 {
		t.Fatalf("absent = %+v, want advise/3", s)
	}
}

func TestLoadSettingsCorruptFallsBackToAdvise(t *testing.T) {
	for name, body := range map[string]string{
		"truncated":       `{"version":1,"mode":"launch","ceil`,
		"unknown version": `{"version":9,"mode":"launch","ceiling":2}`,
		"invalid mode":    `{"version":1,"mode":"foo","ceiling":2}`,
	} {
		p := filepath.Join(t.TempDir(), "staffing.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := LoadSettings(p)
		if err == nil {
			t.Errorf("%s: no error", name)
		}
		if s.Mode != pigtail.StaffingAdvise || s.Ceiling != DefaultCeiling {
			t.Errorf("%s: fallback = %+v, want advise/3", name, s)
		}
	}
	if fallbackMode != pigtail.StaffingAdvise {
		t.Fatalf("fallbackMode = %q: an unreadable file must never read as launch", fallbackMode)
	}
}

func TestSaveSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "staffing.json")
	if err := SaveSettings(p, Settings{Mode: pigtail.StaffingLaunch, Ceiling: 2}); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != pigtail.StaffingLaunch || s.Ceiling != 2 {
		t.Fatalf("round trip = %+v", s)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
	if SettingsPath("") != "" || SettingsPath("/a/b/pigtail.db") != "/a/b/staffing.json" {
		t.Fatalf("SettingsPath = %q", SettingsPath("/a/b/pigtail.db"))
	}
}

func TestSettingsValidate(t *testing.T) {
	for _, tc := range []struct {
		s  Settings
		ok bool
	}{
		{Settings{Mode: "foo", Ceiling: 3}, false},
		{Settings{Mode: pigtail.StaffingAdvise, Ceiling: 0}, false},
		{Settings{Mode: pigtail.StaffingAdvise, Ceiling: 21}, false},
		{Settings{Mode: pigtail.StaffingOff, Ceiling: 1}, true},
		{Settings{Mode: pigtail.StaffingLaunch, Ceiling: 20}, true},
	} {
		if err := tc.s.Validate(); (err == nil) != tc.ok {
			t.Errorf("Validate(%+v) = %v, want ok=%v", tc.s, err, tc.ok)
		}
	}
}
