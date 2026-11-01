// Package scope holds the scope isolation policy of the subject layer.
//
// Every theme belongs to one scope, derived from its vault link through the
// scope table (vault root → scope; see Table); a subject inherits its theme's
// scope. The caller's scope comes from its Claude cell (the X-Pigtail-Scope
// header the CLI sets). The rule is asymmetric:
//
//   - the master scope (sees_all in the table) sees everything and writes its
//     own; writing onto another scope's object needs an explicit opt-in
//     (X-Pigtail-Cross-Scope: write);
//   - any other scope sees and writes itself only — an object of another
//     scope does not exist for it (same answer as an unknown id).
//
// A caller that sends no scope gets the table's default, never the master:
// the fail-closed side, so a bare curl, a browser or an unconfigured profile
// never reads every scope.
//
// Pure functions, no I/O — the daemon maps the errors onto HTTP codes. The
// policy reads the active table (Use, Active), the builtin one until the
// daemon or the CLI installs <config dir>/scopes.yaml.
package scope

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// Scope is the name of a declared scope (see Table).
type Scope string

// Any is the "no restriction" filter a master caller gets when it lists
// without narrowing. Scope names live in the table only (D1): no code names
// one (TestNoHardcodedScopeNames).
const Any Scope = ""

var (
	// ErrInvalid: a scope value, a filter or a vault link the policy cannot
	// read. Maps to 400.
	ErrInvalid = errors.New("invalid scope")
	// ErrNotVisible: the target lies outside what the caller may read. Maps
	// to 404, indistinguishable from an unknown id.
	ErrNotVisible = errors.New("not visible from this scope")
	// ErrCrossScope: the caller can see the target but may not act on it from
	// its scope (perso writing globex without opt-in, globex asking for perso).
	// Maps to 403 SCOPE_MISMATCH.
	ErrCrossScope = errors.New("cross-scope access refused")
)

var active atomic.Pointer[Table]

func init() { active.Store(Builtin()) }

// Use makes t the active table and returns a func restoring the previous one.
// The daemon and the CLI call it once at start; tests through scopetest.
// Not safe against a concurrent Use: no test of this module runs t.Parallel.
func Use(t *Table) (restore func()) {
	prev := active.Swap(t)
	return func() { active.Store(prev) }
}

// Active is the table the policy reads.
func Active() *Table { return active.Load() }

// Default is the scope of a caller that did not say: the table's default,
// never the master (fail-closed).
func Default() Scope { return Active().Default() }

// Master is the scope that sees every scope (sees_all).
func Master() Scope { return Active().Master() }

// SeesAll reports whether s is the master scope.
func SeesAll(s Scope) bool { return Active().SeesAll(s) }

// Parse reads a scope value (name or alias, any case) into its canonical name.
func Parse(s string) (Scope, error) { return Active().Parse(s) }

// Canonical is Parse for values kept rather than refused (a peer's cell
// scope): an undeclared value comes back lower-cased as is — CanRead then
// shows it to the master only.
func Canonical(s string) Scope {
	if sc, err := Parse(s); err == nil {
		return sc
	}
	return Scope(strings.ToLower(strings.TrimSpace(s)))
}

// FromHeader reads the caller's scope from a header value: absent means
// Default, anything else must parse. A table with no default (DenyAll)
// refuses an absent header too: "" would read as Any.
func FromHeader(v string) (Scope, error) {
	if strings.TrimSpace(v) == "" {
		if d := Default(); d != "" {
			return d, nil
		}
		return "", fmt.Errorf("%w: no scope table is active", ErrInvalid)
	}
	return Parse(v)
}

// FromVaultLink derives a theme's scope from its vault link: the scope owning
// the link's vault root. A theme with any other link has no scope and is
// refused at creation.
func FromVaultLink(link string) (Scope, error) { return Active().FromVaultLink(link) }

// CanRead: the master reads everything; any other scope reads itself only.
func CanRead(caller, target Scope) bool { return SeesAll(caller) || caller == target }

