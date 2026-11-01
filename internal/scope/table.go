package scope

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// TableFile is the scope table's name inside the pigtail config dir.
const TableFile = "scopes.yaml"

//go:embed builtin_scopes.yaml
var builtinYAML []byte

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// reserved are the words the filters already give a meaning to.
var reserved = map[string]bool{"all": true, "any": true}

// Def is one declared scope.
type Def struct {
	Name       Scope
	VaultRoots []string
	Aliases    []string
	SeesAll    bool
	// LaunchDir is where pigtaild launches a session of this scope when its
	// subject has no live worktree (D3); "" = not declared. Absolute or ~/,
	// as written in the file: Table.LaunchDir expands it.
	LaunchDir string
}

// Table is a validated scope table. Immutable once built.
type Table struct {
	defs   map[Scope]Def
	alias  map[string]Scope
	names  []Scope
	master Scope
	def    Scope
	source string
	// anyLink is the one scope of a single-scope table that declares no
	// vault root: it owns every relative link. "" otherwise.
	anyLink Scope
}

type fileDef struct {
	VaultRoots []string `yaml:"vault_roots"`
	Aliases    []string `yaml:"aliases"`
	SeesAll    bool     `yaml:"sees_all"`
	LaunchDir  string   `yaml:"launch_dir"`
}

type fileFormat struct {
	Default string             `yaml:"default"`
	Scopes  map[string]fileDef `yaml:"scopes"`
}

// ParseTable reads and validates a scope table. source names it in errors
// (a path, or "builtin").
func ParseTable(b []byte, source string) (*Table, error) {
	fail := func(format string, a ...any) (*Table, error) {
		return nil, fmt.Errorf("%s: "+format, append([]any{source}, a...)...)
	}
	var f fileFormat
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return fail("%v", err)
	}
	t := &Table{defs: map[Scope]Def{}, alias: map[string]Scope{}, source: source}
	rootOwner := map[string]Scope{}
	single := len(f.Scopes) == 1
	for name, d := range f.Scopes {
		if !nameRE.MatchString(name) || reserved[name] {
			return fail("scope name %q: want [a-z][a-z0-9-]{0,31}, not all/any", name)
		}
		if len(d.VaultRoots) == 0 && single {
			t.anyLink = Scope(name)
		} else if len(d.VaultRoots) == 0 {
			return fail("scope %s declares no vault_roots", name)
		}
		for _, r := range d.VaultRoots {
			if r == "" || !strings.HasSuffix(r, "/") || strings.HasPrefix(r, "/") {
				return fail("scope %s: vault root %q must be relative and end with /", name, r)
			}
			if o, dup := rootOwner[r]; dup {
				return fail("vault root %q declared by %s and %s", r, o, name)
			}
			rootOwner[r] = Scope(name)
		}
		if d.LaunchDir != "" {
			if !strings.HasPrefix(d.LaunchDir, "/") && !strings.HasPrefix(d.LaunchDir, "~/") {
				return fail("scope %s: launch_dir %q must be absolute or start with ~/", name, d.LaunchDir)
			}
			if slices.Contains(strings.Split(d.LaunchDir, "/"), "..") {
				return fail("scope %s: launch_dir %q must not contain ..", name, d.LaunchDir)
			}
		}
		if d.SeesAll {
			if t.master != "" {
				return fail("two sees_all scopes (%s, %s): exactly one is the master scope", t.master, name)
			}
			t.master = Scope(name)
		}
		t.defs[Scope(name)] = Def{Name: Scope(name), VaultRoots: d.VaultRoots, Aliases: d.Aliases, SeesAll: d.SeesAll, LaunchDir: d.LaunchDir}
		t.names = append(t.names, Scope(name))
	}
	for name, d := range f.Scopes {
		for _, a := range d.Aliases {
			a = strings.ToLower(strings.TrimSpace(a))
			if !nameRE.MatchString(a) || reserved[a] {
				return fail("scope %s: alias %q: want [a-z][a-z0-9-]{0,31}, not all/any", name, a)
			}
			if _, isName := f.Scopes[a]; isName {
				return fail("scope %s: alias %q is already a scope name", name, a)
			}
			if o, dup := t.alias[a]; dup {
				return fail("alias %q declared by %s and %s", a, o, name)
			}
			t.alias[a] = Scope(name)
		}
	}
	roots := make([]string, 0, len(rootOwner))
	for r := range rootOwner {
		roots = append(roots, r)
	}
	sort.Strings(roots)
	for i := range roots {
		for j := range roots {
			if i != j && strings.HasPrefix(roots[j], roots[i]) {
				return fail("vault roots %q and %q overlap", roots[i], roots[j])
			}
		}
	}
	if t.master == "" {
		return fail("no scope has sees_all: true — exactly one must be the master scope")
	}
	def := Scope(strings.TrimSpace(f.Default))
	if _, ok := t.defs[def]; !ok {
		return fail("default %q is not a declared scope name", f.Default)
	}
	// One scope: it is the master, and the default too — there is no other
	// scope for a caller that names none. Two or more: the default must not
	// be the master (fail-closed).
	if single && def != t.master {
		return fail("default %q must be the only scope, %s", def, t.master)
	}
	if !single && def == t.master {
		return fail("default %q is the master scope: a caller that names no scope must not see everything", def)
	}
	t.def = def
	sort.Slice(t.names, func(i, j int) bool { return t.names[i] < t.names[j] })
	return t, nil
}

