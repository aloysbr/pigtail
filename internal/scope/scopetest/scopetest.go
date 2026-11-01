// Package scopetest installs scope tables for tests. Each installing helper
// (Two, Three, RenamedMaster) swaps the process-wide table and restores it at
// cleanup, so it refuses a test that runs t.Parallel: testutil.SerialOnly
// panics instead of letting it race. The *Table helpers only parse a table
// and install nothing, so a parallel test may call them.
package scopetest

import (
	"testing"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/testutil"
)

// TwoYAML is a two-scope table (launch dirs aside): perso (master), globex
// (default, alias gx). For the tests first written against such a table.
const TwoYAML = `default: globex
scopes:
  perso: {vault_roots: [Perso/], sees_all: true}
  globex: {vault_roots: [Globex/], aliases: [gx]}
`

const ThreeYAML = `default: globex
scopes:
  perso: {vault_roots: [Perso/], sees_all: true}
  globex: {vault_roots: [Globex/], aliases: [gx]}
  acme: {vault_roots: [ACME/]}
`

const RenamedMasterYAML = `default: globex
scopes:
  root: {vault_roots: [Perso/], sees_all: true}
  globex: {vault_roots: [Globex/], aliases: [gx]}
  acme: {vault_roots: [ACME/]}
`

func parse(t testing.TB, body string) *scope.Table {
	t.Helper()
	tb, err := scope.ParseTable([]byte(body), "scopetest")
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func use(t testing.TB, body string) {
	t.Helper()
	testutil.SerialOnly(t)
	t.Cleanup(scope.Use(parse(t, body)))
}

// Two: perso (master), globex (default, alias gx) — the former builtin.
func Two(t testing.TB) { use(t, TwoYAML) }

// TwoTable is Two's table, parsed but not installed.
func TwoTable(t testing.TB) *scope.Table { return parse(t, TwoYAML) }

// Three: perso (master), globex (default, alias gx), acme.
func Three(t testing.TB) { use(t, ThreeYAML) }

// RenamedMaster: Three with the master called root — proves no call site
// names the master.
func RenamedMaster(t testing.TB) { use(t, RenamedMasterYAML) }

// ThreeTable is Three's table, parsed but not installed: for code that takes a
// *scope.Table argument (the space table, the cell table).
func ThreeTable(t testing.TB) *scope.Table { return parse(t, ThreeYAML) }

// MainTwo installs Two's table for a whole test binary: call it from the
// TestMain of a package whose tests were written against the former builtin
// (perso master, globex default). A test that needs another table still
// installs it (Three, scope.Use), restored to Two at its cleanup.
func MainTwo() {
	tb, err := scope.ParseTable([]byte(TwoYAML), "scopetest")
	if err != nil {
		panic(err)
	}
	scope.Use(tb)
}
