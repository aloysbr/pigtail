package thermal

import "testing"

func TestParseNotifyutil(t *testing.T) {
	ok := []struct {
		in   string
		want State
	}{
		{"com.apple.system.thermalpressurelevel 0\n", Nominal},
		{"com.apple.system.thermalpressurelevel 1\n", Fair},
		{"com.apple.system.thermalpressurelevel 2\n", Serious},
		{"com.apple.system.thermalpressurelevel 3\n", Critical},
		{"com.apple.system.thermalpressurelevel 4\n", Critical},
	}
	for _, c := range ok {
		got, err := parseNotifyutil(c.in)
		if err != nil || got != c.want {
			t.Errorf("parseNotifyutil(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{
		"",
		"garbage\n",
		"com.apple.system.other 0\n",
		"com.apple.system.thermalpressurelevel 5\n",
		"com.apple.system.thermalpressurelevel -1\n",
		"com.apple.system.thermalpressurelevel x\n",
		"com.apple.system.thermalpressurelevel 0 extra\n",
	} {
		if got, err := parseNotifyutil(in); err == nil {
			t.Errorf("parseNotifyutil(%q) = %v, want an error", in, got)
		}
	}
}

func TestStateString(t *testing.T) {
	for s, want := range map[State]string{Nominal: "nominal", Fair: "fair", Serious: "serious", Critical: "critical"} {
		if got := s.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", int(s), got, want)
		}
	}
}
