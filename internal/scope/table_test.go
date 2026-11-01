package scope

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const threeYAML = `default: globex
scopes:
  perso: {vault_roots: [Perso/], sees_all: true}
  globex: {vault_roots: [Globex/], aliases: [gx]}
  acme: {vault_roots: [ACME/, CLIENTS/acme/]}
`

func TestParseTableThreeScopes(t *testing.T) {
	tb, err := ParseTable([]byte(threeYAML), "test")
	if err != nil {
		t.Fatal(err)
	}
	if tb.Master() != "perso" || tb.Default() != "globex" {
		t.Fatalf("master=%q default=%q", tb.Master(), tb.Default())
	}
	if got := tb.Names(); len(got) != 3 || got[0] != "acme" || got[1] != "globex" || got[2] != "perso" {
		t.Fatalf("names = %v", got)
	}
	for in, want := range map[string]Scope{"gx": "globex", " Gx ": "globex", "GLOBEX": "globex", "acme": "acme", "perso": "perso"} {
		if got, err := tb.Parse(in); err != nil || got != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "all", "any", "work", "Globex/"} {
		if _, err := tb.Parse(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) = %v, want ErrInvalid", in, err)
		}
	}
	for link, want := range map[string]Scope{"Globex/Projects/x": "globex", "Perso/Areas/y": "perso", "ACME/x": "acme", "CLIENTS/acme/y": "acme"} {
		if got, err := tb.FromVaultLink(link); err != nil || got != want {
			t.Errorf("FromVaultLink(%q) = %q, %v; want %q", link, got, err, want)
		}
	}
	for _, link := range []string{"", "gx/x", "CLIENTS/other/x", "Globex"} {
		if _, err := tb.FromVaultLink(link); !errors.Is(err, ErrInvalid) {
			t.Errorf("FromVaultLink(%q) = %v, want ErrInvalid", link, err)
		}
	}
	if !tb.SeesAll("perso") || tb.SeesAll("globex") || tb.SeesAll("acme") || tb.SeesAll("nope") {
		t.Fatal("SeesAll must follow sees_all only")
	}
}

func TestParseTableRefusals(t *testing.T) {
	cases := map[string]string{
		"no master":            "default: a\nscopes:\n  a: {vault_roots: [A/]}\n  b: {vault_roots: [B/]}\n",
		"two masters":          "default: a\nscopes:\n  a: {vault_roots: [A/]}\n  b: {vault_roots: [B/], sees_all: true}\n  c: {vault_roots: [C/], sees_all: true}\n",
		"default is master":    "default: b\nscopes:\n  a: {vault_roots: [A/]}\n  b: {vault_roots: [B/], sees_all: true}\n",
		"default undeclared":   "default: z\nscopes:\n  a: {vault_roots: [A/]}\n  b: {vault_roots: [B/], sees_all: true}\n",
		"default is an alias":  "default: x\nscopes:\n  a: {vault_roots: [A/], aliases: [x]}\n  b: {vault_roots: [B/], sees_all: true}\n",
		"uppercase name":       "default: a\nscopes:\n  A: {vault_roots: [A/]}\n  b: {vault_roots: [B/], sees_all: true}\n",
		"reserved name":        "default: a\nscopes:\n  a: {vault_roots: [A/]}\n  all: {vault_roots: [B/], sees_all: true}\n",
		"no root":              "default: a\nscopes:\n  a: {}\n  b: {vault_roots: [B/], sees_all: true}\n",
		"root without slash":   "default: a\nscopes:\n  a: {vault_roots: [A]}\n  b: {vault_roots: [B/], sees_all: true}\n",
		"overlapping roots":    "default: a\nscopes:\n  a: {vault_roots: [A/]}\n  b: {vault_roots: [A/b/], sees_all: true}\n",
		"alias shadows a name": "default: a\nscopes:\n  a: {vault_roots: [A/], aliases: [b]}\n  b: {vault_roots: [B/], sees_all: true}\n",
		"duplicate alias":      "default: a\nscopes:\n  a: {vault_roots: [A/], aliases: [x]}\n  b: {vault_roots: [B/], sees_all: true, aliases: [x]}\n",
		"unknown key":          "default: a\nscopes:\n  a: {vault_roots: [A/], seesall: true}\n  b: {vault_roots: [B/], sees_all: true}\n",
	}
	for name, body := range cases {
		if _, err := ParseTable([]byte(body), "test"); err == nil {
			t.Errorf("%s: accepted, want an error", name)
		} else if !strings.Contains(err.Error(), "test") {
			t.Errorf("%s: error %q does not name its source", name, err)
		}
	}
}

// A table of one scope: that scope is the master and the default (nobody
// else to fall back to), and it may declare no vault root.
const soloYAML = "default: solo\nscopes:\n  solo:\n    sees_all: true\n"

