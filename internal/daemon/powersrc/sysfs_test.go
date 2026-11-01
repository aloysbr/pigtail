//go:build linux

package powersrc

import "testing"

func TestParseACOnline(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool // true == on battery
		wantErr bool
	}{
		{"AC online (1) → mains", "1\n", false, false},
		{"AC offline (0) → battery", "0\n", true, false},
		{"no trailing newline", "0", true, false},
		{"unparseable → error", "huh", false, true},
		{"empty → error", "", false, true},
	}
	for _, c := range cases {
		got, err := parseACOnline(c.content)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.name, err, c.wantErr)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("%s: parseACOnline = %v, want %v", c.name, got, c.want)
		}
	}
}
