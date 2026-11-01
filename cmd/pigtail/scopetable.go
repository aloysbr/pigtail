package main

import (
	"fmt"
	"io"

	"github.com/aloysbr/pigtail/internal/scope"
)

// invalidTableWarning is what a broken scopes.yaml prints, on every command,
// in every cell: the deny-all table is active, so no cell is the master and
// none may read ParseTable's error, which names scopes and vault roots (D19).
// The daemon refuses to start on the same file and logs the detail.
const invalidTableWarning = "warning: scopes.yaml invalid — scoped calls refused until it is fixed (details: daemon.log)"

// loadScopeTable makes <dir>/scopes.yaml the CLI's scope table. A broken file
// leaves the deny-all table active (scope.Load) — never the builtin, whose one
// scope is the master — and says so on every call: the CLI keeps running (so
// `pigtail doctor` still answers), every scoped call refuses, nothing fails
// silently.
func loadScopeTable(dir string, stderr io.Writer) {
	tbl, _, err := scope.Load(dir)
	scope.Use(tbl)
	if err == nil {
		return
	}
	fmt.Fprintln(stderr, invalidTableWarning)
}
