// Package buildinfo carries where a pigtail binary was built from: the git
// branch, the full commit and whether tracked files were modified. The
// Makefile stamps the three variables with -ldflags -X into both binaries;
// an unstamped build (a bare `go build`, sources without .git) leaves them
// empty, rendered unknown — never an empty field, never dirty=true.
package buildinfo

import "fmt"

// Stamped at build time (Makefile, LDFLAGS_CLIENT).
var (
	// Branch is `git rev-parse --abbrev-ref HEAD`: HEAD when detached.
	Branch = ""
	// Commit is `git rev-parse HEAD`, 40 hex characters.
	Commit = ""
	// Dirty is "true" or "false": tracked files modified at build time.
	Dirty = ""
)

// Fields returns the stamped branch, commit and dirty, rendered: an empty
// field reads unknown, a detached HEAD reads detached.
func Fields() (branch, commit, dirty string) {
	return renderBranch(Branch), orUnknown(Commit), orUnknown(Dirty)
}

// Line is this binary's identity line:
// "<binary> <version> branch=<b> commit=<c> dirty=<d>".
func Line(binary, version string) string {
	return Format(binary, version, Branch, Commit, Dirty)
}

// Format renders an identity line from given fields — another binary's, as
// GET /version serves them — with the same rules as Line.
func Format(binary, version, branch, commit, dirty string) string {
	return fmt.Sprintf("%s %s branch=%s commit=%s dirty=%s",
		binary, orUnknown(version), renderBranch(branch), orUnknown(commit), orUnknown(dirty))
}

func renderBranch(b string) string {
	if b == "HEAD" {
		return "detached"
	}
	return orUnknown(b)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
