package scope

import (
	"os"
	"testing"
)

// formerYAML is the builtin table before the neutral one (launch dirs
// aside); scopetest.TwoYAML holds the same, but scopetest imports this
// package.
const formerYAML = `default: globex
scopes:
  perso: {vault_roots: [Perso/], sees_all: true}
  globex: {vault_roots: [Globex/], aliases: [gx]}
`

// TestMain runs the package's tests on the former builtin table, the one
// they were written against; the tests of the neutral builtin read Builtin()
// directly.
func TestMain(m *testing.M) {
	tb, err := ParseTable([]byte(formerYAML), "test")
	if err != nil {
		panic(err)
	}
	Use(tb)
	os.Exit(m.Run())
}