var builtin = sync.OnceValue(func() *Table {
	t, err := ParseTable(builtinYAML, "builtin")
	if err != nil {
		panic(err) // TestBuiltinIsNeutral keeps this unreachable
	}
	return t
})

// Builtin is the embedded table: one scope, personal, master and default,
// owning every vault link.
func Builtin() *Table { return builtin() }

// DenySource is the Source of the DenyAll table.
const DenySource = "deny-all"

// DenyAll is a table that declares no scope: every Parse, ParseFor,
// FromVaultLink and FromHeader refuses, and no name sees all. It is what an
// invalid scopes.yaml leaves active — never the builtin, whose one scope is
// the master.
func DenyAll() *Table {
	return &Table{defs: map[Scope]Def{}, alias: map[string]Scope{}, source: DenySource}
}

// Load reads <dir>/scopes.yaml. Absent: the builtin table, source "builtin",
// no error. Present and valid: its table, source = its path. Present and
// invalid (unreadable, unparsable, refused): the DenyAll table, source = its
// path, and the error — never the builtin: with one scope, it would make every
// caller the master. The daemon refuses to start on that error; the CLI runs
// on the deny-all table and warns.
func Load(dir string) (t *Table, source string, err error) {
	path := filepath.Join(dir, TableFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Builtin(), "builtin", nil
	}
	if err != nil {
		return DenyAll(), path, fmt.Errorf("read %s: %w", path, err)
	}
	t, err = ParseTable(b, path)
	if err != nil {
		return DenyAll(), path, err
	}
	return t, path, nil
}

// Parse reads a scope value: a declared name or alias, case and surrounding
// space tolerated; the result is always the canonical name.
func (t *Table) Parse(s string) (Scope, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if _, ok := t.defs[Scope(v)]; ok {
		return Scope(v), nil
	}
	if c, ok := t.alias[v]; ok {
		return c, nil
	}
	return "", notDeclared(s)
}

// notDeclared is the one error of a scope name the table does not declare —
// and, through ParseFor, of one the caller may not read (D19): one
// constructor, so the two bodies are identical by construction.
func notDeclared(v string) error { return fmt.Errorf("%w: %q is not a declared scope", ErrInvalid, v) }

// noDeclaredRoot is notDeclared for the vault root of a link.
func noDeclaredRoot(link string) error {
	return fmt.Errorf("%w: vault_link %q starts with no declared vault root", ErrInvalid, link)
}

// FromVaultLink returns the scope owning the link's vault root — in a
// single-scope table without roots, that scope for any relative link.
func (t *Table) FromVaultLink(link string) (Scope, error) {
	if t.anyLink != "" && link != "" && !strings.HasPrefix(link, "/") {
		return t.anyLink, nil
	}
	for _, n := range t.names {
		for _, r := range t.defs[n].VaultRoots {
			if strings.HasPrefix(link, r) {
				return n, nil
			}
		}
	}
	return "", noDeclaredRoot(link)
}

func (t *Table) SeesAll(s Scope) bool { return s != "" && s == t.master }
func (t *Table) Master() Scope        { return t.master }
func (t *Table) Default() Scope       { return t.def }
func (t *Table) Has(s Scope) bool     { _, ok := t.defs[s]; return ok }
func (t *Table) Source() string       { return t.source }
func (t *Table) Names() []Scope       { return append([]Scope(nil), t.names...) }

// LaunchDir is the directory pigtaild launches a session of s in when its
// subject has no live worktree (D3): ~/ expanded with os.UserHomeDir, the
// path cleaned. Its existence is not checked (the launcher does). false when
// s declares none, is unknown, or ~ cannot be expanded.
func (t *Table) LaunchDir(s Scope) (string, bool) {
	d := t.defs[s].LaunchDir
	if d == "" {
		return "", false
	}
	if rest, ok := strings.CutPrefix(d, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		d = filepath.Join(home, rest)
	}
	return filepath.Clean(d), true
}

// Aliases returns the read-only spellings of s, sorted, in a new slice each
// call; empty for a scope without alias or one the table does not declare.
func (t *Table) Aliases(s Scope) []string {
	out := []string{}
	for a, c := range t.alias {
		if c == s {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

// NameableBy is what a caller may name: every scope for the master, its own
// otherwise (D19) — an error never teaches a scope the caller cannot read.
func (t *Table) NameableBy(caller Scope) []Scope {
	if t.SeesAll(caller) {
		return t.Names()
	}
	if t.Has(caller) {
		return []Scope{caller}
	}
	return nil
}

// RootsOf: the vault roots of the scopes NameableBy(caller) returns.
func (t *Table) RootsOf(caller Scope) []string {
	var out []string
	for _, n := range t.NameableBy(caller) {
		out = append(out, t.defs[n].VaultRoots...)
	}
	return out
}