func TestParseTableSingleScopeDefaultIsMaster(t *testing.T) {
	tb, err := ParseTable([]byte(soloYAML), "t")
	if err != nil {
		t.Fatal(err)
	}
	if tb.Master() != "solo" || tb.Default() != "solo" {
		t.Fatalf("master=%q default=%q, want solo/solo", tb.Master(), tb.Default())
	}
}

func TestParseTableSingleScopeNoRootsTakesAnyLink(t *testing.T) {
	tb, err := ParseTable([]byte(soloYAML), "t")
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{"Notes/Projects/x", "x", "Perso/y"} {
		if got, err := tb.FromVaultLink(link); err != nil || got != "solo" {
			t.Errorf("FromVaultLink(%q) = %q, %v; want solo", link, got, err)
		}
	}
	for _, link := range []string{"", "/abs/x"} {
		if _, err := tb.FromVaultLink(link); !errors.Is(err, ErrInvalid) {
			t.Errorf("FromVaultLink(%q) = %v, want ErrInvalid (not a relative link)", link, err)
		}
	}
	// A single scope that does declare roots keeps them.
	rooted, err := ParseTable([]byte("default: solo\nscopes:\n  solo: {vault_roots: [N/], sees_all: true}\n"), "t")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooted.FromVaultLink("Other/x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a declared root must still be required: %v", err)
	}
}

func TestParseTableSingleScopeRefusals(t *testing.T) {
	for name, body := range map[string]string{
		"default not the master": "default: other\nscopes:\n  solo: {sees_all: true}\n",
		"no default":             "scopes:\n  solo: {sees_all: true}\n",
		"no master":              "default: solo\nscopes:\n  solo: {}\n",
	} {
		if _, err := ParseTable([]byte(body), "t"); err == nil {
			t.Errorf("%s: accepted, want an error", name)
		}
	}
}

func TestParseTableTwoScopesDefaultMasterRefused(t *testing.T) {
	_, err := ParseTable([]byte("default: a\nscopes:\n  a: {vault_roots: [A/], sees_all: true}\n  b: {vault_roots: [B/]}\n"), "t")
	if err == nil || !strings.Contains(err.Error(), "is the master scope") {
		t.Fatalf("err = %v, want the « is the master scope » refusal", err)
	}
}

func TestParseTableTwoScopesRootsStillRequired(t *testing.T) {
	_, err := ParseTable([]byte("default: b\nscopes:\n  a: {vault_roots: [A/], sees_all: true}\n  b: {}\n"), "t")
	if err == nil || !strings.Contains(err.Error(), "vault_roots") {
		t.Fatalf("err = %v, want the « declares no vault_roots » refusal", err)
	}
}

// The builtin names nobody's org: one scope, personal, master and default,
// no vault root, no launch dir.
func TestBuiltinIsNeutral(t *testing.T) {
	b := Builtin()
	if got := b.Names(); len(got) != 1 || got[0] != "personal" {
		t.Fatalf("builtin names = %v, want [personal]", got)
	}
	if b.Default() != "personal" || b.Master() != "personal" || b.Source() != "builtin" {
		t.Fatalf("builtin master=%q default=%q source=%q", b.Master(), b.Default(), b.Source())
	}
	if d, ok := b.LaunchDir("personal"); ok {
		t.Fatalf("builtin launch_dir = %q, want none", d)
	}
	if got, err := b.FromVaultLink("Notes/x"); err != nil || got != "personal" {
		t.Fatalf("builtin FromVaultLink = %q, %v", got, err)
	}
	if strings.Contains(string(builtinYAML), "sub_") || regexp.MustCompile(`\bD[0-9]+\b`).Match(builtinYAML) {
		t.Fatal("the builtin's comment cites a subject id or a decision number")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if tb, src, err := Load(dir); err != nil || tb != Builtin() || src != "builtin" {
		t.Fatalf("absent file: %v, %q, %v", tb.Source(), src, err)
	}
	path := filepath.Join(dir, TableFile)
	if err := os.WriteFile(path, []byte(threeYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if tb, src, err := Load(dir); err != nil || !tb.Has("acme") || tb.Source() != path || src != path {
		t.Fatalf("valid file: has acme=%v source=%q src=%q err=%v", tb.Has("acme"), tb.Source(), src, err)
	}
}

// An invalid scopes.yaml never falls back to the builtin: with a neutral
// single-scope builtin, the fallback would make every caller the master.
// Load hands back the deny-all table and the error.
func TestInvalidFileNeverFallsBackToMaster(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, TableFile)
	for name, body := range map[string]string{
		"unparsable":     "default: [\n",
		"bad launch_dir": "default: globex\nscopes:\n  perso: {vault_roots: [Perso/], sees_all: true, launch_dir: foo}\n  globex: {vault_roots: [Globex/]}\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		tb, src, err := Load(dir)
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("%s: err = %v, want one naming %s", name, err, path)
		}
		if tb == nil || tb == Builtin() || src != path {
			t.Fatalf("%s: table %v, src %q: want the deny-all table, the path as source", name, tb, src)
		}
		for _, n := range append(Builtin().Names(), "perso", "globex", "") {
			if tb.SeesAll(n) || tb.Has(n) {
				t.Errorf("%s: deny-all table grants %q (sees_all=%v has=%v)", name, n, tb.SeesAll(n), tb.Has(n))
			}
		}
		if _, err := tb.FromVaultLink("Notes/x"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: deny-all FromVaultLink = %v, want ErrInvalid", name, err)
		}
		func() {
			defer Use(tb)()
			if _, err := ParseFor("", ""); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: ParseFor on deny-all = %v, want ErrInvalid", name, err)
			}
			if sc, err := FromHeader(""); err == nil {
				t.Errorf("%s: a caller naming no scope got %q on deny-all, want a refusal", name, sc)
			}
			if sc, err := ListFilter(Default(), ""); err == nil {
				t.Errorf("%s: ListFilter on deny-all = %q, want a refusal", name, sc)
			}
		}()
	}
}

