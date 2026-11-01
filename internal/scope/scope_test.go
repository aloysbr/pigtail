package scope

import (
	"errors"
	"strings"
	"testing"
)

func TestParseUsesTheActiveTable(t *testing.T) {
	for in, want := range map[string]Scope{"gx": "globex", "globex": "globex", " Perso ": "perso"} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if got, err := FromHeader(""); err != nil || got != "globex" {
		t.Fatalf("absent header = %q, %v; want globex (fail-closed default)", got, err)
	}
	if got, err := FromVaultLink("Globex/x"); err != nil || got != "globex" {
		t.Fatalf("Globex/ = %q, %v", got, err)
	}
}

func TestFromVaultLink(t *testing.T) {
	ok := map[string]Scope{
		"Globex/Projects/demo-module": Scope("globex"),
		"Perso/Areas/side-project":    Scope("perso"),
		"Globex/":                     Scope("globex"),
	}
	for in, want := range ok {
		got, err := FromVaultLink(in)
		if err != nil || got != want {
			t.Errorf("FromVaultLink(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "Areas/x", "gx/Projects/x", "GLOBEXS/x", "/Globex/x", "Globex"} {
		if _, err := FromVaultLink(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("FromVaultLink(%q) = %v, want ErrInvalid", in, err)
		}
	}
}

func TestCanRead(t *testing.T) {
	cases := []struct {
		caller, target Scope
		want           bool
	}{
		{Scope("globex"), Scope("globex"), true},
		{Scope("globex"), Scope("perso"), false},
		{Scope("perso"), Scope("perso"), true},
		{Scope("perso"), Scope("globex"), true},
	}
	for _, c := range cases {
		if got := CanRead(c.caller, c.target); got != c.want {
			t.Errorf("CanRead(%s, %s) = %v, want %v", c.caller, c.target, got, c.want)
		}
	}
}

func TestCanWrite(t *testing.T) {
	cases := []struct {
		caller, target Scope
		cross          bool
		want           error
	}{
		{Scope("globex"), Scope("globex"), false, nil},
		{Scope("globex"), Scope("globex"), true, nil},
		{Scope("globex"), Scope("perso"), false, ErrNotVisible},
		{Scope("globex"), Scope("perso"), true, ErrNotVisible}, // the opt-in never widens a globex caller
		{Scope("perso"), Scope("perso"), false, nil},
		{Scope("perso"), Scope("globex"), false, ErrCrossScope},
		{Scope("perso"), Scope("globex"), true, nil},
	}
	for _, c := range cases {
		err := CanWrite(c.caller, c.target, c.cross)
		if c.want == nil && err != nil || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("CanWrite(%s, %s, cross=%v) = %v, want %v", c.caller, c.target, c.cross, err, c.want)
		}
	}
}

func TestListFilter(t *testing.T) {
	cases := []struct {
		caller    Scope
		requested string
		want      Scope
		err       error
	}{
		{Scope("globex"), "", Scope("globex"), nil},
		{Scope("globex"), "gx", Scope("globex"), nil},
		{Scope("globex"), "perso", "", ErrInvalid}, // forbidden reads as unknown
		{Scope("globex"), "all", "", ErrCrossScope},
		{Scope("perso"), "", Any, nil},
		{Scope("perso"), "all", Any, nil},
		{Scope("perso"), "gx", Scope("globex"), nil},
		{Scope("perso"), "perso", Scope("perso"), nil},
		{Scope("perso"), "bogus", "", ErrInvalid},
		{Scope("globex"), "bogus", "", ErrInvalid},
	}
	for _, c := range cases {
		got, err := ListFilter(c.caller, c.requested)
		if c.err != nil {
			if !errors.Is(err, c.err) {
				t.Errorf("ListFilter(%s, %q) err = %v, want %v", c.caller, c.requested, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ListFilter(%s, %q) = %q, %v; want %q", c.caller, c.requested, got, err, c.want)
		}
	}
}

func TestNextFilter(t *testing.T) {
	cases := []struct {
		caller    Scope
		requested string
		want      Scope
		err       error
	}{
		{Scope("globex"), "", Scope("globex"), nil},
		{Scope("globex"), "gx", Scope("globex"), nil},
		{Scope("globex"), "perso", "", ErrInvalid}, // forbidden reads as unknown
		{Scope("perso"), "", Scope("perso"), nil},  // next stays in the caller's own scope by default
		{Scope("perso"), "gx", Scope("globex"), nil},
		{Scope("perso"), "all", "", ErrInvalid}, // "next" across both backlogs has no single rank order
		{Scope("globex"), "all", "", ErrInvalid},
	}
	for _, c := range cases {
		got, err := NextFilter(c.caller, c.requested)
		if c.err != nil {
			if !errors.Is(err, c.err) {
				t.Errorf("NextFilter(%s, %q) err = %v, want %v", c.caller, c.requested, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("NextFilter(%s, %q) = %q, %v; want %q", c.caller, c.requested, got, err, c.want)
		}
	}
}

// useThree installs the three-scope table (scopetest imports this package).
func useThree(t *testing.T) {
	t.Helper()
	tb, err := ParseTable([]byte(threeYAML), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Use(tb))
}

// an error names only what its caller may name.
func TestHintNamesOnlyNameableScopes(t *testing.T) {
	useThree(t)
	for caller, want := range map[Scope]string{"acme": " (want acme)", "perso": " (want acme, globex, perso, all)", "nope": ""} {
		if got := Hint(caller); got != want {
			t.Errorf("Hint(%q) = %q, want %q", caller, got, want)
		}
	}
	if got := RootsHint("acme"); got != " (vault roots: ACME/, CLIENTS/acme/)" {
		t.Errorf("RootsHint(acme) = %q", got)
	}
	_, err := Parse("nope")
	for _, n := range []string{"acme", "perso", "globex"} {
		if err == nil || strings.Contains(err.Error(), n) {
			t.Errorf("Parse(nope) = %v, names %q", err, n)
		}
	}
	_, err = FromVaultLink("ZZZ/x")
	for _, r := range []string{"ACME/", "Perso/", "Globex/"} {
		if err == nil || strings.Contains(err.Error(), r) {
			t.Errorf("FromVaultLink(ZZZ/x) = %v, names %q", err, r)
		}
	}
}

// to a non-master caller, a declared scope it cannot read is an unknown
// name — same error, same text once the name is masked.
func TestForbiddenScopeReadsAsUnknown(t *testing.T) {
	useThree(t)
	mask := func(err error, name string) string { return strings.ReplaceAll(err.Error(), name, "<NAME>") }
	_, unknown := ListFilter("acme", "nope")
	for _, name := range []string{"perso", "gx"} {
		_, err := ListFilter("acme", name)
		if !errors.Is(err, ErrInvalid) || mask(err, name) != mask(unknown, "nope") {
			t.Errorf("ListFilter(acme, %s) = %v, want the unknown name's error", name, err)
		}
		_, err = NextFilter("acme", name)
		_, unknownNext := NextFilter("acme", "nope")
		if !errors.Is(err, ErrInvalid) || mask(err, name) != mask(unknownNext, "nope") {
			t.Errorf("NextFilter(acme, %s) = %v, want the unknown name's error", name, err)
		}
	}
}
