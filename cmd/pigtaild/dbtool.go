package main

import (
	"os"

	"github.com/aloysbr/pigtail/internal/dbtool"
	"github.com/aloysbr/pigtail/internal/scope"
)

// dbToolMode handles `pigtaild -db-tool list|restore <flags…>`: the engine
// of `pigtail db`, run here because pigtaild links the SQLite driver and the
// CLI must not (plan 2026-10-28-cli-startup-modernc). A one-shot like
// -version: read before any flag parsing, the daemon never starts. The scope
// table is the daemon's (scopes.yaml beside the cell table); a broken file
// leaves the deny-all table active — the CLI already warned before running us.
func dbToolMode(args []string, o dbtool.IO, loadTable func() (*scope.Table, error)) (handled bool, code int) {
	if len(args) == 0 || (args[0] != "-db-tool" && args[0] != "--db-tool") {
		return false, 0
	}
	tbl, _ := loadTable() // scope.Load returns the deny-all table with its error
	if tbl != nil {
		defer scope.Use(tbl)()
	}
	return true, dbtool.Run(args[1:], o)
}

// isStdinTTY: the same test as the CLI's isTTYFn (cmd/pigtail/dev.go).
func isStdinTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