func TestDenyAll(t *testing.T) {
	d := DenyAll()
	if len(d.Names()) != 0 || d.Master() != "" || d.Default() != "" {
		t.Fatalf("deny-all names=%v master=%q default=%q", d.Names(), d.Master(), d.Default())
	}
}

// Aliases: the read-only spellings of a scope, sorted, a fresh slice each
// call; none for a scope without alias or an unknown one.
func TestTableAliases(t *testing.T) {
	b, err := ParseTable([]byte(threeYAML), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Aliases("globex"); len(got) != 1 || got[0] != "gx" {
		t.Fatalf("Aliases(globex) = %v, want [gx]", got)
	}
	if got := b.Aliases("perso"); len(got) != 0 {
		t.Fatalf("Aliases(perso) = %v, want none", got)
	}
	if got := b.Aliases("nope"); len(got) != 0 {
		t.Fatalf("Aliases(nope) = %v, want none", got)
	}
	got := b.Aliases("globex")
	got[0] = "mutated"
	if again := b.Aliases("globex"); again[0] != "gx" {
		t.Fatalf("the returned slice is the table's: %v", again)
	}
	tb, err := ParseTable([]byte(`default: globex
scopes:
  perso: {vault_roots: [Perso/], sees_all: true}
  globex: {vault_roots: [Globex/], aliases: [ts, gx]}
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := tb.Aliases("globex"); strings.Join(got, ",") != "gx,ts" {
		t.Fatalf("Aliases = %v, want sorted [gx ts]", got)
	}
}

// of the web launch subject: a scope's launch_dir, ~/ expanded at read.
func TestLaunchDirExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	b, err := ParseTable([]byte(`default: globex
scopes:
  perso: {vault_roots: [Perso/], sees_all: true, launch_dir: ~/code/home}
  globex: {vault_roots: [Globex/], launch_dir: ~/code/globex}
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := b.LaunchDir("perso"); !ok || got != filepath.Join(home, "code", "home") {
		t.Fatalf("LaunchDir(perso) = %q, %v; want %s/code/home", got, ok, home)
	}
	if got, ok := b.LaunchDir("globex"); !ok || got != filepath.Join(home, "code", "globex") {
		t.Fatalf("LaunchDir(globex) = %q, %v", got, ok)
	}
	tb, err := ParseTable([]byte(threeYAML), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := tb.LaunchDir("acme"); ok || got != "" {
		t.Fatalf("an undeclared launch_dir = %q, %v; want none", got, ok)
	}
	if got, ok := tb.LaunchDir("nope"); ok || got != "" {
		t.Fatalf("an unknown scope's launch_dir = %q, %v; want none", got, ok)
	}
	abs, err := ParseTable([]byte(`default: globex
scopes:
  perso: {vault_roots: [Perso/], sees_all: true, launch_dir: /w/perso/}
  globex: {vault_roots: [Globex/]}
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := abs.LaunchDir("perso"); !ok || got != "/w/perso" {
		t.Fatalf("absolute launch_dir = %q, %v; want /w/perso (cleaned)", got, ok)
	}
}

func TestLaunchDirRejectsRelative(t *testing.T) {
	for dir, want := range map[string]string{
		"foo": "must be absolute or start with ~/", "./foo": "must be absolute or start with ~/", "~foo": "must be absolute or start with ~/",
		"/w/../etc": "must not contain ..", "~/a/../../b": "must not contain ..",
	} {
		y := "default: globex\nscopes:\n  perso: {vault_roots: [Perso/], sees_all: true, launch_dir: \"" + dir + "\"}\n  globex: {vault_roots: [Globex/]}\n"
		_, err := ParseTable([]byte(y), "test")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("launch_dir %q: err = %v, want one saying it %s", dir, err, want)
		}
	}
}
