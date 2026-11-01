package pigtail

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestThemeJSONAlwaysCarriesDeadlineAndCarried(t *testing.T) {
	for _, tc := range []struct {
		th   Theme
		want []string
	}{
		{Theme{ID: "x"}, []string{`"deadline":""`, `"carried":false`}},
		{Theme{ID: "x", Deadline: "2026-10-31", Carried: true}, []string{`"deadline":"2026-10-31"`, `"carried":true`}},
	} {
		b, err := json.Marshal(tc.th)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range tc.want {
			if !strings.Contains(string(b), w) {
				t.Errorf("%s lacks %s", b, w)
			}
		}
	}
}

func TestValidThemeDeadline(t *testing.T) {
	for _, ok := range []string{"", "2026-10-31", "2028-02-29"} {
		if err := ValidThemeDeadline(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"31/10/2026", "2026-10-32", "2026-02-30", "2026-1-5", "2026-10-31T00:00", " 2026-10-31", "soon"} {
		err := ValidThemeDeadline(bad)
		want := `deadline must be a date YYYY-MM-DD, or empty to clear (got "` + bad + `")`
		if err == nil || err.Error() != want {
			t.Errorf("%q: err = %v, want %q", bad, err, want)
		}
	}
}
