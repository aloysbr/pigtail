// Package space holds the space table: the cmux sidebar folders a theme's
// sessions go to (sub_x000000000000041, D1), in folder order (D6).
package space

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/aloysbr/pigtail/internal/scope"
)

//go:embed builtin_spaces.yaml
var builtinYAML []byte

// The two folders that are not spaces (D3, D4), always after the spaces, and
// their icons (D11).
const (
	NoSubject = "Inbox" // lot 6 (sub_x000000000000010): was FormerNoSubject (« Sans sujet » before D9 of sub_x000000000000007)
	// FormerNoSubject is the previous no-subject label: a folder pigtail made
	// under it is renamed in place (cmuxsync renameFormer). Not reserved.
	FormerNoSubject = "Other"
	Standby         = "Standby"
	NoSubjectIcon   = "tray"
	StandbyIcon     = "moon.zzz"
	FocusIcon       = "target" // focus D19: a focus folder, temporary (sub_x000000000000007)
	// Prio stays a reserved name (D24 of sub_x000000000000014): no space and
	// no focus may take it, though pigtail has no « Prio » folder any more
	// (the prio is cmux's own pin, D3).
	Prio = "Prio"
	// MacMaintenance (maintenance window, G1): pigtail's own folder for the
	// sessions that announced how long they need the Mac (`--need`),
	// just above « Standby ». Not « Maintenance » the system gesture
	// (cmuxsync types.go): this constant names the folder only.
	MacMaintenance     = "Maintenance"
	MacMaintenanceIcon = "wrench.and.screwdriver"
)

var symbolRE = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9]+)*$`)

const maxNameRunes = 64

// ErrInvalid wraps every table error.
var ErrInvalid = errors.New("space: invalid table")

// Space is one row of the table.
type Space struct {
	Name  string `yaml:"name" json:"name"`
	Scope string `yaml:"scope" json:"scope"`
}

// Table is the parsed space table, in folder order.
type Table struct {
	spaces []Space
	byName map[string]Space
	icons  map[string]string // scope → SF Symbol (D11)
	source string
}

func checkName(n string) error { return checkNamed("space name", n) }

// checkNamed applies the name rules; noun leads each message (« space name »,
// « focus name », focus D28), the rule text is the same for both.
func checkNamed(noun, n string) error {
	if strings.TrimSpace(n) == "" || strings.HasPrefix(n, "-") || len([]rune(n)) > maxNameRunes {
		return fmt.Errorf("%s %q: 1 to %d characters, not starting with -", noun, n, maxNameRunes)
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s %q: control character", noun, n)
		}
	}
	if n == NoSubject || n == Standby || n == Prio || n == MacMaintenance {
		return fmt.Errorf("%s %q is reserved", noun, n)
	}
	return nil
}

// CheckFocusName applies the space name rules to a focus name
// (sub_x000000000000007): 1–64 runes, not starting with -, no control
// character, neither « Inbox », « Standby », « Prio » nor « Maintenance ». Its messages say « focus
// name » (focus D28).
func CheckFocusName(n string) error { return checkNamed("focus name", n) }

// Parse reads a table. Every space names a scope the scope table declares
// (canonical name, never an alias). A table may declare no space: every
// session then goes to « Inbox ».
func Parse(b []byte, source string, scopes *scope.Table) (*Table, error) {
	var raw struct {
		Spaces []Space           `yaml:"spaces"`
		Icons  map[string]string `yaml:"icons"`
	}
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, source, err)
	}
	t := &Table{byName: map[string]Space{}, icons: map[string]string{}, source: source}
	for sc, sym := range raw.Icons {
		if !scopes.Has(scope.Scope(sc)) || !symbolRE.MatchString(sym) || len(sym) > maxNameRunes {
			return nil, fmt.Errorf("%w: %s: icon %q for scope %q", ErrInvalid, source, sym, sc)
		}
		t.icons[sc] = sym
	}
	for _, s := range raw.Spaces {
		if err := checkName(s.Name); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, source, err)
		}
		if _, dup := t.byName[s.Name]; dup {
			return nil, fmt.Errorf("%w: %s: space %q declared twice", ErrInvalid, source, s.Name)
		}
		if !scopes.Has(scope.Scope(s.Scope)) {
			return nil, fmt.Errorf("%w: %s: space %q: scope %q is not declared", ErrInvalid, source, s.Name, s.Scope)
		}
		t.spaces = append(t.spaces, s)
		t.byName[s.Name] = s
	}
	return t, nil
}

// Builtin parses the built-in table against the scope table.
func Builtin(scopes *scope.Table) (*Table, error) { return Parse(builtinYAML, "builtin", scopes) }

// LoadOrBuiltin reads <dir>/spaces.yaml, or the built-in table when absent.
func LoadOrBuiltin(dir string, scopes *scope.Table) (*Table, error) {
	p := filepath.Join(dir, "spaces.yaml")
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Builtin(scopes)
	}
	if err != nil {
		return nil, err
	}
	return Parse(b, p, scopes)
}

// Names lists the spaces in folder order.
func (t *Table) Names() []string {
	out := make([]string, 0, len(t.spaces))
	for _, s := range t.spaces {
		out = append(out, s.Name)
	}
	return out
}

// FolderOrder is the sidebar folder order (D6): the spaces, then « Inbox »,
// then « Maintenance » (G1 of the maintenance window), then « Standby ».
func (t *Table) FolderOrder() []string {
	return append(t.Names(), NoSubject, MacMaintenance, Standby)
}

// Icon is the SF Symbol of a folder (D11): its space's scope icon, tray for
// « Inbox », moon.zzz for « Standby », "" for an unknown folder or a scope
// without icon (cmux's default stays).
func (t *Table) Icon(folder string) string {
	switch folder {
	case NoSubject:
		return NoSubjectIcon
	case Standby:
		return StandbyIcon
	case MacMaintenance:
		return MacMaintenanceIcon
	}
	if s, ok := t.byName[folder]; ok {
		return t.icons[s.Scope]
	}
	return ""
}

// Get returns a space by name.
func (t *Table) Get(name string) (Space, bool) { s, ok := t.byName[name]; return s, ok }

// VisibleTo lists the spaces a caller may name: all for the master, its own
// scope's otherwise.
func (t *Table) VisibleTo(caller scope.Scope, scopes *scope.Table) []Space {
	var out []Space
	for _, s := range t.spaces {
		if scopes.SeesAll(caller) || s.Scope == string(caller) {
			out = append(out, s)
		}
	}
	return out
}
