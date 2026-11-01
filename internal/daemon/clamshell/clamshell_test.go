package clamshell

import (
	"strings"
	"testing"
)

// fixtureD1 is an excerpt of the real `ioreg -r -k AppleClamshellState -d 1`
// output measured on 2026-10-24, its "Clamshell" neighbours included.
func fixtureD1(state string) string {
	return strings.Join([]string{
		`+-o IOPMrootDomain  <class IOPMrootDomain, id 0x100000126, registered, matched, active, busy 0 (0 ms), retain 196>`,
		`    {`,
		`      "AppleClamshellCausesSleep" = Yes`,
		`      "AppleClamshellState" = ` + state,
		`      "Last Sleep Reason" = "Clamshell Sleep"`,
		`    }`,
		``,
	}, "\n")
}

func TestParseIoreg(t *testing.T) {
	ok := []struct {
		name string
		in   string
		want bool
	}{
		{"d1 open", fixtureD1("No"), false},
		{"d1 closed", fixtureD1("Yes"), true},
		{"d4 open", "+-o IOPMrootDomain  <class IOPMrootDomain>\n  |   \"AppleClamshellCausesSleep\" = Yes\n  |   \"AppleClamshellState\" = No\n", false},
		{"d4 closed", "  |   \"AppleClamshellState\" = Yes\n", true},
	}
	for _, c := range ok {
		got, err := parseIoreg(c.in)
		if err != nil || got != c.want {
			t.Errorf("%s: parseIoreg = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	noKey := strings.Join([]string{
		`+-o IOPMrootDomain  <class IOPMrootDomain>`,
		`    {`,
		`      "AppleClamshellCausesSleep" = Yes`,
		`      "Last Sleep Reason" = "Clamshell Sleep"`,
		`    }`,
	}, "\n")
	bad := []struct{ name, in string }{
		{"empty (key absent: ioreg prints nothing, exit 0)", ""},
		{"key absent, neighbours only", noKey},
		{"two lines (ambiguous)", fixtureD1("No") + `      "AppleClamshellState" = Yes` + "\n"},
		{"Maybe", fixtureD1("Maybe")},
		{"1", fixtureD1("1")},
		{"approaching name", `      "AppleClamshellStateX" = Yes` + "\n"},
	}
	for _, c := range bad {
		if got, err := parseIoreg(c.in); err == nil {
			t.Errorf("%s: parseIoreg = %v, want an error", c.name, got)
		}
	}
}
