// Package envlocal parses the `.env.local` files pigtail writes into every
// worktree, the way the shell that sources them would.
//
// Three packages read this same file — `internal/devserver` for the port and
// the owner stamp, `internal/backend/git` when a new worktree inherits its
// parent's — and they used to do it three different ways, disagreeing on
// `export NAME=`, on spacing, on trailing comments, and on which of two
// assignments wins. The consequence was not academic: a `PORT` one reader
// could not parse became a silent fall back to 3000, which is precisely the
// collision `pigtail dev` exists to prevent.
//
// The reference is the shell, not a dotenv dialect: the same file is sourced
// with `set -a && . .env.local` when a dev server starts (see
// internal/tui/commands.go). Anything read differently here than the shell
// assigns there means pigtail's picture of a worktree disagrees with the
// environment its server actually runs under.
//
// This package deliberately imports nothing from the rest of pigtail. That
// neutrality is what lets `internal/backend/git` — a backend, below the CLI —
// use it without acquiring a dependency edge upward into a CLI-facing package,
// which is the reason the parsing was duplicated in the first place.
package envlocal

import "strings"

// Owner is PIGTAIL_OWNER, the variable naming the session that owns a
// worktree's servers: written into .env.local by `pigtail new`
// (internal/backend/git) and read back by OwnerID (internal/devserver). One
// constant rather than a literal per package, so the two sides cannot drift.
const Owner = "PIGTAIL_OWNER"

// Browser is BROWSER, and BrowserNone the value that suppresses the browser tab
// a dev server would otherwise open on boot. Written into .env.local by
// `pigtail new` (internal/backend/git); read by nothing inside pigtail — the
// consumer is the dev server itself, which inherits it because `pigtail dev`
// sources the file (internal/devserver.wrapCommand). Vite honours it in
// openBrowser, Storybook through better-opn, and both skip opening when it is
// "none" — one lever that holds whatever the repo's own vite/storybook config
// says.
const (
	Browser     = "BROWSER"
	BrowserNone = "none"
)

// Lookup returns the value a shell sourcing `data` would assign to `name`, and
// whether the file assigns it at all.
//
// The LAST non-commented assignment wins, matching a shell evaluating the file
// top to bottom — a worktree that inherited a parent's .env.local before
// mergeEnvLocal rewrote it is the concrete case. An empty value reports ok:
// `NAME=` is an assignment, and a caller that must reject it (an unparseable
// port) needs to tell it from a file that never mentions the variable.
func Lookup(data []byte, name string) (string, bool) {
	val, n := LookupN(data, name)
	return val, n > 0
}

// LookupN is Lookup with the evidence: it also reports how many times the file
// assigns the variable. More than one is legal and unambiguous — the shell
// takes the last, and so does this package — but it is worth telling a user
// about, because it is rarely deliberate: the pre-fix mergeEnvLocal produced
// exactly that shape by appending a port line instead of rewriting the one it
// failed to recognise, so these files exist on disk today.
//
// The count comes from the same pass as the value so a caller that wants to
// warn does not have to parse the file a second time — which is how three
// readers of this file came to disagree in the first place.
func LookupN(data []byte, name string) (string, int) {
	val, count := "", 0
	for _, line := range strings.Split(string(data), "\n") {
		n, v, ok := ParseAssignment(line)
		if !ok || n != name {
			continue
		}
		val, count = v, count+1
	}
	return val, count
}

// ParseAssignment splits one .env.local line into a variable name and the
// value a shell sourcing the file would assign. ok is false for blank lines,
// full-line comments, and anything that is not an assignment.
//
// It covers the forms a hand-edited file actually produces — the README asks
// users to add PIGTAIL_OWNER by hand to worktrees predating the
// session-identity lot, and both `export NAME=` and a trailing `# comment` are
// second nature to a shell user.
func ParseAssignment(line string) (name, val string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	// `export NAME=value` assigns exactly like `NAME=value`. The keyword only
	// counts when followed by whitespace, so a variable named e.g. `exported`
	// is left alone.
	if rest, cut := strings.CutPrefix(trimmed, "export"); cut && strings.IndexAny(rest, " \t") == 0 {
		trimmed = strings.TrimLeft(rest, " \t")
	}
	rawName, rawVal, found := strings.Cut(trimmed, "=")
	if !found {
		return "", "", false
	}
	name = strings.TrimSpace(rawName)
	if name == "" {
		return "", "", false
	}
	return name, Value(rawVal), true
}

// AssignPrefix returns what an assignment line carries before the variable
// name — its indentation and any `export` keyword — so a caller rewriting the
// value can leave the line's shape intact. It returns "" when the line does
// not assign `name` at all.
//
// Whitespace around the `=` is deliberately not part of the prefix: `PORT =
// 3000` is a line this package tolerates on read but that a shell runs as a
// command, so rewriting it as `PORT=3001` turns it back into a real
// assignment. Everything else about the line is the author's business —
// `pigtail new` inherits a parent .env.local that people maintain by hand, and
// reformatting it is not part of allocating a port.
func AssignPrefix(line, name string) string {
	got, _, ok := ParseAssignment(line)
	if !ok || got != name {
		return ""
	}
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return ""
	}
	return strings.TrimSuffix(strings.TrimRight(line[:eq], " \t"), name)
}

// Value reduces the right-hand side of an assignment to the string a shell
// would assign: one pair of surrounding quotes removed, and a trailing comment
// dropped.
//
// A `#` only opens a comment when it follows unquoted whitespace. `foo#bar` is
// a single literal word to the shell, and `"a # b"` is quoted data — cutting on
// every `#` would corrupt both rather than fix anything.
func Value(raw string) string {
	raw = strings.TrimLeft(raw, " \t")
	if raw == "" {
		return ""
	}
	switch raw[0] {
	case '"', '\'':
		// Quoted: the value ends at the matching quote; whatever follows is a
		// comment or stray text, and is not part of it. An unterminated quote
		// falls through to the unquoted rule rather than swallowing the line.
		if end := strings.IndexByte(raw[1:], raw[0]); end >= 0 {
			return raw[1 : 1+end]
		}
	case '#':
		return ""
	}
	for i := 1; i < len(raw); i++ {
		if raw[i] == '#' && (raw[i-1] == ' ' || raw[i-1] == '\t') {
			raw = raw[:i]
			break
		}
	}
	return strings.TrimRight(raw, " \t")
}