// CanWrite decides a write. An object the caller cannot read is ErrNotVisible
// (the opt-in never widens a non-master caller); the master writing another
// scope needs crossOptIn, else ErrCrossScope.
func CanWrite(caller, target Scope, crossOptIn bool) error {
	if !CanRead(caller, target) {
		return ErrNotVisible
	}
	if caller != target && !crossOptIn {
		return fmt.Errorf("%w: a %s caller writing a %s object needs the cross-scope opt-in", ErrCrossScope, caller, target)
	}
	return nil
}

// ParseFor parses a scope name sent by caller. To a caller that cannot read
// the named scope, the name does not exist (D19): the error is byte for byte
// the one an undeclared name gets, so neither the status nor the message
// tells "forbidden" from "unknown". The master gets Parse's answer.
func ParseFor(caller Scope, v string) (Scope, error) {
	s, err := Parse(v)
	if err != nil || !CanRead(caller, s) {
		return "", fmt.Errorf("%w%s", notDeclared(v), Hint(caller))
	}
	return s, nil
}

// FromVaultLinkFor: same for the vault root of a link — another scope's root
// reads as no declared root, with RootsHint(caller).
func FromVaultLinkFor(caller Scope, link string) (Scope, error) {
	s, err := FromVaultLink(link)
	if err != nil || !CanRead(caller, s) {
		return "", fmt.Errorf("%w%s", noDeclaredRoot(link), RootsHint(caller))
	}
	return s, nil
}

// NotDeclared is the error of an undeclared scope name, with no hint: the
// same text as the 400 of an unknown name elsewhere, listing no scope (the
// board's 400, D20).
func NotDeclared(v string) error { return notDeclared(v) }

// Hint completes an ErrInvalid for this caller: " (want acme)" — its own
// name — or, for the master, every name sorted then all, joined by ", ":
// " (want acme, globex, perso, all)". "" when the caller names nothing
// (unreadable header, undeclared cell). D19: an error never names a scope
// its caller cannot read.
func Hint(caller Scope) string {
	t := Active()
	names := t.NameableBy(caller)
	if len(names) == 0 {
		return ""
	}
	out := make([]string, 0, len(names)+1)
	for _, n := range names {
		out = append(out, string(n))
	}
	if t.SeesAll(caller) {
		out = append(out, "all")
	}
	return " (want " + strings.Join(out, ", ") + ")"
}

// RootsHint: same for a vault link, " (vault roots: ACME/)".
func RootsHint(caller Scope) string {
	roots := Active().RootsOf(caller)
	if len(roots) == 0 {
		return ""
	}
	return " (vault roots: " + strings.Join(roots, ", ") + ")"
}

// ListFilter turns a ?scope= query into the scope a listing is restricted to.
// It only ever narrows: a non-master caller gets its own scope ("" or its
// name), and asking for more is ErrCrossScope; the master gets everything by
// default (Any) or the scope it names.
func ListFilter(caller Scope, requested string) (Scope, error) {
	if !Active().Has(caller) {
		// An undeclared caller ("" on DenyAll) must never fall through to
		// "return caller", which reads as Any.
		return "", notDeclared(string(caller))
	}
	req := strings.ToLower(strings.TrimSpace(requested))
	if req != "" && req != "all" {
		return ParseFor(caller, req)
	}
	if SeesAll(caller) {
		return Any, nil
	}
	if req == "all" {
		return "", fmt.Errorf("%w: a %s caller cannot list all scopes", ErrCrossScope, caller)
	}
	return caller, nil
}

// NextFilter turns a ?scope= query on /subjects/next into one scope: the
// caller's own by default. "all" is refused — the two backlogs have separate
// rank orders, so there is no single "next" across them.
func NextFilter(caller Scope, requested string) (Scope, error) {
	req := strings.ToLower(strings.TrimSpace(requested))
	if req == "" {
		return caller, nil
	}
	if req == "all" {
		return "", fmt.Errorf("%w: next is per scope, not across all", ErrInvalid)
	}
	return ParseFor(caller, req)
}
